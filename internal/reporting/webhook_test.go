package reporting

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	kelosv1alpha1 "github.com/kelos-dev/kelos/api/v1alpha1"
)

type fakeSecretReader struct {
	secrets map[string]string
}

func (f *fakeSecretReader) ReadSecret(_ context.Context, namespace, name, key string) (string, error) {
	return f.secrets[namespace+"/"+name+"/"+key], nil
}

func TestWebhookReporter_ReportWebhooks(t *testing.T) {
	tests := []struct {
		name           string
		task           *kelosv1alpha1.Task
		serverStatus   int
		wantRequests   int
		wantPayload    *WebhookPayload
		wantAuthHeader string
	}{
		{
			name: "sends webhook on task succeeded",
			task: &kelosv1alpha1.Task{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-task-1",
					Namespace: "default",
					Labels:    map[string]string{"kelos.dev/taskspawner": "my-spawner"},
					Annotations: map[string]string{
						AnnotationOnCompletion: `[{"name":"slack-alert","webhook":{"url":"PLACEHOLDER"}}]`,
					},
				},
				Spec: kelosv1alpha1.TaskSpec{
					Type:  "claude-code",
					Model: "claude-sonnet-4-20250514",
				},
				Status: kelosv1alpha1.TaskStatus{
					Phase:   kelosv1alpha1.TaskPhaseSucceeded,
					Message: "Task completed successfully",
					Outputs: []string{"https://github.com/org/repo/pull/1"},
					Results: map[string]string{"cost-usd": "0.42"},
				},
			},
			serverStatus: 200,
			wantRequests: 1,
			wantPayload: &WebhookPayload{
				Task:      "test-task-1",
				Namespace: "default",
				Spawner:   "my-spawner",
				Phase:     "Succeeded",
				Message:   "Task completed successfully",
				AgentType: "claude-code",
				Model:     "claude-sonnet-4-20250514",
				Outputs:   []string{"https://github.com/org/repo/pull/1"},
				Results:   map[string]string{"cost-usd": "0.42"},
			},
		},
		{
			name: "sends webhook on task failed",
			task: &kelosv1alpha1.Task{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-task-2",
					Namespace: "default",
					Annotations: map[string]string{
						AnnotationOnCompletion: `[{"name":"alert","webhook":{"url":"PLACEHOLDER"}}]`,
					},
				},
				Spec: kelosv1alpha1.TaskSpec{Type: "claude-code"},
				Status: kelosv1alpha1.TaskStatus{
					Phase:   kelosv1alpha1.TaskPhaseFailed,
					Message: "OOM killed",
				},
			},
			serverStatus: 200,
			wantRequests: 1,
		},
		{
			name: "skips non-terminal phase",
			task: &kelosv1alpha1.Task{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-task-3",
					Namespace: "default",
					Annotations: map[string]string{
						AnnotationOnCompletion: `[{"name":"alert","webhook":{"url":"PLACEHOLDER"}}]`,
					},
				},
				Status: kelosv1alpha1.TaskStatus{Phase: kelosv1alpha1.TaskPhaseRunning},
			},
			serverStatus: 200,
			wantRequests: 0,
		},
		{
			name: "skips already reported phase",
			task: &kelosv1alpha1.Task{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-task-4",
					Namespace: "default",
					Annotations: map[string]string{
						AnnotationOnCompletion:       `[{"name":"alert","webhook":{"url":"PLACEHOLDER"}}]`,
						AnnotationWebhookReportPhase: "Succeeded",
					},
				},
				Status: kelosv1alpha1.TaskStatus{Phase: kelosv1alpha1.TaskPhaseSucceeded},
			},
			serverStatus: 200,
			wantRequests: 0,
		},
		{
			name: "filters by phase - only Failed configured but task Succeeded",
			task: &kelosv1alpha1.Task{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-task-5",
					Namespace: "default",
					Annotations: map[string]string{
						AnnotationOnCompletion: `[{"name":"alert","phases":["Failed"],"webhook":{"url":"PLACEHOLDER"}}]`,
					},
				},
				Status: kelosv1alpha1.TaskStatus{Phase: kelosv1alpha1.TaskPhaseSucceeded},
			},
			serverStatus: 200,
			wantRequests: 0,
		},
		{
			name: "includes auth header from secret",
			task: &kelosv1alpha1.Task{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-task-6",
					Namespace: "default",
					Annotations: map[string]string{
						AnnotationOnCompletion: `[{"name":"alert","webhook":{"url":"PLACEHOLDER","secretRef":{"name":"webhook-secret"}}}]`,
					},
				},
				Spec:   kelosv1alpha1.TaskSpec{Type: "claude-code"},
				Status: kelosv1alpha1.TaskStatus{Phase: kelosv1alpha1.TaskPhaseSucceeded},
			},
			serverStatus:   200,
			wantRequests:   1,
			wantAuthHeader: "Bearer my-token",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			requestCount := 0
			var lastBody []byte
			var lastAuthHeader string

			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requestCount++
				lastAuthHeader = r.Header.Get("Authorization")
				lastBody, _ = io.ReadAll(r.Body)
				w.WriteHeader(tt.serverStatus)
			}))
			defer server.Close()

			// Replace PLACEHOLDER URL in annotations with actual test server URL.
			if ann := tt.task.Annotations[AnnotationOnCompletion]; ann != "" {
				var hooks []json.RawMessage
				json.Unmarshal([]byte(ann), &hooks)
				for i := range hooks {
					var h map[string]interface{}
					json.Unmarshal(hooks[i], &h)
					if wh, ok := h["webhook"].(map[string]interface{}); ok {
						wh["url"] = server.URL
					}
					hooks[i], _ = json.Marshal(h)
				}
				updated, _ := json.Marshal(hooks)
				tt.task.Annotations[AnnotationOnCompletion] = string(updated)
			}

			secretReader := &fakeSecretReader{
				secrets: map[string]string{
					"default/webhook-secret/Authorization": "Bearer my-token",
				},
			}

			wr := &WebhookReporter{
				Client:       nil, // persist is tested separately
				HTTPClient:   server.Client(),
				SecretReader: secretReader,
			}

			// Skip persist by calling sendWebhook directly for tests that need it,
			// or test the full flow by not calling persistWebhookReportPhase.
			// For simplicity, test the core dispatch logic without k8s client.
			if tt.wantRequests == 0 {
				err := wr.ReportWebhooks(context.Background(), tt.task)
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				if requestCount != 0 {
					t.Errorf("expected 0 requests, got %d", requestCount)
				}
				return
			}

			// For tests that expect requests, call the internal method directly
			// to avoid needing a k8s client for annotation persistence.
			var hooks []kelosv1alpha1.NotificationHook
			json.Unmarshal([]byte(tt.task.Annotations[AnnotationOnCompletion]), &hooks)

			payload := buildWebhookPayload(tt.task)
			for _, hook := range hooks {
				if !phaseMatches(hook.Phases, tt.task.Status.Phase) {
					continue
				}
				err := wr.sendWebhook(context.Background(), tt.task.Namespace, hook, payload)
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
			}

			if requestCount != tt.wantRequests {
				t.Errorf("expected %d requests, got %d", tt.wantRequests, requestCount)
			}

			if tt.wantPayload != nil && lastBody != nil {
				var got WebhookPayload
				if err := json.Unmarshal(lastBody, &got); err != nil {
					t.Fatalf("unmarshal payload: %v", err)
				}
				if got.Task != tt.wantPayload.Task {
					t.Errorf("payload.Task = %q, want %q", got.Task, tt.wantPayload.Task)
				}
				if got.Phase != tt.wantPayload.Phase {
					t.Errorf("payload.Phase = %q, want %q", got.Phase, tt.wantPayload.Phase)
				}
				if got.Spawner != tt.wantPayload.Spawner {
					t.Errorf("payload.Spawner = %q, want %q", got.Spawner, tt.wantPayload.Spawner)
				}
				if got.AgentType != tt.wantPayload.AgentType {
					t.Errorf("payload.AgentType = %q, want %q", got.AgentType, tt.wantPayload.AgentType)
				}
			}

			if tt.wantAuthHeader != "" && lastAuthHeader != tt.wantAuthHeader {
				t.Errorf("Authorization header = %q, want %q", lastAuthHeader, tt.wantAuthHeader)
			}
		})
	}
}

