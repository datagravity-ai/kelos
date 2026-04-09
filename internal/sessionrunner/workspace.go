/*
Copyright 2025 Kelos contributors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package sessionrunner

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

const workspaceRepoPath = "/workspace/repo"

// WorkspaceManager handles git workspace reset between tasks.
type WorkspaceManager struct {
	resetGit     bool
	preserveDirs []string
	baseBranch   string
}

// NewWorkspaceManager creates a new WorkspaceManager configured from environment variables.
func NewWorkspaceManager() *WorkspaceManager {
	wm := &WorkspaceManager{
		resetGit:   true,
		baseBranch: os.Getenv("KELOS_BASE_BRANCH"),
	}

	if v := os.Getenv("KELOS_WORKSPACE_RESET_GIT"); v == "false" {
		wm.resetGit = false
	}

	if v := os.Getenv("KELOS_WORKSPACE_RESET_PRESERVE_DIRS"); v != "" {
		var dirs []string
		if err := json.Unmarshal([]byte(v), &dirs); err == nil {
			wm.preserveDirs = dirs
		}
	}

	return wm
}

// Reset resets the workspace to a clean state, optionally checking out a branch.
func (wm *WorkspaceManager) Reset(ctx context.Context, branch string) error {
	if !wm.resetGit {
		// If git reset is disabled, just checkout the branch if specified.
		if branch != "" {
			return wm.checkoutBranch(ctx, branch)
		}
		return nil
	}

	// Fetch latest from origin.
	if err := wm.gitCmd(ctx, "fetch", "origin"); err != nil {
		fmt.Printf("Warning: git fetch failed: %v\n", err)
	}

	// Determine the base branch to reset to.
	baseBranch := wm.baseBranch
	if baseBranch == "" {
		baseBranch = "main"
	}

	// Checkout base branch and reset to origin.
	if err := wm.gitCmd(ctx, "checkout", baseBranch); err != nil {
		return fmt.Errorf("failed to checkout base branch %s: %w", baseBranch, err)
	}

	if err := wm.gitCmd(ctx, "reset", "--hard", "origin/"+baseBranch); err != nil {
		fmt.Printf("Warning: git reset to origin/%s failed: %v\n", baseBranch, err)
	}

	// Clean untracked files, preserving specified directories.
	cleanArgs := []string{"clean", "-fdx"}
	for _, dir := range wm.preserveDirs {
		cleanArgs = append(cleanArgs, "-e", dir)
	}
	if err := wm.gitCmd(ctx, cleanArgs...); err != nil {
		return fmt.Errorf("failed to clean workspace: %w", err)
	}

	// Checkout task branch if specified.
	if branch != "" {
		return wm.checkoutBranch(ctx, branch)
	}

	return nil
}

// checkoutBranch checks out or creates the specified branch.
func (wm *WorkspaceManager) checkoutBranch(ctx context.Context, branch string) error {
	// Try to fetch the branch from origin first.
	_ = wm.gitCmd(ctx, "fetch", "origin", branch+":"+branch)

	// Check if branch exists locally.
	err := wm.gitCmd(ctx, "rev-parse", "--verify", "refs/heads/"+branch)
	if err == nil {
		// Branch exists, check it out.
		return wm.gitCmd(ctx, "checkout", branch)
	}
	// Branch doesn't exist, create it.
	return wm.gitCmd(ctx, "checkout", "-b", branch)
}

// gitCmd runs a git command in the workspace directory.
func (wm *WorkspaceManager) gitCmd(ctx context.Context, args ...string) error {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = workspaceRepoPath
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	fmt.Printf("git %s\n", strings.Join(args, " "))
	return cmd.Run()
}
