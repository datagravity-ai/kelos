package sessionruntime

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"reflect"
	"strings"
	"testing"

	kelos "github.com/kelos-dev/kelos/api/v1alpha2"
)

type fakeWorkspaceStatusRunner struct {
	commands map[string]workspaceStatusResult
}

type workspaceStatusResult struct {
	output string
	err    error
}

func (r fakeWorkspaceStatusRunner) run(_ context.Context, workingDir, name string, args ...string) (string, error) {
	key := workingDir + ": " + name + " " + strings.Join(args, " ")
	result, ok := r.commands[key]
	if !ok {
		return "", fmt.Errorf("command not mocked: %s", key)
	}
	return result.output, result.err
}

func TestCaptureWorkspaceStatus(t *testing.T) {
	runner := fakeWorkspaceStatusRunner{commands: map[string]workspaceStatusResult{
		"/repo: git rev-parse --is-inside-work-tree":                                    {output: "true"},
		"/repo: git branch --show-current":                                              {output: "feature/status"},
		"/repo: git for-each-ref --format=%(push:remotename) refs/heads/feature/status": {output: ""},
		"/repo: git remote get-url --push origin":                                       {output: "https://github.com/kelos-dev/kelos.git"},
		"/repo: gh pr list --head feature/status --state all --json id,url,headRepositoryOwner,state,isDraft,statusCheckRollup --limit 100": {
			output: `[{"id":"PR_42","url":"https://github.com/kelos-dev/kelos/pull/42","state":"OPEN","isDraft":false,"headRepositoryOwner":{"login":"kelos-dev"},"statusCheckRollup":[{"__typename":"CheckRun","status":"COMPLETED","conclusion":"SUCCESS"},{"__typename":"CheckRun","status":"IN_PROGRESS","conclusion":""},{"__typename":"StatusContext","state":"SUCCESS"}]}]`,
		},
		"/repo: gh api graphql -F id=PR_42 -f query=" + pullRequestMergeQueueQuery: {output: `{"data":{"node":{"mergeQueueEntry":null}}}`},
	}}

	got, err := captureWorkspaceStatus(context.Background(), runner, "/repo", nil)
	if err != nil {
		t.Fatal(err)
	}
	want := WorkspaceStatus{
		Branch: "feature/status",
		PullRequest: &kelos.SessionPullRequest{
			URL:   "https://github.com/kelos-dev/kelos/pull/42",
			State: kelos.SessionPullRequestStateOpen,
			Checks: &kelos.SessionPullRequestChecks{
				State:     kelos.SessionPullRequestChecksStatePending,
				Completed: 2,
				Total:     3,
			},
		},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("captureWorkspaceStatus() = %#v, want %#v", got, want)
	}
}

func TestCaptureWorkspaceStatusUsesMergeQueueChecks(t *testing.T) {
	runner := fakeWorkspaceStatusRunner{commands: map[string]workspaceStatusResult{
		"/repo: git rev-parse --is-inside-work-tree":                                    {output: "true"},
		"/repo: git branch --show-current":                                              {output: "feature/status"},
		"/repo: git for-each-ref --format=%(push:remotename) refs/heads/feature/status": {output: ""},
		"/repo: git remote get-url --push origin":                                       {output: "https://github.com/kelos-dev/kelos.git"},
		"/repo: gh pr list --head feature/status --state all --json id,url,headRepositoryOwner,state,isDraft,statusCheckRollup --limit 100": {
			output: `[{"id":"PR_42","url":"https://github.com/kelos-dev/kelos/pull/42","state":"OPEN","isDraft":false,"headRepositoryOwner":{"login":"kelos-dev"},"statusCheckRollup":[{"__typename":"CheckRun","status":"COMPLETED","conclusion":"SUCCESS"}]}]`,
		},
		"/repo: gh api graphql -F id=PR_42 -f query=" + pullRequestMergeQueueQuery: {
			output: `{"data":{"node":{"mergeQueueEntry":{"headCommit":{"statusCheckRollup":{"contexts":{"nodes":[{"__typename":"CheckRun","status":"COMPLETED","conclusion":"SUCCESS"},{"__typename":"CheckRun","status":"IN_PROGRESS","conclusion":""}]}}}}}}}`,
		},
	}}

	got, err := captureWorkspaceStatus(context.Background(), runner, "/repo", nil)
	if err != nil {
		t.Fatal(err)
	}
	want := WorkspaceStatus{
		Branch: "feature/status",
		PullRequest: &kelos.SessionPullRequest{
			URL:   "https://github.com/kelos-dev/kelos/pull/42",
			State: kelos.SessionPullRequestStateQueued,
			Checks: &kelos.SessionPullRequestChecks{
				State:     kelos.SessionPullRequestChecksStatePending,
				Completed: 1,
				Total:     2,
			},
		},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("captureWorkspaceStatus() = %#v, want %#v", got, want)
	}
}

