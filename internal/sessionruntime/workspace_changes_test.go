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
	c, err := newWorkspaceChangesCollector(t.Context(), dir, stateDir, os.Environ())
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func captureChanges(t *testing.T, c *workspaceChangesCollector) WorkspaceChanges {
	t.Helper()
	changes, err := c.capture(t.Context())
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

func TestWorkspaceChangesPersistsBaseAcrossCommitsAndRestarts(t *testing.T) {
	dir, stateDir := changesRepository(t), t.TempDir()
	c := changesCollector(t, dir, stateDir)
	base := captureChanges(t, c).Base
	changesWrite(t, dir, "created.txt", "new file\n")
	changesGit(t, dir, "add", ".")
	changesGit(t, dir, "commit", "-qm", "Add file")
	c = changesCollector(t, dir, stateDir)
	changes := captureChanges(t, c)
	if changes.Base != base || len(changes.Files) != 1 || changes.Files[0].Name != "created.txt" {
		t.Fatalf("committed changes = %#v", changes)
	}
	changesGit(t, dir, "restore", "--source", base, "--staged", "--worktree", ".")
	if changes := captureChanges(t, c); len(changes.Files) != 0 {
		t.Fatalf("reverted changes = %#v", changes)
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
	changesWrite(t, stateDir, workspaceChangesBaseFile, "invalid-revision\n")
	if _, err := c.capture(t.Context()); err == nil || !strings.Contains(err.Error(), "invalid-revision") || !strings.Contains(err.Error(), "fatal:") {
		t.Fatalf("invalid base diagnostic = %v", err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := c.capture(ctx); err == nil {
		t.Fatal("cancelled capture succeeded")
	}
}

func TestWorkspaceChangesInitializationReportsGitFailuresWithoutBlocking(t *testing.T) {
	dir, stateDir, bin := t.TempDir(), t.TempDir(), t.TempDir()
	git := filepath.Join(bin, "git")
	if err := os.WriteFile(git, []byte("#!/bin/sh\nprintf 'fatal: detected dubious ownership in repository\\n' >&2\nexit 128\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	c, err := newWorkspaceChangesCollector(t.Context(), dir, stateDir, os.Environ())
	if err != nil || c == nil {
		t.Fatalf("Git inspection failure blocks startup: collector=%v error=%v", c, err)
	}
	if _, err := c.capture(t.Context()); err == nil || !strings.Contains(err.Error(), "dubious ownership") {
		t.Fatalf("Git diagnostic is unavailable: %v", err)
	}
	if _, err := os.Stat(filepath.Join(stateDir, workspaceChangesBaseFile)); !os.IsNotExist(err) {
		t.Fatalf("failed Git inspection recorded a comparison base: %v", err)
	}
	if _, err := newWorkspaceChangesCollector(t.Context(), "", stateDir, os.Environ()); err == nil {
		t.Fatal("invalid configuration did not fail")
	}
	if _, err := newWorkspaceChangesCollector(t.Context(), dir, git, os.Environ()); err == nil {
		t.Fatal("a file was accepted as the state directory")
	}
}

func TestServerWorkspaceChangesIndependentOfProviderAndHistory(t *testing.T) {
	for _, provider := range []string{"codex", "claude-code", "opencode"} {
		t.Run(provider, func(t *testing.T) {
			dir := changesRepository(t)
			collector := changesCollector(t, dir, t.TempDir())
			changesWrite(t, dir, "new.txt", "new\n")
			journal := NewJournal()
			defer journal.Close()
			if err := journal.Append(Event{Type: EventFileDiff, Diff: "outdated provider diff"}); err != nil {
				t.Fatal(err)
			}
			server := NewServer(Config{AgentType: provider}, journal, &fakeProvider{})
			server.workspaceChanges = collector
			for i := range 2 {
				serverConn, client := net.Pipe()
				if err := client.SetDeadline(time.Now().Add(10 * time.Second)); err != nil {
					t.Fatal(err)
				}
				go server.handleConnection(t.Context(), serverConn)
				if err := json.NewEncoder(client).Encode(ClientRequest{Type: "workspace.changes", RequestID: "changes-test"}); err != nil {
					t.Fatal(err)
				}
				var event Event
				if err := json.NewDecoder(client).Decode(&event); err != nil {
					t.Fatal(err)
				}
				client.Close()
				if event.Type != EventWorkspaceChanges || event.RequestID != "changes-test" || event.Changes == nil || event.Changes.Message != "" || len(event.Changes.Files) != 1-i {
					t.Fatalf("snapshot %d = %#v", i, event)
				}
				if i == 0 {
					if event.Changes.Files[0].Name != "new.txt" || !strings.Contains(event.Changes.Files[0].Diff, "+new") {
						t.Fatalf("new file = %#v", event.Changes.Files)
					}
					if err := os.Remove(filepath.Join(dir, "new.txt")); err != nil {
						t.Fatal(err)
					}
				}
			}
			if len(journal.Snapshot()) != 1 {
				t.Fatal("workspace snapshots leaked into conversation history")
			}
		})
	}
}
