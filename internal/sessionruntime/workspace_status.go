package sessionruntime

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	kelos "github.com/kelos-dev/kelos/api/v1alpha2"
)

const (
	workspaceStatusFileName       = "workspace-status.json"
	workspaceStatusCommandTimeout = 5 * time.Second
	pullRequestMergeQueueQuery    = `query($id:ID!){node(id:$id){... on PullRequest{mergeQueueEntry{headCommit{statusCheckRollup{contexts(first:100){nodes{__typename ... on CheckRun{status conclusion} ... on StatusContext{state}}}}}}}}}`
)

// WorkspaceStatus describes the git work currently visible in a Session.
type WorkspaceStatus struct {
	Branch      string                    `json:"branch,omitempty"`
	PullRequest *kelos.SessionPullRequest `json:"pullRequest,omitempty"`
}

type workspaceStatusRunner interface {
	run(context.Context, string, string, ...string) (string, error)
}

type githubStatusCheck struct {
	TypeName   string `json:"__typename"`
	Status     string `json:"status"`
	Conclusion string `json:"conclusion"`
	State      string `json:"state"`
}

type githubMergeQueueEntry struct {
	HeadCommit *struct {
		StatusCheckRollup *struct {
			Contexts struct {
				Nodes []githubStatusCheck `json:"nodes"`
			} `json:"contexts"`
		} `json:"statusCheckRollup"`
	} `json:"headCommit"`
}

type realWorkspaceStatusRunner struct{}

func (realWorkspaceStatusRunner) run(ctx context.Context, workingDir, name string, args ...string) (string, error) {
	commandCtx, cancel := context.WithTimeout(ctx, workspaceStatusCommandTimeout)
	defer cancel()
	command := exec.CommandContext(commandCtx, name, args...)
	command.Dir = workingDir
	output, err := command.Output()
	return strings.TrimSpace(string(output)), err
}

func captureWorkspaceStatus(ctx context.Context, runner workspaceStatusRunner, workingDir string, environment []string) (WorkspaceStatus, error) {
	isGitWorkspace, err := isGitWorkspace(ctx, runner, workingDir)
	if err != nil {
		return WorkspaceStatus{}, err
	}
	if !isGitWorkspace {
		return WorkspaceStatus{}, nil
	}
	branch, err := runner.run(ctx, workingDir, "git", "branch", "--show-current")
	if err != nil {
		return WorkspaceStatus{}, fmt.Errorf("reading Session git branch: %w", err)
	}
	if branch == "" {
		return WorkspaceStatus{}, nil
	}
	status := WorkspaceStatus{Branch: branch}
	headOwner, err := repositoryOwner(ctx, runner, workingDir, branch)
	if err != nil {
		return status, err
	}
	status.PullRequest, err = findPullRequest(ctx, runner, workingDir, branch, "", headOwner)
	if err != nil {
		return status, err
	}
	if status.PullRequest == nil {
		if upstreamRepo := environmentValue(environment, "KELOS_UPSTREAM_REPO"); upstreamRepo != "" {
			status.PullRequest, err = findPullRequest(ctx, runner, workingDir, branch, upstreamRepo, headOwner)
			if err != nil {
				return status, err
			}
		}
	}
	return status, nil
}

func repositoryOwner(ctx context.Context, runner workspaceStatusRunner, workingDir, branch string) (string, error) {
	remote, err := runner.run(ctx, workingDir, "git", "for-each-ref", "--format=%(push:remotename)", "refs/heads/"+branch)
	if err != nil {
		return "", fmt.Errorf("reading Session branch push remote: %w", err)
	}
	if remote == "" {
		remote = "origin"
	}
	remoteURL, err := runner.run(ctx, workingDir, "git", "remote", "get-url", "--push", remote)
	if err != nil {
		return "", fmt.Errorf("reading Session branch push URL: %w", err)
	}
	owner, err := repositoryOwnerFromRemoteURL(remoteURL)
	if err != nil {
		return "", fmt.Errorf("identifying Session branch push repository: %w", err)
	}
	return owner, nil
}

func repositoryOwnerFromRemoteURL(remoteURL string) (string, error) {
	repositoryPath := remoteURL
	parsed, err := url.Parse(remoteURL)
	if err == nil && parsed.Scheme != "" {
		repositoryPath = parsed.Path
	} else if prefix, path, ok := strings.Cut(remoteURL, ":"); ok && strings.Contains(prefix, "@") {
		repositoryPath = path
	}
	repositoryPath = strings.Trim(strings.TrimSuffix(repositoryPath, ".git"), "/")
	parts := strings.Split(repositoryPath, "/")
	if len(parts) < 2 || parts[len(parts)-2] == "" || parts[len(parts)-1] == "" {
		return "", fmt.Errorf("invalid remote URL %q", remoteURL)
	}
	return parts[len(parts)-2], nil
}

