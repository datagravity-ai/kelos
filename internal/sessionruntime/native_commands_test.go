package sessionruntime

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

func replyToCodexRequest(t *testing.T, provider *CodexProvider, stdin *channelWriteCloser, method, result string) map[string]any {
	t.Helper()
	var data []byte
	select {
	case data = <-stdin.writes:
	case <-time.After(5 * time.Second):
		t.Fatalf("Codex request %s did not arrive", method)
	}
	var request struct {
		ID     int64          `json:"id"`
		Method string         `json:"method"`
		Params map[string]any `json:"params"`
	}
	if err := json.Unmarshal(data, &request); err != nil {
		t.Fatal(err)
	}
	if request.Method != method {
		t.Fatalf("Request method = %q, want %q", request.Method, method)
	}
	provider.pendingMu.Lock()
	pending := provider.pending[strconv.FormatInt(request.ID, 10)]
	provider.pendingMu.Unlock()
	pending <- codexResponse{Result: json.RawMessage(result)}
	return request.Params
}

func TestCodexDiscoversAndInvokesNativeLoopSkill(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		t.Run(fmt.Sprint(enabled), func(t *testing.T) {
			stdin := &channelWriteCloser{writes: make(chan []byte, 1)}
			provider := &CodexProvider{ctx: t.Context(), config: ProviderConfig{WorkingDir: "/workspace/project"}, stdin: stdin, pending: map[string]chan codexResponse{}, threadID: "thread-1", done: make(chan struct{})}
			loaded := make(chan error, 1)
			go func() { loaded <- provider.loadLoopSkill(t.Context()) }()
			params := replyToCodexRequest(t, provider, stdin, "skills/list", fmt.Sprintf(`{"data":[{"cwd":"/workspace/project","skills":[{"name":"loop","path":"/skills/loop/SKILL.md","enabled":%t}]}]}`, enabled))
			if !reflect.DeepEqual(params["cwds"], []any{"/workspace/project"}) || params["forceReload"] != true {
				t.Fatalf("Skills params = %#v", params)
			}
			if err := <-loaded; err != nil {
				t.Fatal(err)
			}
			status := provider.runtimeStatusSnapshot()
			if slices.Contains(status.Commands, "loop") != enabled || !slices.Contains(status.Commands, "goal") {
				t.Fatalf("Native commands = %#v", status.Commands)
			}
			journal := NewJournal()
			defer journal.Close()
			server := NewServer(Config{AgentType: "codex"}, journal, provider)
			err := server.submitClientMessage(t.Context(), "/loop 5m Check CI", "loop")
			if !enabled {
				if err == nil || len(server.turns) != 0 {
					t.Fatalf("Disabled loop was accepted: %v", err)
				}
				if err := provider.StartLoop(t.Context(), "/loop 5m Check CI"); err == nil {
					t.Fatal("Missing loop skill was invoked")
				}
				return
			}
			if err != nil || len(server.turns) != 1 {
				t.Fatalf("Enabled loop was not accepted: %v", err)
			}
			sink := newProviderTestSink(nil)
			provider.SetEventSink(sink)
			started := make(chan error, 1)
			go func() { started <- provider.StartLoop(t.Context(), "/loop 5m Check CI") }()
			params = replyToCodexRequest(t, provider, stdin, "turn/start", `{"turn":{"id":"native-turn"}}`)
			want := []any{
				map[string]any{"type": "text", "text": "$loop 5m Check CI"},
				map[string]any{"type": "skill", "name": "loop", "path": "/skills/loop/SKILL.md"},
			}
			if params["threadId"] != "thread-1" || !reflect.DeepEqual(params["input"], want) {
				t.Fatalf("Native loop params = %#v", params)
			}
			if err := <-started; err != nil {
				t.Fatal(err)
			}
			provider.handleNotification("turn/started", json.RawMessage(`{"threadId":"thread-1","turn":{"id":"native-turn"}}`))
			provider.handleNotification("turn/completed", json.RawMessage(`{"threadId":"thread-1","turn":{"id":"native-turn","status":"completed"}}`))
			sink.waitForCompletion(t, "completed")
		})
	}
}

func TestCodexMissingLoopSkill(t *testing.T) {
	stdin := &channelWriteCloser{writes: make(chan []byte, 1)}
	provider := &CodexProvider{config: ProviderConfig{WorkingDir: "/workspace/project"}, stdin: stdin, pending: map[string]chan codexResponse{}, done: make(chan struct{})}
	done := make(chan error, 1)
	go func() { done <- provider.loadLoopSkill(t.Context()) }()
	replyToCodexRequest(t, provider, stdin, "skills/list", `{"data":[{"cwd":"/workspace/project","skills":[]}]}`)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if slices.Contains(provider.runtimeStatusSnapshot().Commands, "loop") {
		t.Fatal("Loop was advertised without a skill")
	}
}

