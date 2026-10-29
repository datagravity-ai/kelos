package sessionruntime

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"
)

func changesGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	command := exec.Command("git", args...)
	command.Dir = dir
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, output)
	}
	return strings.TrimSpace(string(output))
}

func changesWrite(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
}

func changesRepository(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	changesGit(t, dir, "init", "-q")
	changesGit(t, dir, "config", "user.name", "Changes test")
	changesGit(t, dir, "config", "user.email", "changes@example.invalid")
	changesWrite(t, dir, "tracked.txt", "original\n")
	changesWrite(t, dir, "deleted.txt", "delete me\n")
	changesWrite(t, dir, ".gitignore", "ignored*\n")
	changesGit(t, dir, "add", ".")
	changesGit(t, dir, "commit", "-qm", "Baseline")
	return dir
}

func changesCollector(t *testing.T, dir, stateDir string) *workspaceChangesCollector {
	t.Helper()
	c, err := newWorkspaceChangesCollector(dir, stateDir, os.Environ())
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func captureChanges(t *testing.T, c *workspaceChangesCollector) WorkspaceChanges {
	t.Helper()
	changes, err := c.capture(t.Context(), "", "")
	if err != nil {
		t.Fatal(err)
	}
	return changes
}

func TestWorkspaceChangesIncludesAllFilesWithoutStaging(t *testing.T) {
	dir := changesRepository(t)
	c := changesCollector(t, dir, t.TempDir())
	changesWrite(t, dir, "tracked.txt", "staged version\n")
	changesWrite(t, dir, "staged.txt", "staged addition\n")
	changesWrite(t, dir, "ignored-staged.txt", "tracked despite ignore\n")
	changesGit(t, dir, "add", "-f", "tracked.txt", "staged.txt", "ignored-staged.txt")
	changesWrite(t, dir, "tracked.txt", "working version\n")
	changesWrite(t, dir, "ignored.txt", "invisible\n")
	if err := os.Remove(filepath.Join(dir, "deleted.txt")); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"untracked.txt", "space b/name.txt"} {
		if strings.Contains(name, "/") {
			if err := os.MkdirAll(filepath.Dir(filepath.Join(dir, name)), 0700); err != nil {
				t.Fatal(err)
			}
		}
		changesWrite(t, dir, name, "new content\n")
	}
	for _, name := range []string{"empty.txt", "한글.txt", "tab\tquote\".txt", "line\nbreak.txt", "-option.txt"} {
		changesWrite(t, dir, name, "")
	}
	changesWrite(t, dir, "binary.bin", "\x00\x01\x02")
	if err := os.Symlink("untracked.txt", filepath.Join(dir, "link.txt")); err != nil {
		t.Fatal(err)
	}
	index, err := os.ReadFile(filepath.Join(dir, ".git", "index"))
	if err != nil {
		t.Fatal(err)
	}
	changes := captureChanges(t, c)
	want := []string{"tracked.txt", "deleted.txt", "staged.txt", "ignored-staged.txt", "untracked.txt", "space b/name.txt", "empty.txt", "한글.txt", "tab\tquote\".txt", "line\nbreak.txt", "-option.txt", "binary.bin", "link.txt"}
	var names []string
	for _, file := range changes.Files {
		names = append(names, file.Name)
		if !strings.HasPrefix(file.Diff, "diff --git ") {
			t.Errorf("file %q has no diff: %q", file.Name, file.Diff)
		}
		if file.Name == "tracked.txt" && (!strings.Contains(file.Diff, "+working version") || strings.Contains(file.Diff, "staged version")) {
			t.Errorf("tracked diff = %q", file.Diff)
		}
	}
	sort.Strings(want)
	sort.Strings(names)
	if !reflect.DeepEqual(names, want) {
		t.Fatalf("files = %q, want %q", names, want)
	}
	after, err := os.ReadFile(filepath.Join(dir, ".git", "index"))
	if err != nil || !bytes.Equal(index, after) {
		t.Fatalf("real index changed: %v", err)
	}
}