func findPullRequest(ctx context.Context, runner workspaceStatusRunner, workingDir, branch, repo, headOwner string) (*kelos.SessionPullRequest, error) {
	args := []string{"pr", "list", "--head", branch, "--state", "all", "--json", "id,url,headRepositoryOwner,state,isDraft,statusCheckRollup", "--limit", "100"}
	if repo != "" {
		args = append(args, "--repo", repo)
	}
	output, err := runner.run(ctx, workingDir, "gh", args...)
	if err != nil {
		return nil, fmt.Errorf("listing pull requests: %w", err)
	}
	if output == "" {
		return nil, nil
	}
	var pullRequests []struct {
		ID                  string `json:"id"`
		URL                 string `json:"url"`
		State               string `json:"state"`
		IsDraft             bool   `json:"isDraft"`
		HeadRepositoryOwner struct {
			Login string `json:"login"`
		} `json:"headRepositoryOwner"`
		StatusCheckRollup []githubStatusCheck `json:"statusCheckRollup"`
	}
	if err := json.Unmarshal([]byte(output), &pullRequests); err != nil {
		return nil, fmt.Errorf("decoding pull requests: %w", err)
	}
	for _, pullRequest := range pullRequests {
		if strings.EqualFold(pullRequest.HeadRepositoryOwner.Login, headOwner) {
			state, err := sessionPullRequestState(pullRequest.State, pullRequest.IsDraft)
			if err != nil {
				return nil, err
			}
			checks := pullRequest.StatusCheckRollup
			mergeQueueEntry, err := findPullRequestMergeQueueEntry(ctx, runner, workingDir, pullRequest.ID)
			if err != nil {
				log.Printf("Unable to read pull request merge queue, using pull request checks error=%v", err)
			} else if mergeQueueEntry != nil {
				state = kelos.SessionPullRequestStateQueued
				checks = nil
				if mergeQueueEntry.HeadCommit != nil && mergeQueueEntry.HeadCommit.StatusCheckRollup != nil {
					checks = mergeQueueEntry.HeadCommit.StatusCheckRollup.Contexts.Nodes
				}
			}
			return &kelos.SessionPullRequest{
				URL:    pullRequest.URL,
				State:  state,
				Checks: sessionPullRequestChecks(checks),
			}, nil
		}
	}
	return nil, nil
}