func TestCaptureWorkspaceStatusUsesPullRequestChecksOnMergeQueueLookupError(t *testing.T) {
	runner := fakeWorkspaceStatusRunner{commands: map[string]workspaceStatusResult{
		"/repo: git rev-parse --is-inside-work-tree":                                    {output: "true"},
		"/repo: git branch --show-current":                                              {output: "feature/status"},
		"/repo: git for-each-ref --format=%(push:remotename) refs/heads/feature/status": {output: ""},
		"/repo: git remote get-url --push origin":                                       {output: "https://github.com/kelos-dev/kelos.git"},
		"/repo: gh pr list --head feature/status --state all --json id,url,headRepositoryOwner,state,isDraft,statusCheckRollup --limit 100": {
			output: `[{"id":"PR_42","url":"https://github.com/kelos-dev/kelos/pull/42","state":"OPEN","isDraft":false,"headRepositoryOwner":{"login":"kelos-dev"},"statusCheckRollup":[{"__typename":"CheckRun","status":"COMPLETED","conclusion":"SUCCESS"}]}]`,
		},
		"/repo: gh api graphql -F id=PR_42 -f query=" + pullRequestMergeQueueQuery: {err: errors.New("merge queue unavailable")},
	}}

	got, err := captureWorkspaceStatus(context.Background(), runner, "/repo", nil)
	if err != nil {
		t.Fatal(err)
	}
	want := WorkspaceStatus{
		Branch: "feature/status",
		PullRequest: &kelos.SessionPullRequest{
			URL:   "https://github.com/kelos-dev/kelos/pull/42",
			State: kelos.SessionPullRequestStateOpen,
			Checks: &kelos.SessionPullRequestChecks{
				State:     kelos.SessionPullRequestChecksStateSuccess,
				Completed: 1,
				Total:     1,
			},
		},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("captureWorkspaceStatus() = %#v, want %#v", got, want)
	}
}

func TestCaptureWorkspaceStatusUsesBranchPushRemote(t *testing.T) {
	runner := fakeWorkspaceStatusRunner{commands: map[string]workspaceStatusResult{
		"/repo: git rev-parse --is-inside-work-tree":                                    {output: "true"},
		"/repo: git branch --show-current":                                              {output: "feature/status"},
		"/repo: git for-each-ref --format=%(push:remotename) refs/heads/feature/status": {output: "fork"},
		"/repo: git remote get-url --push fork":                                         {output: "git@github.com:gjkim/kelos.git"},
		"/repo: gh pr list --head feature/status --state all --json id,url,headRepositoryOwner,state,isDraft,statusCheckRollup --limit 100": {
			output: `[{"id":"PR_42","url":"https://github.com/kelos-dev/kelos/pull/42","state":"OPEN","isDraft":false,"headRepositoryOwner":{"login":"gjkim"}}]`,
		},
		"/repo: gh api graphql -F id=PR_42 -f query=" + pullRequestMergeQueueQuery: {output: `{"data":{"node":{"mergeQueueEntry":null}}}`},
	}}

	got, err := captureWorkspaceStatus(context.Background(), runner, "/repo", nil)
	if err != nil {
		t.Fatal(err)
	}
	want := WorkspaceStatus{
		Branch: "feature/status",
		PullRequest: &kelos.SessionPullRequest{
			URL:   "https://github.com/kelos-dev/kelos/pull/42",
			State: kelos.SessionPullRequestStateOpen,
		},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("captureWorkspaceStatus() = %#v, want %#v", got, want)
	}
}