func TestWorkspaceChangesFollowsHEADAcrossCommitsAndRestarts(t *testing.T) {
	dir, stateDir := changesRepository(t), t.TempDir()
	c := changesCollector(t, dir, stateDir)
	captureChanges(t, c)
	changesWrite(t, dir, "created.txt", "new file\n")
	if changes := captureChanges(t, c); len(changes.Files) != 1 || changes.Files[0].Name != "created.txt" {
		t.Fatalf("uncommitted changes = %#v", changes)
	}
	changesGit(t, dir, "add", ".")
	changesGit(t, dir, "commit", "-qm", "Add file")
	head := changesGit(t, dir, "rev-parse", "HEAD")
	if changes := captureChanges(t, c); changes.Base != head || len(changes.Files) != 0 {
		t.Fatalf("committed changes = %#v, want clean workspace at %s", changes, head)
	}
	changesWrite(t, dir, "created.txt", "working file\n")
	for range 2 {
		changes := captureChanges(t, c)
		if changes.Base != head || len(changes.Files) != 1 || changes.Files[0].Name != "created.txt" ||
			!strings.Contains(changes.Files[0].Diff, "-new file\n+working file") {
			t.Fatalf("uncommitted changes = %#v, want comparison with %s", changes, head)
		}
		c = changesCollector(t, dir, stateDir)
	}
}

func TestWorkspaceChangesKeepsUncommittedEditsAfterCommit(t *testing.T) {
	dir := changesRepository(t)
	c := changesCollector(t, dir, t.TempDir())
	changesWrite(t, dir, "tracked.txt", "staged version\n")
	changesGit(t, dir, "add", "tracked.txt")
	changesWrite(t, dir, "tracked.txt", "working version\n")
	changesWrite(t, dir, "untracked.txt", "untracked addition\n")
	changesGit(t, dir, "commit", "-qm", "Update tracked file")
	changes := captureChanges(t, c)
	if changes.Base != changesGit(t, dir, "rev-parse", "HEAD") || len(changes.Files) != 2 ||
		changes.Files[0].Name != "tracked.txt" || changes.Files[1].Name != "untracked.txt" ||
		!strings.Contains(changes.Files[0].Diff, "-staged version\n+working version") ||
		!strings.Contains(changes.Files[1].Diff, "+untracked addition") {
		t.Fatalf("uncommitted changes = %#v", changes)
	}
}

func TestWorkspaceChangesUnbornRepository(t *testing.T) {
	dir := t.TempDir()
	changesGit(t, dir, "init", "-q")
	c := changesCollector(t, dir, t.TempDir())
	changesWrite(t, dir, "first.txt", "first\n")
	changes := captureChanges(t, c)
	if changes.Base == "" || len(changes.Files) != 1 || changes.Files[0].Name != "first.txt" {
		t.Fatalf("unborn changes = %#v", changes)
	}
	changesGit(t, dir, "add", ".")
	changesGit(t, dir, "-c", "user.name=Changes test", "-c", "user.email=changes@example.invalid", "commit", "-qm", "First commit")
	head := changesGit(t, dir, "rev-parse", "HEAD")
	if changes := captureChanges(t, c); changes.Base != head || len(changes.Files) != 0 {
		t.Fatalf("committed changes = %#v, want clean workspace at %s", changes, head)
	}
}

