package webhook

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/go-logr/logr"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/kelos-dev/kelos/api/v1alpha1"
	"github.com/kelos-dev/kelos/internal/taskbuilder"
)

// WebhookSource represents the type of webhook source.
type WebhookSource string

const (
	GitHubSource WebhookSource = "github"
	LinearSource WebhookSource = "linear"

	// GitHub webhook headers
	GitHubEventHeader     = "X-GitHub-Event"
	GitHubSignatureHeader = "X-Hub-Signature-256"
	GitHubDeliveryHeader  = "X-GitHub-Delivery"

	// Linear webhook headers
	LinearSignatureHeader = "Linear-Signature"
	LinearDeliveryHeader  = "Linear-Delivery"
)

// WebhookHandler handles webhook requests for a specific source type.
type WebhookHandler struct {
	client        client.Client
	source        WebhookSource
	log           logr.Logger
	taskBuilder   *taskbuilder.TaskBuilder
	secret        []byte
	deliveryCache *DeliveryCache
}

// DeliveryCache tracks processed webhook deliveries for idempotency.
type DeliveryCache struct {
	mu    sync.RWMutex
	cache map[string]time.Time
}

// NewDeliveryCache creates a new delivery cache with cleanup.
func NewDeliveryCache() *DeliveryCache {
	cache := &DeliveryCache{
		cache: make(map[string]time.Time),
	}

	// Clean up expired entries every hour
	go func() {
		ticker := time.NewTicker(time.Hour)
		defer ticker.Stop()

		for range ticker.C {
			cache.cleanup()
		}
	}()

	return cache
}

// IsProcessed checks if a delivery ID has already been processed.
func (d *DeliveryCache) IsProcessed(deliveryID string) bool {
	d.mu.RLock()
	defer d.mu.RUnlock()
	_, exists := d.cache[deliveryID]
	return exists
}

// MarkProcessed marks a delivery ID as processed.
func (d *DeliveryCache) MarkProcessed(deliveryID string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.cache[deliveryID] = time.Now()
}

// cleanup removes entries older than 24 hours.
func (d *DeliveryCache) cleanup() {
	d.mu.Lock()
	defer d.mu.Unlock()

	cutoff := time.Now().Add(-24 * time.Hour)
	for id, timestamp := range d.cache {
		if timestamp.Before(cutoff) {
			delete(d.cache, id)
		}
	}
}

// NewWebhookHandler creates a new webhook handler for the specified source.
func NewWebhookHandler(client client.Client, source WebhookSource, log logr.Logger) (*WebhookHandler, error) {
	secret := []byte(os.Getenv("WEBHOOK_SECRET"))
	if len(secret) == 0 {
		return nil, fmt.Errorf("WEBHOOK_SECRET environment variable is required")
	}

	taskBuilder, err := taskbuilder.NewTaskBuilder(client)
	if err != nil {
		return nil, fmt.Errorf("failed to create task builder: %w", err)
	}

	return &WebhookHandler{
		client:        client,
		source:        source,
		log:           log,
		taskBuilder:   taskBuilder,
		secret:        secret,
		deliveryCache: NewDeliveryCache(),
	}, nil
}