func TestCaptureWorkspaceStatusWithoutGitRepository(t *testing.T) {
	runner := fakeWorkspaceStatusRunner{commands: map[string]workspaceStatusResult{
		"/repo: git rev-parse --is-inside-work-tree": {
			err: &exec.ExitError{Stderr: []byte("fatal: not a git repository")},
		},
	}}

	got, err := captureWorkspaceStatus(context.Background(), runner, "/repo", nil)
	if err != nil {
		t.Fatal(err)
	}
	if want := (WorkspaceStatus{}); !reflect.DeepEqual(got, want) {
		t.Fatalf("captureWorkspaceStatus() = %#v, want %#v", got, want)
	}
}

func TestCaptureWorkspaceStatusUsesUpstreamRepository(t *testing.T) {
	runner := fakeWorkspaceStatusRunner{commands: map[string]workspaceStatusResult{
		"/repo: git rev-parse --is-inside-work-tree":                                    {output: "true"},
		"/repo: git branch --show-current":                                              {output: "feature/status"},
		"/repo: git for-each-ref --format=%(push:remotename) refs/heads/feature/status": {output: "origin"},
		"/repo: git remote get-url --push origin":                                       {output: "git@github.com:gjkim/kelos.git"},
		"/repo: gh pr list --head feature/status --state all --json id,url,headRepositoryOwner,state,isDraft,statusCheckRollup --limit 100": {
			output: "[]",
		},
		"/repo: gh pr list --head feature/status --state all --json id,url,headRepositoryOwner,state,isDraft,statusCheckRollup --limit 100 --repo kelos-dev/kelos": {
			output: `[{"id":"PR_41","url":"https://github.com/kelos-dev/kelos/pull/41","state":"OPEN","isDraft":false,"headRepositoryOwner":{"login":"another-fork"}},{"id":"PR_42","url":"https://github.com/kelos-dev/kelos/pull/42","state":"OPEN","isDraft":true,"headRepositoryOwner":{"login":"gjkim"}}]`,
		},
		"/repo: gh api graphql -F id=PR_42 -f query=" + pullRequestMergeQueueQuery: {output: `{"data":{"node":{"mergeQueueEntry":null}}}`},
	}}

	got, err := captureWorkspaceStatus(context.Background(), runner, "/repo", []string{"KELOS_UPSTREAM_REPO=kelos-dev/kelos"})
	if err != nil {
		t.Fatal(err)
	}
	want := WorkspaceStatus{
		Branch: "feature/status",
		PullRequest: &kelos.SessionPullRequest{
			URL:   "https://github.com/kelos-dev/kelos/pull/42",
			State: kelos.SessionPullRequestStateDraft,
		},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("captureWorkspaceStatus() = %#v, want %#v", got, want)
	}
}

func TestRefreshWorkspaceStatusPreservesPullRequestOnLookupError(t *testing.T) {
	stateDir := t.TempDir()
	want := WorkspaceStatus{
		Branch: "feature/status",
		PullRequest: &kelos.SessionPullRequest{
			URL:   "https://github.com/kelos-dev/kelos/pull/42",
			State: kelos.SessionPullRequestStateOpen,
		},
	}
	if err := writeWorkspaceStatus(stateDir, want); err != nil {
		t.Fatal(err)
	}
	runner := fakeWorkspaceStatusRunner{commands: map[string]workspaceStatusResult{
		"/repo: git rev-parse --is-inside-work-tree":                                    {output: "true"},
		"/repo: git branch --show-current":                                              {output: "feature/status"},
		"/repo: git for-each-ref --format=%(push:remotename) refs/heads/feature/status": {output: "origin"},
		"/repo: git remote get-url --push origin":                                       {err: errors.New("temporary git failure")},
	}}

	_, err := refreshWorkspaceStatusWithRunner(context.Background(), runner, stateDir, "/repo", nil)
	if err == nil {
		t.Fatal("refreshWorkspaceStatusWithRunner() error = nil, want GitHub failure")
	}
	got, err := readRecordedWorkspaceStatus(stateDir)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("recorded workspace status = %#v, want %#v", got, want)
	}
}