func TestWorkspaceBranchChangesUseMergeBase(t *testing.T) {
	dir := changesRepository(t)
	changesGit(t, dir, "branch", "-M", "review-base")
	base := changesGit(t, dir, "rev-parse", "HEAD")
	changesGit(t, dir, "checkout", "-qb", "feature")
	changesWrite(t, dir, "committed.txt", "committed addition\n")
	changesWrite(t, dir, "tracked.txt", "committed version\n")
	changesGit(t, dir, "add", ".")
	changesGit(t, dir, "commit", "-qm", "Feature work")
	changesGit(t, dir, "checkout", "-q", "review-base")
	changesWrite(t, dir, "base-only.txt", "base branch work\n")
	changesGit(t, dir, "add", ".")
	changesGit(t, dir, "commit", "-qm", "Advance base branch")
	changesGit(t, dir, "update-ref", "refs/remotes/origin/review-base", "HEAD")
	changesGit(t, dir, "symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/review-base")
	changesGit(t, dir, "checkout", "-q", "feature")
	changesWrite(t, dir, "tracked.txt", "staged version\n")
	changesGit(t, dir, "add", "tracked.txt")
	changesWrite(t, dir, "tracked.txt", "working version\n")
	changesWrite(t, dir, "untracked.txt", "untracked addition\n")
	index, err := os.ReadFile(filepath.Join(dir, ".git", "index"))
	if err != nil {
		t.Fatal(err)
	}
	c := changesCollector(t, dir, t.TempDir())
	for _, baseRef := range []string{"refs/heads/review-base", "refs/remotes/origin/review-base"} {
		t.Run(baseRef, func(t *testing.T) {
			changes, err := c.capture(t.Context(), workspaceChangesBranch, baseRef)
			if err != nil {
				t.Fatal(err)
			}
			if changes.Scope != workspaceChangesBranch || changes.BaseRef != baseRef || changes.Base != base || len(changes.Files) != 2 ||
				changes.Files[0].Name != "committed.txt" || changes.Files[1].Name != "tracked.txt" ||
				!strings.Contains(changes.Files[0].Diff, "+committed addition") ||
				!strings.Contains(changes.Files[1].Diff, "-original\n+committed version") {
				t.Fatalf("branch changes = %#v", changes)
			}
			wantBranches := []string{"refs/heads/feature", "refs/heads/review-base", "refs/remotes/origin/review-base"}
			if !reflect.DeepEqual(changes.Branches, wantBranches) {
				t.Fatalf("branches = %q, want %q", changes.Branches, wantBranches)
			}
		})
	}
	after, err := os.ReadFile(filepath.Join(dir, ".git", "index"))
	if err != nil || !bytes.Equal(index, after) {
		t.Fatalf("real index changed: %v", err)
	}
	changesGit(t, dir, "add", ".")
	changesGit(t, dir, "commit", "-qm", "More feature work")
	changesGit(t, dir, "checkout", "--detach", "-q")
	changes, err := c.capture(t.Context(), workspaceChangesBranch, "refs/heads/review-base")
	if err != nil || changes.Base != base || len(changes.Files) != 3 {
		t.Fatalf("branch changes after commit in detached HEAD = %#v, error = %v", changes, err)
	}
}

func TestWorkspaceBranchChangesRequireBaseBranch(t *testing.T) {
	dir := changesRepository(t)
	c := changesCollector(t, dir, t.TempDir())
	changes, err := c.capture(t.Context(), workspaceChangesBranch, "")
	if err != nil || changes.Message != "Choose a base branch to compare committed changes" || len(changes.Files) != 0 || len(changes.Branches) != 1 {
		t.Fatalf("missing base branch = %#v, error = %v", changes, err)
	}
	changes, err = c.capture(t.Context(), workspaceChangesBranch, "refs/heads/missing")
	if err == nil || !strings.Contains(err.Error(), `base branch "refs/heads/missing" is unavailable`) || len(changes.Branches) != 1 {
		t.Fatalf("unavailable base branch = %#v, error = %v", changes, err)
	}
	if _, err := c.capture(t.Context(), "unknown", ""); err == nil || !strings.Contains(err.Error(), "unknown workspace changes scope") {
		t.Fatalf("unknown scope error = %v", err)
	}
}

func TestWorkspaceBranchChangesRequireSharedHistory(t *testing.T) {
	dir := changesRepository(t)
	changesGit(t, dir, "branch", "review-base")
	changesGit(t, dir, "checkout", "--orphan", "unrelated")
	changesGit(t, dir, "commit", "-qm", "Unrelated root")
	c := changesCollector(t, dir, t.TempDir())
	if _, err := c.capture(t.Context(), workspaceChangesBranch, "refs/heads/review-base"); err == nil || !strings.Contains(err.Error(), "finding merge base") {
		t.Fatalf("unrelated base error = %v", err)
	}
}