func findPullRequestMergeQueueEntry(ctx context.Context, runner workspaceStatusRunner, workingDir, pullRequestID string) (*githubMergeQueueEntry, error) {
	output, err := runner.run(ctx, workingDir, "gh", "api", "graphql", "-F", "id="+pullRequestID, "-f", "query="+pullRequestMergeQueueQuery)
	if err != nil {
		return nil, fmt.Errorf("reading pull request merge queue: %w", err)
	}
	var response struct {
		Data struct {
			Node struct {
				MergeQueueEntry *githubMergeQueueEntry `json:"mergeQueueEntry"`
			} `json:"node"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(output), &response); err != nil {
		return nil, fmt.Errorf("decoding pull request merge queue: %w", err)
	}
	return response.Data.Node.MergeQueueEntry, nil
}

func sessionPullRequestChecks(checks []githubStatusCheck) *kelos.SessionPullRequestChecks {
	if len(checks) == 0 {
		return nil
	}

	result := &kelos.SessionPullRequestChecks{
		State: kelos.SessionPullRequestChecksStateSuccess,
		Total: int32(len(checks)),
	}
	for _, check := range checks {
		completed, failed := completedGitHubCheck(check)
		if completed {
			result.Completed++
		}
		if failed {
			result.State = kelos.SessionPullRequestChecksStateFailure
		}
	}
	if result.State != kelos.SessionPullRequestChecksStateFailure && result.Completed < result.Total {
		result.State = kelos.SessionPullRequestChecksStatePending
	}
	return result
}

func completedGitHubCheck(check githubStatusCheck) (completed, failed bool) {
	switch check.TypeName {
	case "CheckRun":
		if !strings.EqualFold(check.Status, "COMPLETED") {
			return false, false
		}
		switch strings.ToUpper(check.Conclusion) {
		case "SUCCESS", "NEUTRAL", "SKIPPED":
			return true, false
		case "ACTION_REQUIRED", "CANCELLED", "FAILURE", "STALE", "STARTUP_FAILURE", "TIMED_OUT":
			return true, true
		default:
			return false, false
		}
	case "StatusContext":
		switch strings.ToUpper(check.State) {
		case "SUCCESS":
			return true, false
		case "ERROR", "FAILURE":
			return true, true
		default:
			return false, false
		}
	default:
		return false, false
	}
}

func sessionPullRequestState(state string, draft bool) (kelos.SessionPullRequestState, error) {
	switch strings.ToUpper(state) {
	case "OPEN":
		if draft {
			return kelos.SessionPullRequestStateDraft, nil
		}
		return kelos.SessionPullRequestStateOpen, nil
	case "MERGED":
		return kelos.SessionPullRequestStateMerged, nil
	case "CLOSED":
		return kelos.SessionPullRequestStateClosed, nil
	default:
		return "", fmt.Errorf("unsupported pull request state %q", state)
	}
}

func environmentValue(environment []string, name string) string {
	prefix := name + "="
	for _, entry := range environment {
		if strings.HasPrefix(entry, prefix) {
			return strings.TrimPrefix(entry, prefix)
		}
	}
	return ""
}

func refreshWorkspaceStatus(ctx context.Context, stateDir, workingDir string, environment []string) (WorkspaceStatus, error) {
	return refreshWorkspaceStatusWithRunner(ctx, realWorkspaceStatusRunner{}, stateDir, workingDir, environment)
}

func refreshWorkspaceStatusWithRunner(ctx context.Context, runner workspaceStatusRunner, stateDir, workingDir string, environment []string) (WorkspaceStatus, error) {
	previous, err := readRecordedWorkspaceStatus(stateDir)
	if err != nil {
		return WorkspaceStatus{}, err
	}
	status, captureErr := captureWorkspaceStatus(ctx, runner, workingDir, environment)
	if captureErr != nil {
		if status.Branch == "" {
			return previous, captureErr
		}
		if status.Branch == previous.Branch {
			status.PullRequest = previous.PullRequest
		}
	}
	if err := writeWorkspaceStatus(stateDir, status); err != nil {
		return status, err
	}
	return status, captureErr
}

func writeWorkspaceStatus(stateDir string, status WorkspaceStatus) error {
	file, err := os.CreateTemp(stateDir, ".workspace-status-*")
	if err != nil {
		return fmt.Errorf("creating Session workspace status: %w", err)
	}
	path := file.Name()
	defer os.Remove(path)
	if err := file.Chmod(0600); err != nil {
		file.Close()
		return fmt.Errorf("securing Session workspace status: %w", err)
	}
	if err := json.NewEncoder(file).Encode(status); err != nil {
		file.Close()
		return fmt.Errorf("encoding Session workspace status: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("closing Session workspace status: %w", err)
	}
	if err := os.Rename(path, filepath.Join(stateDir, workspaceStatusFileName)); err != nil {
		return fmt.Errorf("recording Session workspace status: %w", err)
	}
	return nil
}

func readWorkspaceStatus(ctx context.Context, runner workspaceStatusRunner, stateDir, workingDir string) (WorkspaceStatus, error) {
	status, err := readRecordedWorkspaceStatus(stateDir)
	if err != nil {
		return WorkspaceStatus{}, err
	}

	isGitWorkspace, err := isGitWorkspace(ctx, runner, workingDir)
	if err != nil {
		return WorkspaceStatus{}, err
	}
	if !isGitWorkspace {
		return WorkspaceStatus{}, nil
	}
	branch, err := runner.run(ctx, workingDir, "git", "branch", "--show-current")
	if err != nil {
		return WorkspaceStatus{}, fmt.Errorf("reading Session git branch: %w", err)
	}
	if branch != status.Branch {
		status.Branch = branch
		status.PullRequest = nil
	}
	return status, nil
}

func isGitWorkspace(ctx context.Context, runner workspaceStatusRunner, workingDir string) (bool, error) {
	output, err := runner.run(ctx, workingDir, "git", "rev-parse", "--is-inside-work-tree")
	if err == nil {
		return output == "true", nil
	}
	var exitError *exec.ExitError
	if errors.As(err, &exitError) && bytes.Contains(exitError.Stderr, []byte("not a git repository")) {
		return false, nil
	}
	return false, fmt.Errorf("checking Session git workspace: %w", err)
}

func readRecordedWorkspaceStatus(stateDir string) (WorkspaceStatus, error) {
	status := WorkspaceStatus{}
	data, err := os.ReadFile(filepath.Join(stateDir, workspaceStatusFileName))
	if err == nil {
		if err := json.Unmarshal(data, &status); err != nil {
			return WorkspaceStatus{}, fmt.Errorf("decoding Session workspace status: %w", err)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return WorkspaceStatus{}, fmt.Errorf("reading Session workspace status: %w", err)
	}
	return status, nil
}
