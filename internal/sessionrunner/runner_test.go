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