func TestWorkspaceBranchChangesUnbornHEAD(t *testing.T) {
	dir := changesRepository(t)
	changesGit(t, dir, "branch", "-M", "review-base")
	changesGit(t, dir, "checkout", "--orphan", "feature")
	c := changesCollector(t, dir, t.TempDir())
	changes, err := c.capture(t.Context(), workspaceChangesBranch, "refs/heads/review-base")
	if err != nil || changes.Scope != workspaceChangesBranch || changes.BaseRef != "refs/heads/review-base" ||
		changes.Base != "" || len(changes.Files) != 0 || changes.Message != "No committed branch changes: HEAD has no commits" {
		t.Fatalf("branch changes without a HEAD commit = %#v, error = %v", changes, err)
	}
	uncommitted := captureChanges(t, c)
	if uncommitted.Base == "" || len(uncommitted.Files) != 3 || uncommitted.Message != "" {
		t.Fatalf("uncommitted changes without a HEAD commit = %#v", uncommitted)
	}
}

func TestWorkspaceChangesBoundsBranchList(t *testing.T) {
	for _, longNames := range []bool{false, true} {
		t.Run(fmt.Sprintf("longNames=%t", longNames), func(t *testing.T) {
			dir := changesRepository(t)
			changesGit(t, dir, "branch", "-M", "feature")
			head := changesGit(t, dir, "rev-parse", "HEAD")
			baseRef := "refs/heads/zz-selected"
			var updates strings.Builder
			fmt.Fprintf(&updates, "update %s %s\n", baseRef, head)
			for i := range maxWorkspaceBranches + 2 {
				prefix := "refs/heads/"
				if longNames {
					prefix += strings.Repeat(strings.Repeat("&", 100)+"/", 4)
				}
				fmt.Fprintf(&updates, "update %sbranch-%04d %s\n", prefix, i, head)
			}
			command := exec.Command("git", "update-ref", "--stdin")
			command.Dir = dir
			command.Stdin = strings.NewReader(updates.String())
			if output, err := command.CombinedOutput(); err != nil {
				t.Fatalf("creating branch refs: %v: %s", err, output)
			}
			content := strings.Repeat("<&>", maxWorkspaceDiffBytes/3)
			changesWrite(t, dir, "large-committed.txt", content)
			changesGit(t, dir, "add", ".")
			changesGit(t, dir, "commit", "-qm", "Add large file")
			changesWrite(t, dir, "large-working.txt", content)
			c := changesCollector(t, dir, t.TempDir())
			for _, scope := range []string{workspaceChangesUncommitted, workspaceChangesBranch} {
				changes, err := c.capture(t.Context(), scope, baseRef)
				if err != nil {
					t.Fatal(err)
				}
				if !changes.BranchesTruncated || len(changes.Branches) == 0 || len(changes.Branches) > maxWorkspaceBranches {
					t.Fatalf("scope %s has %d branches, truncated=%v", scope, len(changes.Branches), changes.BranchesTruncated)
				}
				if !longNames && len(changes.Branches) != maxWorkspaceBranches {
					t.Fatalf("branch count = %d, want %d", len(changes.Branches), maxWorkspaceBranches)
				}
				if longNames && len(changes.Branches) == maxWorkspaceBranches {
					t.Fatal("long branch names did not trigger the encoded size limit")
				}
				if scope == workspaceChangesBranch && (!slices.Contains(changes.Branches, baseRef) || changes.Base != head) {
					t.Fatalf("selected base was omitted: %s at %s", changes.BaseRef, changes.Base)
				}
				branches, err := json.Marshal(changes.Branches)
				if err != nil || len(branches) > maxWorkspaceBranchListBytes {
					t.Fatalf("encoded branch list has %d bytes, error = %v", len(branches), err)
				}
				if len(changes.Files) != 1 || !changes.Truncated {
					t.Fatalf("large diff has %d files, truncated=%v", len(changes.Files), changes.Truncated)
				}
				encoded, err := json.Marshal(Event{Type: EventWorkspaceChanges, RequestID: "changes-test", Changes: &changes})
				if err != nil || len(encoded)+1 >= 8*1024*1024 {
					t.Fatalf("encoded event has %d bytes, error = %v", len(encoded)+1, err)
				}
			}
		})
	}
}

