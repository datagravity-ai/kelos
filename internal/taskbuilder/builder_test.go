package taskbuilder

import (
	"testing"

	"github.com/kelos-dev/kelos/api/v1alpha1"
)

func TestBuildTask_PersistentModeLabel(t *testing.T) {
	tb := &TaskBuilder{}

	tmpl := &v1alpha1.TaskTemplate{
		Type: "claude-code",
		Credentials: v1alpha1.Credentials{
			Type:      v1alpha1.CredentialTypeAPIKey,
			SecretRef: &v1alpha1.SecretReference{Name: "secret"},
		},
		WorkspaceRef: &v1alpha1.WorkspaceReference{Name: "ws"},
	}

	task, err := tb.BuildTask("test-task", "default", tmpl, map[string]interface{}{
		"Title": "Test",
	}, &SpawnerRef{
		Name:          "my-spawner",
		UID:           "uid-123",
		APIVersion:    "kelos.dev/v1alpha1",
		Kind:          "TaskSpawner",
		ExecutionMode: v1alpha1.ExecutionModePersistent,
	})
	if err != nil {
		t.Fatalf("BuildTask() returned error: %v", err)
	}

	if task.Labels["kelos.dev/execution-mode"] != "persistent" {
		t.Errorf("Expected label kelos.dev/execution-mode=persistent, got %q", task.Labels["kelos.dev/execution-mode"])
	}
	if task.Labels["kelos.dev/taskspawner"] != "my-spawner" {
		t.Errorf("Expected label kelos.dev/taskspawner=my-spawner, got %q", task.Labels["kelos.dev/taskspawner"])
	}
}

func TestBuildTask_EphemeralModeNoLabel(t *testing.T) {
	tb := &TaskBuilder{}

	tmpl := &v1alpha1.TaskTemplate{
		Type: "claude-code",
		Credentials: v1alpha1.Credentials{
			Type:      v1alpha1.CredentialTypeAPIKey,
			SecretRef: &v1alpha1.SecretReference{Name: "secret"},
		},
	}

	task, err := tb.BuildTask("test-task", "default", tmpl, map[string]interface{}{
		"Title": "Test",
	}, &SpawnerRef{
		Name:          "my-spawner",
		UID:           "uid-123",
		APIVersion:    "kelos.dev/v1alpha1",
		Kind:          "TaskSpawner",
		ExecutionMode: v1alpha1.ExecutionModeEphemeral,
	})
	if err != nil {
		t.Fatalf("BuildTask() returned error: %v", err)
	}

	if _, exists := task.Labels["kelos.dev/execution-mode"]; exists {
		t.Error("Expected no kelos.dev/execution-mode label for ephemeral mode")
	}
}

func TestBuildTask_NoSpawnerRef(t *testing.T) {
	tb := &TaskBuilder{}

	tmpl := &v1alpha1.TaskTemplate{
		Type: "claude-code",
		Credentials: v1alpha1.Credentials{
			Type:      v1alpha1.CredentialTypeAPIKey,
			SecretRef: &v1alpha1.SecretReference{Name: "secret"},
		},
	}

	task, err := tb.BuildTask("test-task", "default", tmpl, map[string]interface{}{
		"Title": "Test",
	}, nil)
	if err != nil {
		t.Fatalf("BuildTask() returned error: %v", err)
	}

	if _, exists := task.Labels["kelos.dev/execution-mode"]; exists {
		t.Error("Expected no kelos.dev/execution-mode label without spawner ref")
	}
	if _, exists := task.Labels["kelos.dev/taskspawner"]; exists {
		t.Error("Expected no kelos.dev/taskspawner label without spawner ref")
	}
}
