package sessionrunner

import (
	"os"
	"testing"
	"time"
)

func TestConfigFromEnv_Defaults(t *testing.T) {
	// Clear relevant env vars.
	os.Unsetenv("KELOS_POD_NAME")
	os.Unsetenv("KELOS_POD_NAMESPACE")
	os.Unsetenv("KELOS_AGENT_TYPE")
	os.Unsetenv("KELOS_TASKSPAWNER")
	os.Unsetenv("KELOS_IDLE_TIMEOUT")
	os.Unsetenv("KELOS_MAX_TASKS_PER_SESSION")
	os.Unsetenv("KELOS_MAX_SESSION_DURATION")

	cfg := ConfigFromEnv()

	if cfg.IdleTimeout != defaultIdleTimeout {
		t.Errorf("IdleTimeout: expected %v, got %v", defaultIdleTimeout, cfg.IdleTimeout)
	}
	if cfg.MaxSessionDuration != defaultMaxSessionDuration {
		t.Errorf("MaxSessionDuration: expected %v, got %v", defaultMaxSessionDuration, cfg.MaxSessionDuration)
	}
	if cfg.MaxTasksPerSession != 0 {
		t.Errorf("MaxTasksPerSession: expected 0, got %d", cfg.MaxTasksPerSession)
	}
}

func TestConfigFromEnv_CustomValues(t *testing.T) {
	t.Setenv("KELOS_POD_NAME", "session-pod-0")
	t.Setenv("KELOS_POD_NAMESPACE", "test-ns")
	t.Setenv("KELOS_AGENT_TYPE", "claude-code")
	t.Setenv("KELOS_TASKSPAWNER", "my-spawner")
	t.Setenv("KELOS_IDLE_TIMEOUT", "15m")
	t.Setenv("KELOS_MAX_TASKS_PER_SESSION", "5")
	t.Setenv("KELOS_MAX_SESSION_DURATION", "4h")

	cfg := ConfigFromEnv()

	if cfg.PodName != "session-pod-0" {
		t.Errorf("PodName: expected 'session-pod-0', got %q", cfg.PodName)
	}
	if cfg.PodNamespace != "test-ns" {
		t.Errorf("PodNamespace: expected 'test-ns', got %q", cfg.PodNamespace)
	}
	if cfg.AgentType != "claude-code" {
		t.Errorf("AgentType: expected 'claude-code', got %q", cfg.AgentType)
	}
	if cfg.TaskSpawner != "my-spawner" {
		t.Errorf("TaskSpawner: expected 'my-spawner', got %q", cfg.TaskSpawner)
	}
	if cfg.IdleTimeout != 15*time.Minute {
		t.Errorf("IdleTimeout: expected 15m, got %v", cfg.IdleTimeout)
	}
	if cfg.MaxTasksPerSession != 5 {
		t.Errorf("MaxTasksPerSession: expected 5, got %d", cfg.MaxTasksPerSession)
	}
	if cfg.MaxSessionDuration != 4*time.Hour {
		t.Errorf("MaxSessionDuration: expected 4h, got %v", cfg.MaxSessionDuration)
	}
}

func TestConfigFromEnv_InvalidDuration(t *testing.T) {
	t.Setenv("KELOS_IDLE_TIMEOUT", "not-a-duration")

	cfg := ConfigFromEnv()

	// Should fall back to default.
	if cfg.IdleTimeout != defaultIdleTimeout {
		t.Errorf("IdleTimeout: expected default %v on invalid input, got %v", defaultIdleTimeout, cfg.IdleTimeout)
	}
}

func TestConfigFromEnv_InvalidMaxTasks(t *testing.T) {
	t.Setenv("KELOS_MAX_TASKS_PER_SESSION", "abc")

	cfg := ConfigFromEnv()

	if cfg.MaxTasksPerSession != 0 {
		t.Errorf("MaxTasksPerSession: expected 0 on invalid input, got %d", cfg.MaxTasksPerSession)
	}
}

