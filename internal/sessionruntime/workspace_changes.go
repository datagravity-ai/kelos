package sessionruntime

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	workspaceChangesUncommitted = "uncommitted"
	workspaceChangesBranch      = "branch"
	maxWorkspaceDiffBytes       = 1024 * 1024
	maxWorkspaceChangeFiles     = 1000
	maxWorkspaceBranches        = 1000
	// JSON escaping can expand diff text sixfold; reserve room for the file and
	// branch lists and diagnostics below the console bridge's 8 MiB event limit.
	maxWorkspaceFileListBytes   = 512 * 1024
	maxWorkspaceBranchListBytes = 128 * 1024
	maxWorkspaceGitErrorBytes   = 4096
)

// WorkspaceChanges is a bounded Git comparison for the selected review scope.
type WorkspaceChanges struct {
	Scope             string                `json:"scope,omitempty"`
	BaseRef           string                `json:"baseRef,omitempty"`
	Base              string                `json:"base,omitempty"`
	Branches          []string              `json:"branches,omitempty"`
	BranchesTruncated bool                  `json:"branchesTruncated,omitempty"`
	Files             []WorkspaceFileChange `json:"files"`
	Message           string                `json:"message,omitempty"`
	Truncated         bool                  `json:"truncated,omitempty"`
}

// WorkspaceFileChange keeps the file list independent of diff content limits.
type WorkspaceFileChange struct {
	Name string `json:"name"`
	Diff string `json:"diff"`
}

type workspaceChangesCollector struct {
	mu          sync.Mutex
	workingDir  string
	stateDir    string
	environment []string
}

func newWorkspaceChangesCollector(workingDir, stateDir string, environment []string) (*workspaceChangesCollector, error) {
	if workingDir == "" || stateDir == "" {
		return nil, errors.New("workspace changes require working and state directories")
	}
	for _, dir := range []string{workingDir, stateDir} {
		info, err := os.Stat(dir)
		if err != nil {
			return nil, fmt.Errorf("checking workspace changes directory %q: %w", dir, err)
		}
		if !info.IsDir() {
			return nil, fmt.Errorf("workspace changes path %q is not a directory", dir)
		}
	}
	return &workspaceChangesCollector{workingDir: workingDir, stateDir: stateDir, environment: replaceProcessEnv(environment, "LC_ALL", "C")}, nil
}

func (c *workspaceChangesCollector) command(ctx context.Context, dir string, environment []string, args ...string) *exec.Cmd {
	command := exec.CommandContext(ctx, "git", args...)
	command.Dir = dir
	command.Env = environment
	return command
}

func (c *workspaceChangesCollector) output(ctx context.Context, dir string, environment []string, args ...string) ([]byte, error) {
	output, err := c.command(ctx, dir, environment, args...).Output()
	var exitError *exec.ExitError
	if errors.As(err, &exitError) {
		detail := strings.TrimSpace(string(exitError.Stderr))
		if len(detail) > maxWorkspaceGitErrorBytes {
			detail = detail[:maxWorkspaceGitErrorBytes] + "…"
		}
		if detail != "" {
			err = fmt.Errorf("%w: %s", err, detail)
		}
	}
	return output, err
}

func (c *workspaceChangesCollector) head(ctx context.Context) (string, string, error) {
	output, err := c.output(ctx, c.workingDir, c.environment, "rev-parse", "--show-toplevel")
	if err != nil {
		if strings.Contains(err.Error(), "not a git repository") {
			return "", "", nil
		}
		return "", "", fmt.Errorf("finding workspace repository: %w", err)
	}
	root := strings.TrimSpace(string(output))
	output, err = c.output(ctx, root, c.environment, "rev-parse", "--verify", "HEAD")
	if err != nil {
		if _, refErr := c.output(ctx, root, c.environment, "symbolic-ref", "HEAD"); refErr != nil {
			return "", "", fmt.Errorf("reading workspace HEAD: %w", err)
		}
		return root, "", nil
	}
	return root, strings.TrimSpace(string(output)), nil
}

