package controller

import (
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestAgentProcessCommand(t *testing.T) {
	got := agentProcessCommand("/usr/bin/printf")
	want := []string{"/bin/sh", "-c", agentProcessScript, "--", "/usr/bin/printf"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("agentProcessCommand() = %v, want %v", got, want)
	}

	command := exec.Command(got[0], append(got[1:], "%s", "hello")...)
	output, err := command.Output()
	if err != nil {
		t.Fatalf("running wrapped command: %v", err)
	}
	if string(output) != "hello" {
		t.Fatalf("wrapped command output = %q, want %q", output, "hello")
	}
}

func TestBundledAgentImagesUseTini(t *testing.T) {
	tests := []struct {
		dir        string
		entrypoint string
	}{
		{dir: "claude-code", entrypoint: "claude"},
		{dir: "codex", entrypoint: "codex"},
		{dir: "gemini", entrypoint: "gemini"},
		{dir: "opencode", entrypoint: "opencode"},
		{dir: "cursor", entrypoint: "agent"},
	}

	for _, tt := range tests {
		t.Run(tt.dir, func(t *testing.T) {
			data, err := os.ReadFile(filepath.Join("..", "..", tt.dir, "Dockerfile"))
			if err != nil {
				t.Fatalf("reading Dockerfile: %v", err)
			}
			content := string(data)
			if !strings.Contains(content, "\n    tini \\\n") {
				t.Fatal("Dockerfile does not install tini")
			}
			wantEntrypoint := `ENTRYPOINT ["/usr/bin/tini", "-g", "--", "` + tt.entrypoint + `"]`
			if !strings.Contains(content, wantEntrypoint) {
				t.Fatalf("Dockerfile does not contain %q", wantEntrypoint)
			}
		})
	}
}

func assertAgentProcessCommand(t *testing.T, got []string, program string) {
	t.Helper()
	want := agentProcessCommand(program)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("agent command = %v, want %v", got, want)
	}
}