func TestWorkspaceChangesComparesContentAfterUntracking(t *testing.T) {
	dir := changesRepository(t)
	c := changesCollector(t, dir, t.TempDir())
	changesGit(t, dir, "rm", "--cached", "tracked.txt")
	if changes := captureChanges(t, c); len(changes.Files) != 0 {
		t.Fatalf("unchanged content = %#v", changes)
	}
	changesWrite(t, dir, "tracked.txt", "updated\n")
	changes := captureChanges(t, c)
	if len(changes.Files) != 1 || !strings.Contains(changes.Files[0].Diff, "-original\n+updated") {
		t.Fatalf("untracked modification = %#v", changes)
	}
}

func TestWorkspaceChangesRelativeStateDirectory(t *testing.T) {
	dir, stateDir := changesRepository(t), t.TempDir()
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	relative, err := filepath.Rel(cwd, stateDir)
	if err != nil {
		t.Fatal(err)
	}
	c := changesCollector(t, dir, relative)
	changesWrite(t, dir, "new.txt", "new\n")
	if changes := captureChanges(t, c); len(changes.Files) != 1 || changes.Files[0].Name != "new.txt" {
		t.Fatalf("relative state directory = %#v", changes)
	}
}

func TestWorkspaceChangesKeepsFilesWhenDiffTruncated(t *testing.T) {
	dir := changesRepository(t)
	c := changesCollector(t, dir, t.TempDir())
	changesWrite(t, dir, "a-large.txt", strings.Repeat("long line\n", maxWorkspaceDiffBytes/5))
	changesWrite(t, dir, "z-small.txt", "small\n")
	changes := captureChanges(t, c)
	if !changes.Truncated || len(changes.Files) != 2 || changes.Files[1].Name != "z-small.txt" {
		t.Fatalf("truncated snapshot has %d files, truncated=%v", len(changes.Files), changes.Truncated)
	}
	if !strings.Contains(changes.Files[0].Diff, "[diff truncated]") || !strings.Contains(changes.Files[1].Diff, "Diff omitted") {
		t.Fatal("missing content limit notices")
	}
}

func TestWorkspaceChangesBoundsEncodedSnapshots(t *testing.T) {
	for _, longNames := range []bool{false, true} {
		t.Run(fmt.Sprintf("longNames=%t", longNames), func(t *testing.T) {
			dir := changesRepository(t)
			c := changesCollector(t, dir, t.TempDir())
			// HTML characters exercise JSON's six-byte escaping alongside file limits.
			changesWrite(t, dir, "a-large.txt", strings.Repeat("<&>", maxWorkspaceDiffBytes/3))
			for i := range maxWorkspaceChangeFiles {
				name := fmt.Sprintf("z-%04d", i)
				if longNames {
					name += strings.Repeat("&", 200)
				}
				changesWrite(t, dir, name, "")
			}
			changes := captureChanges(t, c)
			if !changes.Truncated || !strings.Contains(changes.Message, "file list truncated") {
				t.Fatalf("missing file limit notice: %q, truncated=%v", changes.Message, changes.Truncated)
			}
			if len(changes.Files) == 0 || len(changes.Files) > maxWorkspaceChangeFiles || changes.Files[0].Name != "a-large.txt" {
				t.Fatalf("limited file list has %d entries", len(changes.Files))
			}
			if !longNames && len(changes.Files) != maxWorkspaceChangeFiles {
				t.Fatalf("file limit returned %d entries, want %d", len(changes.Files), maxWorkspaceChangeFiles)
			}
			if longNames && len(changes.Files) == maxWorkspaceChangeFiles {
				t.Fatal("long names did not trigger the encoded file-list limit")
			}
			encoded, err := json.Marshal(Event{Type: EventWorkspaceChanges, RequestID: "changes-test", Changes: &changes})
			if err != nil {
				t.Fatal(err)
			}
			if len(encoded)+1 >= 8*1024*1024 {
				t.Fatalf("encoded event has %d bytes and exceeds the console bridge limit", len(encoded)+1)
			}
		})
	}
}

