package reporting

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"k8s.io/client-go/util/retry"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kelosv1alpha1 "github.com/kelos-dev/kelos/api/v1alpha1"
)

const (
	// AnnotationOnCompletion stores the serialized onCompletion hooks config
	// so the reporting loop can dispatch without looking up the TaskSpawner.
	AnnotationOnCompletion = "kelos.dev/on-completion"

	// AnnotationWebhookReportPhase records the last Task phase that was
	// reported via webhook hooks, preventing duplicate deliveries.
	AnnotationWebhookReportPhase = "kelos.dev/webhook-report-phase"
)

// WebhookPayload is the JSON body sent to onCompletion webhook endpoints.
type WebhookPayload struct {
	Task           string            `json:"task"`
	Namespace      string            `json:"namespace"`
	Spawner        string            `json:"spawner,omitempty"`
	Phase          string            `json:"phase"`
	Message        string            `json:"message,omitempty"`
	AgentType      string            `json:"agentType,omitempty"`
	Model          string            `json:"model,omitempty"`
	StartTime      *time.Time        `json:"startTime,omitempty"`
	CompletionTime *time.Time        `json:"completionTime,omitempty"`
	Outputs        []string          `json:"outputs,omitempty"`
	Results        map[string]string `json:"results,omitempty"`
}

// WebhookReporter dispatches onCompletion webhook notifications for Tasks.
type WebhookReporter struct {
	Client     client.Client
	HTTPClient *http.Client
	// SecretReader resolves secret values. When nil, secretRef is ignored.
	SecretReader SecretReader
}

// SecretReader reads a key from a named Secret in a namespace.
type SecretReader interface {
	ReadSecret(ctx context.Context, namespace, name, key string) (string, error)
}

// ReportWebhooks checks whether the task has onCompletion hooks configured
// and dispatches webhooks for terminal phases that haven't been reported yet.
func (wr *WebhookReporter) ReportWebhooks(ctx context.Context, task *kelosv1alpha1.Task) error {
	log := ctrl.Log.WithName("webhook-reporter")

	annotations := task.Annotations
	if annotations == nil {
		return nil
	}

	hooksJSON := annotations[AnnotationOnCompletion]
	if hooksJSON == "" {
		return nil
	}

	// Only fire for terminal phases.
	if task.Status.Phase != kelosv1alpha1.TaskPhaseSucceeded && task.Status.Phase != kelosv1alpha1.TaskPhaseFailed {
		return nil
	}

	// Skip if already reported this phase.
	if annotations[AnnotationWebhookReportPhase] == string(task.Status.Phase) {
		return nil
	}

	var hooks []kelosv1alpha1.NotificationHook
	if err := json.Unmarshal([]byte(hooksJSON), &hooks); err != nil {
		return fmt.Errorf("parsing on-completion hooks annotation: %w", err)
	}

	payload := buildWebhookPayload(task)

	var lastErr error
	dispatched := 0
	for _, hook := range hooks {
		if !phaseMatches(hook.Phases, task.Status.Phase) {
			continue
		}

		dispatched++
		if err := wr.sendWebhook(ctx, task.Namespace, hook, payload); err != nil {
			log.Error(err, "Sending webhook", "task", task.Name, "hook", hook.Name)
			lastErr = err
			continue
		}
		log.Info("Sent webhook notification", "task", task.Name, "hook", hook.Name, "phase", task.Status.Phase)
	}

	if dispatched == 0 {
		return nil
	}

	// Only persist the reported phase if all hooks succeeded.
	if lastErr != nil {
		return lastErr
	}

	return wr.persistWebhookReportPhase(ctx, task, string(task.Status.Phase))
}

func (wr *WebhookReporter) sendWebhook(ctx context.Context, namespace string, hook kelosv1alpha1.NotificationHook, payload WebhookPayload) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshaling webhook payload: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, hook.Webhook.URL, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("creating webhook request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	if hook.Webhook.SecretRef != nil && wr.SecretReader != nil {
		authValue, err := wr.SecretReader.ReadSecret(ctx, namespace, hook.Webhook.SecretRef.Name, "Authorization")
		if err != nil {
			return fmt.Errorf("reading webhook secret %q: %w", hook.Webhook.SecretRef.Name, err)
		}
		if authValue != "" {
			req.Header.Set("Authorization", authValue)
		}
	}

	httpClient := wr.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 10 * time.Second}
	}

	resp, err := httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("sending webhook to %s: %w", hook.Webhook.URL, err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)

	if resp.StatusCode >= 300 {
		return fmt.Errorf("webhook %s returned status %d", hook.Webhook.URL, resp.StatusCode)
	}

	return nil
}

func (wr *WebhookReporter) persistWebhookReportPhase(ctx context.Context, task *kelosv1alpha1.Task, phase string) error {
	return persistAnnotationRetry(ctx, wr.Client, task, map[string]string{
		AnnotationWebhookReportPhase: phase,
	})
}

func buildWebhookPayload(task *kelosv1alpha1.Task) WebhookPayload {
	p := WebhookPayload{
		Task:      task.Name,
		Namespace: task.Namespace,
		Spawner:   task.Labels["kelos.dev/taskspawner"],
		Phase:     string(task.Status.Phase),
		Message:   task.Status.Message,
		AgentType: task.Spec.Type,
		Model:     task.Spec.Model,
		Outputs:   task.Status.Outputs,
		Results:   task.Status.Results,
	}
	if task.Status.StartTime != nil {
		t := task.Status.StartTime.Time
		p.StartTime = &t
	}
	if task.Status.CompletionTime != nil {
		t := task.Status.CompletionTime.Time
		p.CompletionTime = &t
	}
	return p
}

func phaseMatches(configured []kelosv1alpha1.TerminalTaskPhase, actual kelosv1alpha1.TaskPhase) bool {
	if len(configured) == 0 {
		return true
	}
	for _, p := range configured {
		if kelosv1alpha1.TaskPhase(p) == actual {
			return true
		}
	}
	return false
}

// persistAnnotationRetry updates annotations on a Task with retry on conflict.
func persistAnnotationRetry(ctx context.Context, cl client.Client, task *kelosv1alpha1.Task, annotations map[string]string) error {
	if err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		var current kelosv1alpha1.Task
		if err := cl.Get(ctx, client.ObjectKeyFromObject(task), &current); err != nil {
			return err
		}
		if current.Annotations == nil {
			current.Annotations = make(map[string]string)
		}
		for k, v := range annotations {
			current.Annotations[k] = v
		}
		if err := cl.Update(ctx, &current); err != nil {
			return err
		}
		task.Annotations = current.Annotations
		return nil
	}); err != nil {
		return fmt.Errorf("persisting webhook annotations on task %s: %w", task.Name, err)
	}
	return nil
}