func (c *workspaceChangesCollector) capture(ctx context.Context, scope, baseRef string) (WorkspaceChanges, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if scope == "" {
		scope = workspaceChangesUncommitted
	}
	changes := WorkspaceChanges{Scope: scope, Files: []WorkspaceFileChange{}}
	if scope != workspaceChangesUncommitted && scope != workspaceChangesBranch {
		return changes, fmt.Errorf("unknown workspace changes scope %q", scope)
	}
	root, head, err := c.head(ctx)
	if err != nil {
		return changes, err
	}
	if root == "" {
		changes.Message = "Changes are unavailable outside a Git workspace"
		return changes, nil
	}
	output, err := c.output(ctx, root, c.environment, "for-each-ref", "--format=%(refname)%09%(symref)", "refs/heads", "refs/remotes")
	if err != nil {
		return changes, fmt.Errorf("listing workspace branches: %w", err)
	}
	branchLimit, branchListBytes := maxWorkspaceBranches, 2
	reserveBase := scope == workspaceChangesBranch && baseRef != ""
	if reserveBase {
		encoded, _ := json.Marshal(baseRef)
		if len(encoded)+1 > maxWorkspaceBranchListBytes-branchListBytes {
			return changes, errors.New("base branch name exceeds the display limit")
		}
		branchListBytes += len(encoded) + 1
		branchLimit--
	}
	baseFound := false
	for _, line := range strings.Split(strings.TrimSuffix(string(output), "\n"), "\n") {
		ref, symbolic, _ := strings.Cut(line, "\t")
		if ref == "" || symbolic != "" {
			continue
		}
		if reserveBase && ref == baseRef {
			baseFound = true
			continue
		}
		encoded, _ := json.Marshal(ref)
		if len(changes.Branches) == branchLimit || branchListBytes+len(encoded)+1 > maxWorkspaceBranchListBytes {
			changes.BranchesTruncated = true
			continue
		}
		changes.Branches = append(changes.Branches, ref)
		branchListBytes += len(encoded) + 1
	}
	if baseFound {
		changes.Branches = append(changes.Branches, baseRef)
		slices.Sort(changes.Branches)
	}
	base := head
	if scope == workspaceChangesBranch {
		changes.BaseRef = baseRef
		if baseRef == "" {
			changes.Message = "Choose a base branch to compare committed changes"
			return changes, nil
		}
		if !baseFound {
			return changes, fmt.Errorf("base branch %q is unavailable", baseRef)
		}
		if head == "" {
			changes.Message = "No committed branch changes: HEAD has no commits"
			return changes, nil
		}
		output, err := c.output(ctx, root, c.environment, "merge-base", baseRef, head)
		if err != nil {
			return changes, fmt.Errorf("finding merge base for branch %q: %w", baseRef, err)
		}
		base = strings.TrimSpace(string(output))
	}
	if base == "" {
		output, err := c.output(ctx, root, c.environment, "hash-object", "-w", "-t", "tree", "--stdin")
		if err != nil {
			return changes, fmt.Errorf("creating empty workspace base: %w", err)
		}
		base = strings.TrimSpace(string(output))
	}
	changes.Base = base
	environment := c.environment
	if scope == workspaceChangesUncommitted {
		dir, err := os.MkdirTemp(c.stateDir, "changes-")
		if err != nil {
			return changes, err
		}
		defer os.RemoveAll(dir)
		index, err := filepath.Abs(filepath.Join(dir, "index"))
		if err != nil {
			return changes, err
		}
		environment = replaceProcessEnv(c.environment, "GIT_INDEX_FILE", index)
		indexPath, err := c.output(ctx, root, c.environment, "rev-parse", "--git-path", "index")
		if err != nil {
			return changes, err
		}
		path := strings.TrimSpace(string(indexPath))
		if !filepath.IsAbs(path) {
			path = filepath.Join(root, path)
		}
		if data, err := os.ReadFile(path); err == nil {
			if err := os.WriteFile(index, data, 0600); err != nil {
				return changes, err
			}
		} else if errors.Is(err, os.ErrNotExist) {
			if _, err := c.output(ctx, root, environment, "read-tree", "--empty"); err != nil {
				return changes, err
			}
		} else {
			return changes, err
		}
		output, err := c.output(ctx, root, environment, "ls-files", "--others", "--exclude-standard", "-z")
		if err != nil {
			return changes, fmt.Errorf("listing untracked workspace files: %w", err)
		}
		addArgs := []string{"add", "--intent-to-add", "--ignore-errors", "--", "."}
		// Unborn nested repositories cannot be represented by a Git diff.
		for _, name := range strings.Split(string(output), "\x00") {
			if !strings.HasSuffix(name, "/") {
				continue
			}
			if _, err := c.output(ctx, filepath.Join(root, name), c.environment, "rev-parse", "--verify", "HEAD"); err != nil {
				addArgs = append(addArgs, ":(exclude,literal)"+strings.TrimSuffix(name, "/"))
				changes.Message = fmt.Sprintf("Some workspace files were omitted: reading nested repository %q: %v", name, err)
				changes.Truncated = true
			}
		}
		// Intent-to-add includes untracked files without changing the user's index.
		if _, err := c.output(ctx, root, environment, addArgs...); err != nil {
			var exitError *exec.ExitError
			if !errors.As(err, &exitError) || exitError.ExitCode() != 1 {
				return changes, fmt.Errorf("collecting workspace files: %w", err)
			}
			changes.Message = "Some workspace files were omitted: " + err.Error()
			changes.Truncated = true
		}
	}
	args := []string{"diff", "--no-ext-diff", "--no-textconv", "--no-color", "--no-renames", "--src-prefix=a/", "--dst-prefix=b/", base}
	if scope == workspaceChangesBranch {
		args = append(args, head)
	}
	output, err = c.output(ctx, root, environment, append(args, "--name-only", "-z", "--")...)
	if err != nil {
		return changes, fmt.Errorf("listing workspace changes: %w", err)
	}
	if len(output) == 0 {
		return changes, nil
	}
	names := strings.Split(strings.TrimSuffix(string(output), "\x00"), "\x00")
	fileListBytes := 0
	for _, name := range names {
		file := WorkspaceFileChange{Name: name, Diff: "Diff omitted because the workspace diff exceeds the display limit"}
		encoded, _ := json.Marshal(file)
		if len(changes.Files) == maxWorkspaceChangeFiles || fileListBytes+len(encoded)+1 > maxWorkspaceFileListBytes {
			changes.Truncated = true
			message := fmt.Sprintf("Showing %d of %d changed files (file list truncated)", len(changes.Files), len(names))
			if changes.Message != "" {
				message += "; " + changes.Message
			}
			changes.Message = message
			break
		}
		fileListBytes += len(encoded) + 1
		changes.Files = append(changes.Files, file)
	}
	patch := &workspaceOutputBuffer{limit: maxWorkspaceDiffBytes}
	command := c.command(ctx, root, environment, append(args, "--patch", "--")...)
	command.Stdout = patch
	stderr := &workspaceOutputBuffer{limit: maxWorkspaceGitErrorBytes}
	command.Stderr = stderr
	if err := command.Run(); err != nil {
		return changes, fmt.Errorf("reading workspace diff: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	changes.Truncated = changes.Truncated || patch.truncated
	segments := strings.Split("\n"+patch.String(), "\ndiff --git ")
	patches := make(map[string]string)
	for i, segment := range segments[1:] {
		header, _, complete := strings.Cut(segment, "\n")
		if !complete {
			continue
		}
		// With renames disabled, both header paths have the same encoded length.
		path := header[(len(header)+1)/2:]
		if strings.HasPrefix(path, "\"") {
			path, err = strconv.Unquote(path)
			if err != nil {
				return changes, fmt.Errorf("decoding workspace diff path: %w", err)
			}
		}
		content := "diff --git " + segment
		if patch.truncated && i == len(segments)-2 {
			content += "\n[diff truncated]"
		}
		path = strings.TrimPrefix(path, "b/")
		if patches[path] != "" {
			patches[path] += "\n"
		}
		patches[path] += content
	}
	for i := range changes.Files {
		if content, ok := patches[changes.Files[i].Name]; ok {
			changes.Files[i].Diff = content
		} else if !patch.truncated {
			return changes, errors.New("workspace files changed during comparison; refresh to retry")
		}
	}
	return changes, nil
}

type workspaceOutputBuffer struct {
	buffer    bytes.Buffer
	limit     int
	truncated bool
}

func (b *workspaceOutputBuffer) String() string {
	return b.buffer.String()
}

func (b *workspaceOutputBuffer) Write(p []byte) (int, error) {
	n := len(p)
	remaining := b.limit - b.buffer.Len()
	if len(p) > remaining {
		b.truncated = true
		p = p[:remaining]
	}
	_, _ = b.buffer.Write(p)
	return n, nil
}
