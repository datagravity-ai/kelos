package sessionruntime

import (
	"strings"
	"testing"
)

func TestParseSessionCommand(t *testing.T) {
	tests := []struct {
		name          string
		input         string
		kind          sessionCommandKind
		text          string
		goalAction    goalCommandAction
		goalObjective string
	}{
		{name: "message", input: "review this", kind: sessionCommandMessage, text: "review this"},
		{name: "unrecognized slash command", input: "/review", kind: sessionCommandMessage, text: "/review"},
		{name: "shell", input: "! make test", kind: sessionCommandShell, text: "make test"},
		{name: "show goal", input: "/goal", kind: sessionCommandGoal, goalAction: goalCommandShow},
		{name: "create goal", input: "/goal improve coverage", kind: sessionCommandGoal, goalAction: goalCommandCreate, goalObjective: "improve coverage"},
		{name: "multiline goal", input: "/goal\nimprove coverage", kind: sessionCommandGoal, goalAction: goalCommandCreate, goalObjective: "improve coverage"},
		{name: "edit goal", input: "/goal edit improve tests", kind: sessionCommandGoal, goalAction: goalCommandEdit, goalObjective: "improve tests"},
		{name: "clear goal", input: "/goal clear", kind: sessionCommandGoal, goalAction: goalCommandClear},
		{name: "pause goal", input: "/goal pause", kind: sessionCommandGoal, goalAction: goalCommandPause},
		{name: "resume goal", input: "/goal resume", kind: sessionCommandGoal, goalAction: goalCommandResume},
		{name: "objective starts with command", input: "/goal clear tech debt", kind: sessionCommandGoal, goalAction: goalCommandCreate, goalObjective: "clear tech debt"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			command, err := parseSessionCommand(test.input, "codex")
			if err != nil {
				t.Fatal(err)
			}
			if command.kind != test.kind || command.text != test.text || command.goal.action != test.goalAction || command.goal.objective != test.goalObjective {
				t.Fatalf("parseSessionCommand(%q) = %#v", test.input, command)
			}
		})
	}
}

func TestParseClaudeGoalCommand(t *testing.T) {
	for _, test := range []struct {
		text      string
		action    goalCommandAction
		objective string
	}{
		{text: "/goal", action: goalCommandShow},
		{text: "/goal All tests pass", action: goalCommandCreate, objective: "All tests pass"},
		{text: "/goal edit tests until they pass", action: goalCommandCreate, objective: "edit tests until they pass"},
		{text: "/goal edit", action: goalCommandCreate, objective: "edit"},
		{text: "/goal pause", action: goalCommandCreate, objective: "pause"},
		{text: "/goal resume", action: goalCommandCreate, objective: "resume"},
		{text: "/goal clear", action: goalCommandClear},
		{text: "/goal STOP", action: goalCommandClear},
		{text: "/goal off", action: goalCommandClear},
		{text: "/goal reset", action: goalCommandClear},
		{text: "/goal none", action: goalCommandClear},
		{text: "/goal cancel", action: goalCommandClear},
		{text: "/goal clear tech debt", action: goalCommandCreate, objective: "clear tech debt"},
	} {
		t.Run(test.text, func(t *testing.T) {
			command, err := parseSessionCommand(test.text, "claude-code")
			if err != nil {
				t.Fatal(err)
			}
			if command.kind != sessionCommandGoal || command.goal.action != test.action || command.goal.objective != test.objective {
				t.Fatalf("Claude goal command = %#v", command)
			}
		})
	}
}

func TestParseClaudeLoopCommand(t *testing.T) {
	for _, text := range []string{"/loop", "/loop 5m Check CI", "/loop 15m", "/loop Check CI", "/loop check CI every 2 hours", "/loop pause", "/loop clear", "/loop\nCheck CI"} {
		command, err := parseSessionCommand(text, "claude-code")
		if err != nil || command.kind != sessionCommandLoop || command.text != text {
			t.Fatalf("parseSessionCommand(%q) = %#v, %v", text, command, err)
		}
	}
	for _, text := range []string{"/loops", "/looping"} {
		command, err := parseSessionCommand(text, "claude-code")
		if err != nil || command.kind != sessionCommandMessage {
			t.Fatalf("parseSessionCommand(%q) = %#v, %v", text, command, err)
		}
	}
}

func TestParseSessionCommandRejectsInvalidCommands(t *testing.T) {
	for _, input := range []string{"!", "/goal edit"} {
		t.Run(strings.ReplaceAll(input, " ", "_"), func(t *testing.T) {
			if _, err := parseSessionCommand(input, "codex"); err == nil {
				t.Fatalf("parseSessionCommand(%q) returned no error", input)
			}
		})
	}
}