func TestWorkspaceChangesIncludesOtherFilesWithUnbornNestedRepository(t *testing.T) {
	for _, name := range []string{"nested", "nested[1]"} {
		t.Run(name, func(t *testing.T) {
			dir := changesRepository(t)
			c := changesCollector(t, dir, t.TempDir())
			changesGit(t, dir, "init", "-q", name)
			changesGit(t, dir, "init", "-q", "nested1")
			changesGit(t, filepath.Join(dir, "nested1"), "-c", "user.name=Changes test", "-c", "user.email=changes@example.invalid", "commit", "--allow-empty", "-qm", "Nested baseline")
			nestedHead := changesGit(t, filepath.Join(dir, "nested1"), "rev-parse", "HEAD")
			changesWrite(t, dir, "new.txt", "new\n")
			changesWrite(t, dir, "tracked.txt", "updated\n")
			index, err := os.ReadFile(filepath.Join(dir, ".git", "index"))
			if err != nil {
				t.Fatal(err)
			}
			changes := captureChanges(t, c)
			if len(changes.Files) != 3 || changes.Files[0].Name != "nested1" || changes.Files[1].Name != "new.txt" || changes.Files[2].Name != "tracked.txt" {
				t.Fatalf("nested repository hid workspace files: %#v", changes.Files)
			}
			if !strings.Contains(changes.Files[0].Diff, "+Subproject commit "+nestedHead) {
				t.Fatalf("valid nested repository diff = %q", changes.Files[0].Diff)
			}
			if !changes.Truncated || !strings.Contains(changes.Message, name+"/") || !strings.Contains(changes.Message, "reading nested repository") {
				t.Fatalf("missing nested repository diagnostic: %q", changes.Message)
			}
			after, err := os.ReadFile(filepath.Join(dir, ".git", "index"))
			if err != nil || !bytes.Equal(index, after) {
				t.Fatalf("real index changed: %v", err)
			}
		})
	}
}

func TestWorkspaceChangesPreservesBothSidesOfFileTypeChanges(t *testing.T) {
	for _, startWithLink := range []bool{false, true} {
		t.Run(fmt.Sprintf("startWithLink=%t", startWithLink), func(t *testing.T) {
			dir := changesRepository(t)
			path := filepath.Join(dir, "tracked.txt")
			if startWithLink {
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink("target", path); err != nil {
					t.Fatal(err)
				}
				changesGit(t, dir, "add", ".")
				changesGit(t, dir, "commit", "-qm", "Use symlink")
			}
			c := changesCollector(t, dir, t.TempDir())
			if err := os.Remove(path); err != nil {
				t.Fatal(err)
			}
			removed, added := "original", "target"
			if startWithLink {
				changesWrite(t, dir, "tracked.txt", "updated\n")
				removed, added = "target", "updated"
			} else if err := os.Symlink("target", path); err != nil {
				t.Fatal(err)
			}
			changes := captureChanges(t, c)
			if len(changes.Files) != 1 {
				t.Fatalf("type change produced %d entries", len(changes.Files))
			}
			diff := changes.Files[0].Diff
			if strings.Count(diff, "diff --git ") != 2 || !strings.Contains(diff, "\ndiff --git ") ||
				!strings.Contains(diff, "\n-"+removed+"\n") || !strings.Contains(diff, "\n+"+added+"\n") {
				t.Fatalf("type change lost a patch segment: %q", diff)
			}
		})
	}
}