func TestOpenCodeNativeGoalAndLoopCommands(t *testing.T) {
	fake := newFakeOpenCodeServer(t)
	fake.nativeCommands = []string{"init", "goal", "loop"}
	provider := newTestOpenCodeProvider(t, fake, ProviderConfig{WorkingDir: t.TempDir(), StateDir: t.TempDir()})
	journal := NewJournal()
	defer journal.Close()
	server := NewServer(Config{AgentType: "opencode"}, journal, provider)
	server.readWorkspaceDiff = func(context.Context, string) string { return "" }
	if !reflect.DeepEqual(server.runtimeStatusSnapshot().Commands, []string{"shell", "goal", "loop"}) {
		t.Fatalf("Native commands = %#v", server.runtimeStatusSnapshot().Commands)
	}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() { defer close(done); server.runTurns(ctx) }()
	defer func() { cancel(); <-done }()
	for i, test := range []struct{ text, command, arguments string }{
		{"/goal", "goal", ""},
		{"/goal edit", "goal", "edit"},
		{"/goal pause", "goal", "pause"},
		{"/goal clear", "goal", "clear"},
		{"/loop check CI every 2 hours", "loop", "check CI every 2 hours"},
	} {
		if err := server.submitClientMessage(ctx, test.text, "native-command"); err != nil {
			t.Fatal(err)
		}
		request := receiveOpenCodePrompt(t, fake.commandRequests)
		if !reflect.DeepEqual(request, map[string]any{"command": test.command, "arguments": test.arguments}) {
			t.Fatalf("Native command input = %#v", request)
		}
		fake.emit("session.status", map[string]any{"sessionID": fake.sessionID, "status": map[string]string{"type": "busy"}})
		fake.emit("session.idle", map[string]string{"sessionID": fake.sessionID})
		waitForSessionEvent(t, journal, func(event Event) bool {
			return event.Type == EventTurnCompleted && event.TurnID == fmt.Sprintf("turn-%d", i+1) && event.Status == "completed"
		})
	}
	if len(fake.prompts) != 0 {
		t.Fatal("Native commands used the ordinary prompt endpoint")
	}
}

func TestOpenCodeMissingNativeCommands(t *testing.T) {
	fake := newFakeOpenCodeServer(t)
	provider := newTestOpenCodeProvider(t, fake, ProviderConfig{WorkingDir: t.TempDir(), StateDir: t.TempDir()})
	journal := NewJournal()
	defer journal.Close()
	server := NewServer(Config{AgentType: "opencode"}, journal, provider)
	for _, text := range []string{"/goal edit", "/loop Check CI"} {
		if err := server.submitClientMessage(t.Context(), text, "unsupported"); err == nil {
			t.Fatalf("Missing native command %q was accepted", text)
		}
	}
	if len(fake.commandRequests) != 0 || len(server.turns) != 0 {
		t.Fatal("Missing native commands started work")
	}
}

func TestOpenCodeNativeCommandFailure(t *testing.T) {
	fake := newFakeOpenCodeServer(t)
	fake.nativeCommands = []string{"loop"}
	fake.commandFailure = http.StatusBadRequest
	provider := newTestOpenCodeProvider(t, fake, ProviderConfig{WorkingDir: t.TempDir(), StateDir: t.TempDir()})
	sink := newProviderTestSink(nil)
	provider.SetEventSink(sink)
	if err := provider.StartLoop(t.Context(), "/loop Check CI"); err != nil {
		t.Fatal(err)
	}
	select {
	case event := <-sink.completed:
		if event.Status != "failed" || !strings.Contains(event.Text, "Running OpenCode loop command") {
			t.Fatalf("Command failure = %#v", event)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Native command failure did not complete the turn")
	}
}

func TestRuntimeCommandsAreRetainedAndCloned(t *testing.T) {
	journal := NewJournal()
	defer journal.Close()
	server := NewServer(Config{}, journal, &fakeProvider{})
	commands := []string{"shell", "loop"}
	server.updateProviderRuntimeStatus(RuntimeStatus{Commands: commands})
	commands[1] = "goal"
	server.updateProviderRuntimeStatus(RuntimeStatus{Model: "native-model"})
	status := server.runtimeStatusSnapshot()
	if !reflect.DeepEqual(status.Commands, []string{"shell", "loop"}) {
		t.Fatalf("Runtime commands = %#v", status.Commands)
	}
	status.Commands[1] = "goal"
	if server.runtimeStatusSnapshot().Commands[1] != "loop" {
		t.Fatal("Snapshot changed command availability")
	}
}

func TestOpenCodeNativeCommandCanBeInterrupted(t *testing.T) {
	fake := newFakeOpenCodeServer(t)
	fake.nativeCommands = []string{"loop"}
	fake.commandWait = make(chan struct{})
	provider := newTestOpenCodeProvider(t, fake, ProviderConfig{WorkingDir: t.TempDir(), StateDir: t.TempDir()})
	sink := newProviderTestSink(nil)
	provider.SetEventSink(sink)
	if err := provider.StartLoop(t.Context(), "/loop Check CI"); err != nil {
		t.Fatal(err)
	}
	receiveOpenCodePrompt(t, fake.commandRequests)
	if err := provider.Interrupt(t.Context()); err != nil {
		t.Fatal(err)
	}
	sink.waitForCompletion(t, "interrupted")
	select {
	case <-fake.aborts:
	default:
		t.Fatal("Native command was not aborted")
	}
}

func TestCodexSkillsChangedRefreshesNativeCommands(t *testing.T) {
	stdin := &channelWriteCloser{writes: make(chan []byte, 1)}
	provider := &CodexProvider{ctx: t.Context(), config: ProviderConfig{WorkingDir: "/workspace/project"}, stdin: stdin, pending: map[string]chan codexResponse{}, done: make(chan struct{}), loopSkillPath: "/skills/loop/SKILL.md"}
	journal := NewJournal()
	defer journal.Close()
	server := NewServer(Config{AgentType: "codex"}, journal, provider)
	updates, cancel := server.subscribeRuntimeStatus()
	defer cancel()
	provider.handleNotification("skills/changed", nil)
	replyToCodexRequest(t, provider, stdin, "skills/list", `{"data":[{"cwd":"/workspace/project","skills":[]}]}`)
	select {
	case status := <-updates:
		if !reflect.DeepEqual(status.Commands, []string{"shell", "goal"}) {
			t.Fatalf("Refreshed commands = %#v", status.Commands)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Changed skills did not update command availability")
	}
}