func TestRefreshWorkspaceStatusReturnsRecordedStatusOnGitError(t *testing.T) {
	stateDir := t.TempDir()
	want := WorkspaceStatus{
		Branch: "feature/status",
		PullRequest: &kelos.SessionPullRequest{
			URL:   "https://github.com/kelos-dev/kelos/pull/42",
			State: kelos.SessionPullRequestStateMerged,
		},
	}
	if err := writeWorkspaceStatus(stateDir, want); err != nil {
		t.Fatal(err)
	}
	runner := fakeWorkspaceStatusRunner{commands: map[string]workspaceStatusResult{
		"/repo: git rev-parse --is-inside-work-tree": {err: errors.New("temporary git failure")},
	}}

	got, err := refreshWorkspaceStatusWithRunner(context.Background(), runner, stateDir, "/repo", nil)
	if err == nil {
		t.Fatal("refreshWorkspaceStatusWithRunner() error = nil, want Git failure")
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("refreshWorkspaceStatusWithRunner() = %#v, want %#v", got, want)
	}
	recorded, err := readRecordedWorkspaceStatus(stateDir)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(recorded, want) {
		t.Fatalf("recorded workspace status = %#v, want %#v", recorded, want)
	}
}

func TestRefreshWorkspaceStatusClearsPullRequestAfterSuccessfulLookup(t *testing.T) {
	stateDir := t.TempDir()
	if err := writeWorkspaceStatus(stateDir, WorkspaceStatus{
		Branch: "feature/status",
		PullRequest: &kelos.SessionPullRequest{
			URL:   "https://github.com/kelos-dev/kelos/pull/42",
			State: kelos.SessionPullRequestStateClosed,
		},
	}); err != nil {
		t.Fatal(err)
	}
	runner := fakeWorkspaceStatusRunner{commands: map[string]workspaceStatusResult{
		"/repo: git rev-parse --is-inside-work-tree":                                    {output: "true"},
		"/repo: git branch --show-current":                                              {output: "feature/status"},
		"/repo: git for-each-ref --format=%(push:remotename) refs/heads/feature/status": {output: "origin"},
		"/repo: git remote get-url --push origin":                                       {output: "https://github.com/kelos-dev/kelos.git"},
		"/repo: gh pr list --head feature/status --state all --json id,url,headRepositoryOwner,state,isDraft,statusCheckRollup --limit 100": {
			output: "[]",
		},
	}}

	if _, err := refreshWorkspaceStatusWithRunner(context.Background(), runner, stateDir, "/repo", nil); err != nil {
		t.Fatal(err)
	}
	got, err := readRecordedWorkspaceStatus(stateDir)
	if err != nil {
		t.Fatal(err)
	}
	want := WorkspaceStatus{Branch: "feature/status"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("recorded workspace status = %#v, want %#v", got, want)
	}
}

func TestSessionPullRequestState(t *testing.T) {
	tests := []struct {
		name  string
		state string
		draft bool
		want  kelos.SessionPullRequestState
	}{
		{name: "draft", state: "OPEN", draft: true, want: kelos.SessionPullRequestStateDraft},
		{name: "open", state: "OPEN", want: kelos.SessionPullRequestStateOpen},
		{name: "merged", state: "MERGED", want: kelos.SessionPullRequestStateMerged},
		{name: "closed", state: "CLOSED", draft: true, want: kelos.SessionPullRequestStateClosed},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := sessionPullRequestState(tt.state, tt.draft)
			if err != nil {
				t.Fatal(err)
			}
			if got != tt.want {
				t.Fatalf("sessionPullRequestState() = %q, want %q", got, tt.want)
			}
		})
	}
	if _, err := sessionPullRequestState("UNKNOWN", false); err == nil {
		t.Fatal("sessionPullRequestState() error = nil, want unsupported state error")
	}
}

