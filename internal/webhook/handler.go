package webhook

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/go-logr/logr"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kelosv1alpha1 "github.com/kelos-dev/kelos/api/v1alpha1"
	"github.com/kelos-dev/kelos/internal/taskbuilder"
)

// Handler processes incoming webhook requests and creates Tasks for matching TaskSpawners.
type Handler struct {
	Client client.Client
	Log    logr.Logger
	Source string // "github" or "linear"
	Secret []byte
}

// ServeHTTP handles webhook POST requests.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	body, err := io.ReadAll(r.Body)
	if err != nil {
		h.Log.Error(err, "Reading request body")
		http.Error(w, "Failed to read body", http.StatusBadRequest)
		return
	}
	defer r.Body.Close()

	// Validate signature
	if err := h.validateSignature(body, r.Header); err != nil {
		h.Log.Error(err, "Signature validation failed")
		http.Error(w, "Signature validation failed", http.StatusUnauthorized)
		return
	}

	ctx := r.Context()

	// List all TaskSpawners
	var spawnerList kelosv1alpha1.TaskSpawnerList
	if err := h.Client.List(ctx, &spawnerList); err != nil {
		h.Log.Error(err, "Listing TaskSpawners")
		http.Error(w, "Internal error", http.StatusInternalServerError)
		return
	}

	// Extract delivery ID for idempotency
	deliveryID := h.extractDeliveryID(r.Header)

	var matched int
	var created int
	var atCapacity bool

	for i := range spawnerList.Items {
		ts := &spawnerList.Items[i]

		// Skip suspended spawners
		if ts.Spec.Suspend != nil && *ts.Spec.Suspend {
			continue
		}

		// Check if this spawner matches the incoming event
		if !h.matchesSpawner(ts, r.Header, body) {
			continue
		}
		matched++

		// Idempotency: check if a Task with this delivery ID already exists
		if deliveryID != "" {
			existingTasks := &kelosv1alpha1.TaskList{}
			if err := h.Client.List(ctx, existingTasks,
				client.InNamespace(ts.Namespace),
				client.MatchingLabels{"kelos.dev/taskspawner": ts.Name},
			); err == nil {
				duplicate := false
				for _, t := range existingTasks.Items {
					if t.Annotations["kelos.dev/webhook-delivery"] == deliveryID {
						duplicate = true
						break
					}
				}
				if duplicate {
					h.Log.Info("Duplicate delivery, skipping", "spawner", ts.Name, "deliveryID", deliveryID)
					continue
				}
			}
		}

		// Check maxConcurrency
		if ts.Spec.MaxConcurrency != nil && *ts.Spec.MaxConcurrency > 0 {
			activeTasks, err := h.countActiveTasks(ctx, ts)
			if err != nil {
				h.Log.Error(err, "Counting active tasks", "spawner", ts.Name)
				continue
			}
			if activeTasks >= int(*ts.Spec.MaxConcurrency) {
				h.Log.Info("Max concurrency reached", "spawner", ts.Name, "active", activeTasks, "max", *ts.Spec.MaxConcurrency)
				atCapacity = true
				continue
			}
		}

		// Check maxTotalTasks
		if ts.Spec.MaxTotalTasks != nil && *ts.Spec.MaxTotalTasks > 0 {
			if ts.Status.TotalTasksCreated >= int(*ts.Spec.MaxTotalTasks) {
				h.Log.Info("Task budget exhausted", "spawner", ts.Name)
				continue
			}
		}

		// Create the Task
		if err := h.createTask(ctx, ts, r.Header, body, deliveryID); err != nil {
			h.Log.Error(err, "Creating task", "spawner", ts.Name)
			continue
		}
		created++
	}

	h.Log.Info("Webhook processed", "source", h.Source, "matched", matched, "created", created)

	if atCapacity && created == 0 && matched > 0 {
		w.Header().Set("Retry-After", "30")
		http.Error(w, "All matching spawners at max concurrency", http.StatusServiceUnavailable)
		return
	}

	w.WriteHeader(http.StatusOK)
	fmt.Fprintf(w, `{"matched":%d,"created":%d}`, matched, created)
}