func TestBuildWebhookPayload(t *testing.T) {
	startTime := metav1.NewTime(time.Date(2026, 3, 20, 10, 0, 0, 0, time.UTC))
	completionTime := metav1.NewTime(time.Date(2026, 3, 20, 10, 5, 30, 0, time.UTC))

	task := &kelosv1alpha1.Task{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "my-task",
			Namespace: "prod",
			Labels:    map[string]string{"kelos.dev/taskspawner": "my-spawner"},
		},
		Spec: kelosv1alpha1.TaskSpec{
			Type:  "claude-code",
			Model: "claude-sonnet-4-20250514",
		},
		Status: kelosv1alpha1.TaskStatus{
			Phase:          kelosv1alpha1.TaskPhaseSucceeded,
			Message:        "Task completed successfully",
			StartTime:      &startTime,
			CompletionTime: &completionTime,
			Outputs:        []string{"https://github.com/org/repo/pull/123"},
			Results:        map[string]string{"cost-usd": "0.42", "input-tokens": "15000"},
		},
	}

	payload := buildWebhookPayload(task)

	if payload.Task != "my-task" {
		t.Errorf("Task = %q, want %q", payload.Task, "my-task")
	}
	if payload.Namespace != "prod" {
		t.Errorf("Namespace = %q, want %q", payload.Namespace, "prod")
	}
	if payload.Spawner != "my-spawner" {
		t.Errorf("Spawner = %q, want %q", payload.Spawner, "my-spawner")
	}
	if payload.Phase != "Succeeded" {
		t.Errorf("Phase = %q, want %q", payload.Phase, "Succeeded")
	}
	if payload.StartTime == nil || !payload.StartTime.Equal(startTime.Time) {
		t.Errorf("StartTime = %v, want %v", payload.StartTime, startTime.Time)
	}
	if payload.CompletionTime == nil || !payload.CompletionTime.Equal(completionTime.Time) {
		t.Errorf("CompletionTime = %v, want %v", payload.CompletionTime, completionTime.Time)
	}
	if len(payload.Outputs) != 1 || payload.Outputs[0] != "https://github.com/org/repo/pull/123" {
		t.Errorf("Outputs = %v, want [https://github.com/org/repo/pull/123]", payload.Outputs)
	}
	if payload.Results["cost-usd"] != "0.42" {
		t.Errorf("Results[cost-usd] = %q, want %q", payload.Results["cost-usd"], "0.42")
	}
}

func TestPhaseMatches(t *testing.T) {
	tests := []struct {
		configured []kelosv1alpha1.TaskPhase
		actual     kelosv1alpha1.TaskPhase
		want       bool
	}{
		{nil, kelosv1alpha1.TaskPhaseSucceeded, true},
		{nil, kelosv1alpha1.TaskPhaseFailed, true},
		{[]kelosv1alpha1.TaskPhase{kelosv1alpha1.TaskPhaseFailed}, kelosv1alpha1.TaskPhaseFailed, true},
		{[]kelosv1alpha1.TaskPhase{kelosv1alpha1.TaskPhaseFailed}, kelosv1alpha1.TaskPhaseSucceeded, false},
		{[]kelosv1alpha1.TaskPhase{kelosv1alpha1.TaskPhaseSucceeded, kelosv1alpha1.TaskPhaseFailed}, kelosv1alpha1.TaskPhaseSucceeded, true},
	}

	for _, tt := range tests {
		got := phaseMatches(tt.configured, tt.actual)
		if got != tt.want {
			t.Errorf("phaseMatches(%v, %v) = %v, want %v", tt.configured, tt.actual, got, tt.want)
		}
	}
}