func TestWorkspaceChangesUnavailableAndErrors(t *testing.T) {
	dir, stateDir := t.TempDir(), t.TempDir()
	c := changesCollector(t, dir, stateDir)
	if changes := captureChanges(t, c); changes.Message == "" || changes.Base != "" {
		t.Fatalf("non-Git workspace = %#v", changes)
	}
	dir = changesRepository(t)
	c = changesCollector(t, dir, stateDir)
	changesWrite(t, dir, ".git/index", "invalid index\n")
	if _, err := c.capture(t.Context(), "", ""); err == nil || !strings.Contains(err.Error(), "listing untracked workspace files") || !strings.Contains(err.Error(), "fatal:") {
		t.Fatalf("invalid index diagnostic = %v", err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := c.capture(ctx, "", ""); err == nil {
		t.Fatal("cancelled capture succeeded")
	}
}

func TestWorkspaceChangesReportsGitFailuresWithoutBlockingStartup(t *testing.T) {
	dir, stateDir, bin := t.TempDir(), t.TempDir(), t.TempDir()
	git := filepath.Join(bin, "git")
	if err := os.WriteFile(git, []byte("#!/bin/sh\nprintf 'fatal: detected dubious ownership in repository\\n' >&2\nexit 128\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	c, err := newWorkspaceChangesCollector(dir, stateDir, os.Environ())
	if err != nil || c == nil {
		t.Fatalf("Git inspection failure blocks startup: collector=%v error=%v", c, err)
	}
	if _, err := c.capture(t.Context(), "", ""); err == nil || !strings.Contains(err.Error(), "dubious ownership") {
		t.Fatalf("Git diagnostic is unavailable: %v", err)
	}
	if _, err := newWorkspaceChangesCollector("", stateDir, os.Environ()); err == nil {
		t.Fatal("invalid configuration did not fail")
	}
	if _, err := newWorkspaceChangesCollector(dir, git, os.Environ()); err == nil {
		t.Fatal("a file was accepted as the state directory")
	}
}

func TestServerWorkspaceChangesIndependentOfProviderAndHistory(t *testing.T) {
	for _, provider := range []string{"codex", "claude-code", "opencode"} {
		t.Run(provider, func(t *testing.T) {
			dir := changesRepository(t)
			changesGit(t, dir, "branch", "review-base")
			collector := changesCollector(t, dir, t.TempDir())
			changesWrite(t, dir, "new.txt", "new\n")
			journal := NewJournal()
			defer journal.Close()
			if err := journal.Append(Event{Type: EventFileDiff, Diff: "outdated provider diff"}); err != nil {
				t.Fatal(err)
			}
			server := NewServer(Config{AgentType: provider}, journal, &fakeProvider{})
			server.workspaceChanges = collector
			for i, scope := range []string{"", workspaceChangesUncommitted, workspaceChangesBranch} {
				serverConn, client := net.Pipe()
				if err := client.SetDeadline(time.Now().Add(10 * time.Second)); err != nil {
					t.Fatal(err)
				}
				go server.handleConnection(t.Context(), serverConn)
				if err := json.NewEncoder(client).Encode(ClientRequest{Type: "workspace.changes", RequestID: "changes-test", Scope: scope, BaseRef: "refs/heads/review-base"}); err != nil {
					t.Fatal(err)
				}
				var event Event
				if err := json.NewDecoder(client).Decode(&event); err != nil {
					t.Fatal(err)
				}
				client.Close()
				wantFiles := 1
				if i == 1 {
					wantFiles = 0
				}
				if event.Type != EventWorkspaceChanges || event.RequestID != "changes-test" || event.Changes == nil || event.Changes.Message != "" || len(event.Changes.Files) != wantFiles {
					t.Fatalf("snapshot %d = %#v", i, event)
				}
				if i == 2 && (event.Changes.Scope != workspaceChangesBranch || event.Changes.BaseRef != "refs/heads/review-base") {
					t.Fatalf("branch snapshot = %#v", event.Changes)
				}
				if i == 0 {
					if event.Changes.Files[0].Name != "new.txt" || !strings.Contains(event.Changes.Files[0].Diff, "+new") {
						t.Fatalf("new file = %#v", event.Changes.Files)
					}
					changesGit(t, dir, "add", "new.txt")
					changesGit(t, dir, "commit", "-qm", "Add file")
				}
			}
			if len(journal.Snapshot()) != 1 {
				t.Fatal("workspace snapshots leaked into conversation history")
			}
		})
	}
}