func TestSessionPullRequestChecks(t *testing.T) {
	tests := []struct {
		name   string
		checks []githubStatusCheck
		want   *kelos.SessionPullRequestChecks
	}{
		{name: "none"},
		{
			name: "successful",
			checks: []githubStatusCheck{
				{TypeName: "CheckRun", Status: "COMPLETED", Conclusion: "SUCCESS"},
				{TypeName: "CheckRun", Status: "COMPLETED", Conclusion: "SKIPPED"},
				{TypeName: "StatusContext", State: "SUCCESS"},
			},
			want: &kelos.SessionPullRequestChecks{State: kelos.SessionPullRequestChecksStateSuccess, Completed: 3, Total: 3},
		},
		{
			name: "pending",
			checks: []githubStatusCheck{
				{TypeName: "CheckRun", Status: "COMPLETED", Conclusion: "NEUTRAL"},
				{TypeName: "CheckRun", Status: "IN_PROGRESS"},
				{TypeName: "StatusContext", State: "EXPECTED"},
			},
			want: &kelos.SessionPullRequestChecks{State: kelos.SessionPullRequestChecksStatePending, Completed: 1, Total: 3},
		},
		{
			name: "failed while pending",
			checks: []githubStatusCheck{
				{TypeName: "CheckRun", Status: "COMPLETED", Conclusion: "FAILURE"},
				{TypeName: "CheckRun", Status: "QUEUED"},
			},
			want: &kelos.SessionPullRequestChecks{State: kelos.SessionPullRequestChecksStateFailure, Completed: 1, Total: 2},
		},
		{
			name:   "cancelled",
			checks: []githubStatusCheck{{TypeName: "CheckRun", Status: "COMPLETED", Conclusion: "CANCELLED"}},
			want:   &kelos.SessionPullRequestChecks{State: kelos.SessionPullRequestChecksStateFailure, Completed: 1, Total: 1},
		},
		{
			name:   "unknown remains pending",
			checks: []githubStatusCheck{{TypeName: "FutureCheck", Status: "COMPLETED", Conclusion: "SUCCESS"}},
			want:   &kelos.SessionPullRequestChecks{State: kelos.SessionPullRequestChecksStatePending, Total: 1},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := sessionPullRequestChecks(tt.checks)
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("sessionPullRequestChecks() = %#v, want %#v", got, tt.want)
			}
		})
	}
}

func TestRepositoryOwnerFromRemoteURL(t *testing.T) {
	tests := []struct {
		name      string
		remoteURL string
		want      string
		wantError bool
	}{
		{name: "HTTPS", remoteURL: "https://github.com/kelos-dev/kelos.git", want: "kelos-dev"},
		{name: "SSH", remoteURL: "git@github.com:gjkim/kelos.git", want: "gjkim"},
		{name: "SSH URL", remoteURL: "ssh://git@github.example.com/team/kelos.git", want: "team"},
		{name: "invalid", remoteURL: "kelos", wantError: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := repositoryOwnerFromRemoteURL(tt.remoteURL)
			if (err != nil) != tt.wantError {
				t.Fatalf("repositoryOwnerFromRemoteURL() error = %v, wantError %t", err, tt.wantError)
			}
			if got != tt.want {
				t.Fatalf("repositoryOwnerFromRemoteURL() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestReadWorkspaceStatusClearsPullRequestWhenBranchChanges(t *testing.T) {
	stateDir := t.TempDir()
	if err := writeWorkspaceStatus(stateDir, WorkspaceStatus{
		Branch: "feature/old",
		PullRequest: &kelos.SessionPullRequest{
			URL:   "https://github.com/kelos-dev/kelos/pull/41",
			State: kelos.SessionPullRequestStateOpen,
		},
	}); err != nil {
		t.Fatal(err)
	}
	runner := fakeWorkspaceStatusRunner{commands: map[string]workspaceStatusResult{
		"/repo: git rev-parse --is-inside-work-tree": {output: "true"},
		"/repo: git branch --show-current":           {output: "feature/current"},
	}}

	got, err := readWorkspaceStatus(context.Background(), runner, stateDir, "/repo")
	if err != nil {
		t.Fatal(err)
	}
	want := WorkspaceStatus{Branch: "feature/current"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("readWorkspaceStatus() = %#v, want %#v", got, want)
	}
}

func TestReadWorkspaceStatusWithoutGitRepository(t *testing.T) {
	stateDir := t.TempDir()
	if err := writeWorkspaceStatus(stateDir, WorkspaceStatus{
		Branch: "feature/old",
		PullRequest: &kelos.SessionPullRequest{
			URL:   "https://github.com/kelos-dev/kelos/pull/41",
			State: kelos.SessionPullRequestStateOpen,
		},
	}); err != nil {
		t.Fatal(err)
	}
	runner := fakeWorkspaceStatusRunner{commands: map[string]workspaceStatusResult{
		"/repo: git rev-parse --is-inside-work-tree": {
			err: &exec.ExitError{Stderr: []byte("fatal: not a git repository")},
		},
	}}

	got, err := readWorkspaceStatus(context.Background(), runner, stateDir, "/repo")
	if err != nil {
		t.Fatal(err)
	}
	if want := (WorkspaceStatus{}); !reflect.DeepEqual(got, want) {
		t.Fatalf("readWorkspaceStatus() = %#v, want %#v", got, want)
	}
}
