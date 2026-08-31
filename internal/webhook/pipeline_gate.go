package webhook

import (
	"context"
	"encoding/json"
	"fmt"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	kelos "github.com/kelos-dev/kelos/api/v1alpha2"
)

const gateSourceLabel = "kelos.dev/gate-source"

// PipelineGateClearer finds TaskPipelines with webhook gates waiting for a
// given source and clears them when the payload matches their filters.
type PipelineGateClearer struct {
	Client client.Client
}

// ClearMatchingGates finds pipelines waiting for the given webhook source,
// evaluates their filters against the payload, and clears matching gates
// via status patch. Returns the number of gates cleared.
func (c *PipelineGateClearer) ClearMatchingGates(ctx context.Context, source string, payload []byte, deliveryID string) (int, error) {
	logger := log.FromContext(ctx).WithValues("source", source, "deliveryID", deliveryID)

	// Find pipelines waiting for this source via label index.
	var pipelines kelos.TaskPipelineList
	if err := c.Client.List(ctx, &pipelines, client.MatchingLabels{
		gateSourceLabel: source,
	}); err != nil {
		return 0, fmt.Errorf("list pipelines for gate source %q: %w", source, err)
	}

	if len(pipelines.Items) == 0 {
		return 0, nil
	}

	logger.Info("Found pipelines with matching gate source", "count", len(pipelines.Items))

	cleared := 0
	for i := range pipelines.Items {
		pipeline := &pipelines.Items[i]
		if err := c.tryClearing(ctx, pipeline, source, payload, deliveryID); err != nil {
			logger.Error(err, "Failed to clear gate", "pipeline", pipeline.Name)
			continue
		}
		cleared++
	}
	return cleared, nil
}

func (c *PipelineGateClearer) tryClearing(ctx context.Context, pipeline *kelos.TaskPipeline, source string, payload []byte, deliveryID string) error {
	// Find the waiting stage with a webhook gate matching this source.
	for i := range pipeline.Status.Stages {
		stageStatus := &pipeline.Status.Stages[i]
		if stageStatus.Phase != kelos.StagePhaseWaiting {
			continue
		}

		specStage := findSpecStage(pipeline.Spec.Stages, stageStatus.Name)
		if specStage == nil || specStage.WaitFor == nil || specStage.WaitFor.Webhook == nil {
			continue
		}
		if specStage.WaitFor.Webhook.Source != source {
			continue
		}

		// Evaluate filters against payload.
		if len(specStage.WaitFor.Webhook.Filters) > 0 {
			matches, err := matchesFilters(specStage.WaitFor.Webhook.Filters, payload)
			if err != nil {
				return fmt.Errorf("evaluate filters for stage %q: %w", stageStatus.Name, err)
			}
			if !matches {
				continue
			}
		}

		// Clear the gate via status patch.
		now := metav1.Now()
		stageStatus.GateStatus = &kelos.GateStatus{
			Cleared:   true,
			ClearedAt: &now,
			ClearedBy: fmt.Sprintf("webhook:%s", deliveryID),
		}

		if err := c.Client.Status().Update(ctx, pipeline); err != nil {
			return fmt.Errorf("patch pipeline status: %w", err)
		}

		// Remove the gate source label so this pipeline is no longer discovered.
		if pipeline.Labels != nil {
			delete(pipeline.Labels, gateSourceLabel)
			if err := c.Client.Update(ctx, pipeline); err != nil {
				return fmt.Errorf("remove gate label: %w", err)
			}
		}

		return nil
	}
	return nil
}

func findSpecStage(stages []kelos.PipelineStage, name string) *kelos.PipelineStage {
	for i := range stages {
		if stages[i].Name == name {
			return &stages[i]
		}
	}
	return nil
}

// matchesFilters evaluates GenericWebhookFilter conditions against a JSON payload.
// All filters must match (AND semantics).
func matchesFilters(filters []kelos.GenericWebhookFilter, payload []byte) (bool, error) {
	var parsed map[string]interface{}
	if err := json.Unmarshal(payload, &parsed); err != nil {
		return false, fmt.Errorf("unmarshal payload: %w", err)
	}

	for _, filter := range filters {
		value, err := extractJSONPath(parsed, filter.Field)
		if err != nil {
			return false, nil
		}

		strValue := fmt.Sprintf("%v", value)

		if filter.Value != nil {
			if strValue != *filter.Value {
				return false, nil
			}
		}
		if filter.Pattern != "" {
			// TODO: Use regexp.MatchString for pattern matching.
			// For reference implementation, exact match on pattern field.
			if strValue != filter.Pattern {
				return false, nil
			}
		}
	}
	return true, nil
}

// extractJSONPath extracts a value from a parsed JSON object using a simple
// dot-notation path (e.g., "$.status" or "status.conclusion").
func extractJSONPath(obj map[string]interface{}, path string) (interface{}, error) {
	// Strip leading "$." if present.
	if len(path) > 2 && path[:2] == "$." {
		path = path[2:]
	}

	parts := splitPath(path)
	var current interface{} = obj

	for _, part := range parts {
		m, ok := current.(map[string]interface{})
		if !ok {
			return nil, fmt.Errorf("cannot traverse into non-object at %q", part)
		}
		current, ok = m[part]
		if !ok {
			return nil, fmt.Errorf("field %q not found", part)
		}
	}
	return current, nil
}

func splitPath(path string) []string {
	var parts []string
	current := ""
	for _, c := range path {
		if c == '.' {
			if current != "" {
				parts = append(parts, current)
				current = ""
			}
		} else {
			current += string(c)
		}
	}
	if current != "" {
		parts = append(parts, current)
	}
	return parts
}