func (h *Handler) validateSignature(body []byte, headers http.Header) error {
	if len(h.Secret) == 0 {
		return nil
	}

	switch h.Source {
	case "github":
		sig := headers.Get("X-Hub-Signature-256")
		if sig == "" {
			return fmt.Errorf("missing X-Hub-Signature-256 header")
		}
		return ValidateGitHubSignature(body, sig, h.Secret)
	case "linear":
		sig := headers.Get("Linear-Signature")
		if sig == "" {
			return fmt.Errorf("missing Linear-Signature header")
		}
		return ValidateLinearSignature(body, sig, h.Secret)
	default:
		return fmt.Errorf("unknown source type: %s", h.Source)
	}
}

func (h *Handler) extractDeliveryID(headers http.Header) string {
	switch h.Source {
	case "github":
		return headers.Get("X-GitHub-Delivery")
	case "linear":
		return headers.Get("Linear-Delivery")
	default:
		return headers.Get("X-Request-ID")
	}
}

func (h *Handler) matchesSpawner(ts *kelosv1alpha1.TaskSpawner, headers http.Header, body []byte) bool {
	switch h.Source {
	case "github":
		if ts.Spec.When.GitHubWebhook == nil {
			return false
		}
		eventType := headers.Get("X-GitHub-Event")
		return MatchesGitHubEvent(ts.Spec.When.GitHubWebhook, eventType, body)
	case "linear":
		if ts.Spec.When.LinearWebhook == nil {
			return false
		}
		return MatchesLinearEvent(ts.Spec.When.LinearWebhook, body)
	default:
		return false
	}
}

func (h *Handler) countActiveTasks(ctx context.Context, ts *kelosv1alpha1.TaskSpawner) (int, error) {
	var taskList kelosv1alpha1.TaskList
	if err := h.Client.List(ctx, &taskList,
		client.InNamespace(ts.Namespace),
		client.MatchingLabels{"kelos.dev/taskspawner": ts.Name},
	); err != nil {
		return 0, err
	}

	active := 0
	for _, t := range taskList.Items {
		if t.Status.Phase != kelosv1alpha1.TaskPhaseSucceeded && t.Status.Phase != kelosv1alpha1.TaskPhaseFailed {
			active++
		}
	}
	return active, nil
}

func (h *Handler) createTask(ctx context.Context, ts *kelosv1alpha1.TaskSpawner, headers http.Header, body []byte, deliveryID string) error {
	// Extract work item data for template rendering
	var templateVars map[string]interface{}
	var eventType string

	switch h.Source {
	case "github":
		eventType = headers.Get("X-GitHub-Event")
		templateVars = ExtractGitHubWorkItem(eventType, body)
	case "linear":
		eventType = "linear_webhook" // Linear doesn't have event type headers
		templateVars = ExtractLinearWorkItem(body)
	}

	if templateVars == nil {
		return fmt.Errorf("failed to extract work item data from payload")
	}

	// Generate a unique ID for this webhook event
	id := sanitizeTaskName(deliveryID)
	if id == "" {
		id = fmt.Sprintf("%d", metav1.Now().UnixNano())
	}

	// Use TaskBuilder to create the task
	builder := taskbuilder.NewTaskBuilder(ts)
	task, err := builder.BuildTaskFromWebhook(id, templateVars, deliveryID, h.Source, eventType)
	if err != nil {
		return fmt.Errorf("building task: %w", err)
	}

	return h.Client.Create(ctx, task)
}

// sanitizeTaskName converts a delivery ID into a valid Kubernetes name suffix.
// Kubernetes names must be lowercase alphanumeric, '-', or '.', max 253 chars.
func sanitizeTaskName(id string) string {
	if id == "" {
		return ""
	}
	id = strings.ToLower(id)
	// Keep only alphanumeric and hyphens
	var b strings.Builder
	for _, c := range id {
		if (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '-' {
			b.WriteRune(c)
		}
	}
	result := b.String()
	// Trim to keep total task name under 253 chars (spawner name + "-" + this)
	if len(result) > 36 {
		result = result[:36]
	}
	return strings.TrimRight(result, "-")
}

// HealthHandler returns a simple health check handler.
func HealthHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, "ok")
	})
}

// ReadyHandler returns a readiness check handler that verifies the client can reach the API server.
func ReadyHandler(cl client.Client) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var list kelosv1alpha1.TaskSpawnerList
		if err := cl.List(r.Context(), &list, client.Limit(1)); err != nil {
			http.Error(w, "not ready", http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, "ok")
	})
}