// ServeHTTP handles webhook HTTP requests.
func (h *WebhookHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	log := h.log.WithValues("method", r.Method, "path", r.URL.Path, "source", h.source)

	// Only accept POST requests
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Read the payload
	body, err := io.ReadAll(r.Body)
	if err != nil {
		log.Error(err, "Failed to read request body")
		http.Error(w, "Bad request", http.StatusBadRequest)
		return
	}

	// Extract headers and validate signature
	var eventType, signature, deliveryID string

	switch h.source {
	case GitHubSource:
		eventType = r.Header.Get(GitHubEventHeader)
		signature = r.Header.Get(GitHubSignatureHeader)
		deliveryID = r.Header.Get(GitHubDeliveryHeader)

		if err := ValidateGitHubSignature(body, signature, h.secret); err != nil {
			log.Error(err, "GitHub signature validation failed")
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}

	case LinearSource:
		signature = r.Header.Get(LinearSignatureHeader)
		deliveryID = r.Header.Get(LinearDeliveryHeader)
		eventType = "linear" // Linear doesn't send event type in header

		if err := ValidateLinearSignature(body, signature, h.secret); err != nil {
			log.Error(err, "Linear signature validation failed")
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}

	default:
		log.Error(fmt.Errorf("unsupported source: %s", h.source), "Unsupported webhook source")
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}

	// Check for duplicate delivery
	if deliveryID != "" && h.deliveryCache.IsProcessed(deliveryID) {
		log.Info("Webhook delivery already processed", "deliveryID", deliveryID)
		w.WriteHeader(http.StatusOK)
		return
	}

	// Process the webhook
	processed, err := h.processWebhook(ctx, eventType, body, deliveryID)
	if err != nil {
		log.Error(err, "Failed to process webhook")
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}

	// Mark as processed if successful
	if processed && deliveryID != "" {
		h.deliveryCache.MarkProcessed(deliveryID)
	}

	w.WriteHeader(http.StatusOK)
}

// processWebhook processes a validated webhook payload.
func (h *WebhookHandler) processWebhook(ctx context.Context, eventType string, payload []byte, deliveryID string) (bool, error) {
	log := h.log.WithValues("eventType", eventType, "deliveryID", deliveryID)

	// Get all TaskSpawners that match this source type
	spawners, err := h.getMatchingSpawners(ctx)
	if err != nil {
		return false, fmt.Errorf("failed to get matching spawners: %w", err)
	}

	if len(spawners) == 0 {
		log.V(1).Info("No matching TaskSpawners found")
		return true, nil // Not an error, just nothing to do
	}

	tasksCreated := 0

	for _, spawner := range spawners {
		spawnerLog := log.WithValues("spawner", spawner.Name, "namespace", spawner.Namespace)

		// Check if spawner is suspended
		if spawner.Spec.Suspend != nil && *spawner.Spec.Suspend {
			spawnerLog.V(1).Info("Skipping suspended spawner")
			continue
		}

		// Check max concurrency
		if spawner.Spec.MaxConcurrency != nil && *spawner.Spec.MaxConcurrency > 0 {
			activeTasks := spawner.Status.ActiveTasks
			if int32(activeTasks) >= *spawner.Spec.MaxConcurrency {
				spawnerLog.Info("Max concurrency reached, returning 503",
					"activeTasks", activeTasks,
					"maxConcurrency", *spawner.Spec.MaxConcurrency)

				// Return 503 with Retry-After header for this spawner
				// Note: This approach returns 503 for any spawner that hits limits,
				// which may not be ideal if other spawners could still process it.
				// A more sophisticated approach would track per-spawner limits.
				return false, &MaxConcurrencyError{
					RetryAfter: 60, // Suggest retry after 60 seconds
				}
			}
		}

		// Check if this webhook matches the spawner's filters
		matches, err := h.matchesSpawner(spawner, eventType, payload)
		if err != nil {
			spawnerLog.Error(err, "Failed to check spawner match")
			continue
		}

		if !matches {
			spawnerLog.V(1).Info("Webhook does not match spawner filters")
			continue
		}

		// Create task for this spawner
		err = h.createTask(ctx, spawner, eventType, payload)
		if err != nil {
			spawnerLog.Error(err, "Failed to create task")
			continue
		}

		tasksCreated++
		spawnerLog.Info("Created task from webhook")
	}

	log.Info("Webhook processing completed", "tasksCreated", tasksCreated)
	return tasksCreated > 0, nil
}

// MaxConcurrencyError represents an error when max concurrency is exceeded.
type MaxConcurrencyError struct {
	RetryAfter int
}

func (e *MaxConcurrencyError) Error() string {
	return fmt.Sprintf("max concurrency exceeded, retry after %d seconds", e.RetryAfter)
}

