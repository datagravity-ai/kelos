package sessionrunner

import (
	"os"
	"testing"
)

func TestNewWorkspaceManager_Defaults(t *testing.T) {
	os.Unsetenv("KELOS_BASE_BRANCH")
	os.Unsetenv("KELOS_WORKSPACE_RESET_GIT")
	os.Unsetenv("KELOS_WORKSPACE_RESET_PRESERVE_DIRS")

	wm := NewWorkspaceManager()

	if !wm.resetGit {
		t.Error("resetGit: expected true by default")
	}
	if wm.baseBranch != "" {
		t.Errorf("baseBranch: expected empty, got %q", wm.baseBranch)
	}
	if len(wm.preserveDirs) != 0 {
		t.Errorf("preserveDirs: expected empty, got %v", wm.preserveDirs)
	}
}

func TestNewWorkspaceManager_GitResetDisabled(t *testing.T) {
	t.Setenv("KELOS_WORKSPACE_RESET_GIT", "false")

	wm := NewWorkspaceManager()

	if wm.resetGit {
		t.Error("resetGit: expected false when KELOS_WORKSPACE_RESET_GIT=false")
	}
}

func TestNewWorkspaceManager_BaseBranch(t *testing.T) {
	t.Setenv("KELOS_BASE_BRANCH", "develop")

	wm := NewWorkspaceManager()

	if wm.baseBranch != "develop" {
		t.Errorf("baseBranch: expected 'develop', got %q", wm.baseBranch)
	}
}

func TestNewWorkspaceManager_PreserveDirs(t *testing.T) {
	t.Setenv("KELOS_WORKSPACE_RESET_PRESERVE_DIRS", `["node_modules",".venv"]`)

	wm := NewWorkspaceManager()

	if len(wm.preserveDirs) != 2 {
		t.Fatalf("preserveDirs: expected 2 entries, got %d", len(wm.preserveDirs))
	}
	if wm.preserveDirs[0] != "node_modules" {
		t.Errorf("preserveDirs[0]: expected 'node_modules', got %q", wm.preserveDirs[0])
	}
	if wm.preserveDirs[1] != ".venv" {
		t.Errorf("preserveDirs[1]: expected '.venv', got %q", wm.preserveDirs[1])
	}
}

func TestNewWorkspaceManager_InvalidPreserveDirsJSON(t *testing.T) {
	t.Setenv("KELOS_WORKSPACE_RESET_PRESERVE_DIRS", "not-json")

	wm := NewWorkspaceManager()

	if len(wm.preserveDirs) != 0 {
		t.Errorf("preserveDirs: expected empty on invalid JSON, got %v", wm.preserveDirs)
	}
}