func TestParseOutputs(t *testing.T) {
	tests := []struct {
		name   string
		input  string
		expect []string
	}{
		{
			name:   "no markers",
			input:  "some random log output\n",
			expect: nil,
		},
		{
			name:   "empty between markers",
			input:  "---KELOS_OUTPUTS_START---\n---KELOS_OUTPUTS_END---\n",
			expect: nil,
		},
		{
			name:   "single output",
			input:  "log line\n---KELOS_OUTPUTS_START---\nbranch: main\n---KELOS_OUTPUTS_END---\n",
			expect: []string{"branch: main"},
		},
		{
			name:   "multiple outputs",
			input:  "---KELOS_OUTPUTS_START---\nbranch: feat\ncommit: abc123\nresponse: dGVzdA==\n---KELOS_OUTPUTS_END---\n",
			expect: []string{"branch: feat", "commit: abc123", "response: dGVzdA=="},
		},
		{
			name:   "start without end",
			input:  "---KELOS_OUTPUTS_START---\nbranch: main\n",
			expect: nil,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := parseOutputs(tc.input)
			if tc.expect == nil {
				if got != nil {
					t.Errorf("expected nil, got %v", got)
				}
				return
			}
			if len(got) != len(tc.expect) {
				t.Fatalf("expected %d outputs, got %d: %v", len(tc.expect), len(got), got)
			}
			for i, want := range tc.expect {
				if got[i] != want {
					t.Errorf("output[%d]: expected %q, got %q", i, want, got[i])
				}
			}
		})
	}
}

func TestResultsFromOutputs(t *testing.T) {
	outputs := []string{"branch: main", "commit: abc123", "cost-usd: 0.05"}
	results := resultsFromOutputs(outputs)

	if results["branch"] != "main" {
		t.Errorf("branch: expected 'main', got %q", results["branch"])
	}
	if results["commit"] != "abc123" {
		t.Errorf("commit: expected 'abc123', got %q", results["commit"])
	}
	if results["cost-usd"] != "0.05" {
		t.Errorf("cost-usd: expected '0.05', got %q", results["cost-usd"])
	}
}

func TestResultsFromOutputs_Empty(t *testing.T) {
	if got := resultsFromOutputs(nil); got != nil {
		t.Errorf("expected nil, got %v", got)
	}
	if got := resultsFromOutputs([]string{}); got != nil {
		t.Errorf("expected nil, got %v", got)
	}
}

func TestTailWriter_SmallWrite(t *testing.T) {
	tw := newTailWriter(100)
	tw.Write([]byte("hello"))
	if got := tw.String(); got != "hello" {
		t.Errorf("expected 'hello', got %q", got)
	}
}

func TestTailWriter_ExactFit(t *testing.T) {
	tw := newTailWriter(5)
	tw.Write([]byte("hello"))
	if got := tw.String(); got != "hello" {
		t.Errorf("expected 'hello', got %q", got)
	}
}

func TestTailWriter_Overflow(t *testing.T) {
	tw := newTailWriter(5)
	tw.Write([]byte("hello world"))
	if got := tw.String(); got != "world" {
		t.Errorf("expected 'world', got %q", got)
	}
}

func TestTailWriter_MultipleWrites(t *testing.T) {
	tw := newTailWriter(10)
	tw.Write([]byte("aaaa"))
	tw.Write([]byte("bbbb"))
	tw.Write([]byte("cccc"))
	got := tw.String()
	// Total written: 12 bytes ("aaaabbbbcccc"), buffer is 10, so last 10 = "aabbbbcccc"
	want := "aabbbbcccc"
	if got != want {
		t.Errorf("expected %q, got %q", want, got)
	}
}

func TestTailWriter_PreservesOutputMarkers(t *testing.T) {
	tw := newTailWriter(256)
	// Write a bunch of noise first
	for i := 0; i < 100; i++ {
		tw.Write([]byte("noise line that should be evicted\n"))
	}
	// Then write the markers at the end
	tw.Write([]byte("---KELOS_OUTPUTS_START---\nbranch: main\ncommit: abc\n---KELOS_OUTPUTS_END---\n"))

	got := tw.String()
	outputs := parseOutputs(got)
	if len(outputs) != 2 {
		t.Fatalf("expected 2 outputs, got %d from tail: %q", len(outputs), got[max(0, len(got)-200):])
	}
	if outputs[0] != "branch: main" {
		t.Errorf("output[0]: expected 'branch: main', got %q", outputs[0])
	}
}