// getMatchingSpawners returns TaskSpawners that match the webhook source.
func (h *WebhookHandler) getMatchingSpawners(ctx context.Context) ([]*v1alpha1.TaskSpawner, error) {
	var spawnerList v1alpha1.TaskSpawnerList
	if err := h.client.List(ctx, &spawnerList, &client.ListOptions{}); err != nil {
		return nil, err
	}

	var matching []*v1alpha1.TaskSpawner
	for i := range spawnerList.Items {
		spawner := &spawnerList.Items[i]

		switch h.source {
		case GitHubSource:
			if spawner.Spec.When.GitHubWebhook != nil {
				matching = append(matching, spawner)
			}
		case LinearSource:
			if spawner.Spec.When.LinearWebhook != nil {
				matching = append(matching, spawner)
			}
		}
	}

	return matching, nil
}

// matchesSpawner checks if the webhook matches the spawner's configuration.
func (h *WebhookHandler) matchesSpawner(spawner *v1alpha1.TaskSpawner, eventType string, payload []byte) (bool, error) {
	switch h.source {
	case GitHubSource:
		if spawner.Spec.When.GitHubWebhook == nil {
			return false, nil
		}
		return MatchesGitHubEvent(spawner.Spec.When.GitHubWebhook, eventType, payload)

	case LinearSource:
		if spawner.Spec.When.LinearWebhook == nil {
			return false, nil
		}
		return MatchesLinearEvent(spawner.Spec.When.LinearWebhook, payload)

	default:
		return false, fmt.Errorf("unsupported source: %s", h.source)
	}
}

// createTask creates a new Task from the webhook event.
func (h *WebhookHandler) createTask(ctx context.Context, spawner *v1alpha1.TaskSpawner, eventType string, payload []byte) error {
	// Extract template variables based on source
	var templateVars map[string]interface{}
	var err error

	switch h.source {
	case GitHubSource:
		eventData, parseErr := ParseGitHubWebhook(eventType, payload)
		if parseErr != nil {
			return fmt.Errorf("failed to parse GitHub webhook: %w", parseErr)
		}
		templateVars = ExtractGitHubWorkItem(eventData)

	case LinearSource:
		eventData, parseErr := ParseLinearWebhook(payload)
		if parseErr != nil {
			return fmt.Errorf("failed to parse Linear webhook: %w", parseErr)
		}
		templateVars = ExtractLinearWorkItem(eventData)

	default:
		return fmt.Errorf("unsupported source: %s", h.source)
	}

	// Build unique task name from delivery info
	taskName := fmt.Sprintf("%s-%s-%s", spawner.Name, eventType, templateVars["ID"])
	if len(taskName) > 63 {
		// Kubernetes name length limit
		taskName = taskName[:63]
	}

	// Create the task
	task, err := h.taskBuilder.BuildTask(
		taskName,
		spawner.Namespace,
		&spawner.Spec.TaskTemplate,
		templateVars,
	)
	if err != nil {
		return fmt.Errorf("failed to build task: %w", err)
	}

	// Add webhook-specific annotations
	if task.Annotations == nil {
		task.Annotations = make(map[string]string)
	}
	task.Annotations["kelos.dev/source-kind"] = "webhook"
	task.Annotations["kelos.dev/source-event"] = eventType
	task.Annotations["kelos.dev/source-action"] = fmt.Sprintf("%v", templateVars["Action"])
	task.Annotations["kelos.dev/taskspawner"] = spawner.Name

	// Set owner reference
	task.OwnerReferences = []metav1.OwnerReference{
		{
			APIVersion: spawner.APIVersion,
			Kind:       spawner.Kind,
			Name:       spawner.Name,
			UID:        spawner.UID,
		},
	}

	if err := h.client.Create(ctx, task); err != nil {
		return fmt.Errorf("failed to create task: %w", err)
	}

	return nil
}
