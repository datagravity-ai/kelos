package webhook

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/kelos-dev/kelos/api/v1alpha1"
)

// LinearEventData represents parsed Linear webhook data.
type LinearEventData struct {
	ID      string                 `json:"id"`
	Title   string                 `json:"title"`
	Type    string                 `json:"type"`
	Action  string                 `json:"action"`
	Payload map[string]interface{} `json:"payload"`
	Labels  []string               `json:"labels"`
	State   string                 `json:"state"`
}

// ParseLinearWebhook parses a Linear webhook payload.
func ParseLinearWebhook(payload []byte) (*LinearEventData, error) {
	var rawPayload map[string]interface{}
	if err := json.Unmarshal(payload, &rawPayload); err != nil {
		return nil, fmt.Errorf("invalid JSON payload: %w", err)
	}

	eventData := &LinearEventData{
		Payload: rawPayload,
	}

	// Extract type from the payload
	if typeVal, ok := rawPayload["type"].(string); ok {
		eventData.Type = typeVal
	}

	// Extract action from the payload
	if actionVal, ok := rawPayload["action"].(string); ok {
		eventData.Action = actionVal
	}

	// Extract data from nested structure
	if data, ok := rawPayload["data"].(map[string]interface{}); ok {
		// Extract ID
		if id, ok := data["id"].(string); ok {
			eventData.ID = id
		}

		// Extract title
		if title, ok := data["title"].(string); ok {
			eventData.Title = title
		}

		// Extract state
		if state, ok := data["state"].(map[string]interface{}); ok {
			if stateName, ok := state["name"].(string); ok {
				eventData.State = stateName
			}
		}

		// Extract labels
		if labels, ok := data["labels"].([]interface{}); ok {
			for _, label := range labels {
				if labelMap, ok := label.(map[string]interface{}); ok {
					if labelName, ok := labelMap["name"].(string); ok {
						eventData.Labels = append(eventData.Labels, labelName)
					}
				}
			}
		}
	}

	return eventData, nil
}

// MatchesLinearEvent checks if a Linear webhook event matches the given configuration.
func MatchesLinearEvent(config *v1alpha1.LinearWebhook, payload []byte) (bool, error) {
	eventData, err := ParseLinearWebhook(payload)
	if err != nil {
		return false, err
	}

	// Check if event type is in the allowed list
	typeMatched := false
	for _, allowedType := range config.Types {
		if strings.EqualFold(eventData.Type, allowedType) {
			typeMatched = true
			break
		}
	}
	if !typeMatched {
		return false, nil
	}

	// If no filters specified, match all events of allowed types
	if len(config.Filters) == 0 {
		return true, nil
	}

	// Check filters (OR semantics - any matching filter passes)
	for _, filter := range config.Filters {
		if matchesLinearFilter(&filter, eventData) {
			return true, nil
		}
	}

	return false, nil
}

// matchesLinearFilter checks if event data matches a specific filter.
func matchesLinearFilter(filter *v1alpha1.LinearWebhookFilter, eventData *LinearEventData) bool {
	// Check type match
	if !strings.EqualFold(filter.Type, eventData.Type) {
		return false
	}

	// Check action filter
	if filter.Action != "" && !strings.EqualFold(filter.Action, eventData.Action) {
		return false
	}

	// Check state filter
	if len(filter.States) > 0 {
		stateMatched := false
		for _, allowedState := range filter.States {
			if strings.EqualFold(allowedState, eventData.State) {
				stateMatched = true
				break
			}
		}
		if !stateMatched {
			return false
		}
	}

	// Check required labels (AND semantics - must have all)
	if len(filter.Labels) > 0 {
		for _, requiredLabel := range filter.Labels {
			found := false
			for _, eventLabel := range eventData.Labels {
				if strings.EqualFold(requiredLabel, eventLabel) {
					found = true
					break
				}
			}
			if !found {
				return false // Missing required label
			}
		}
	}

	// Check excluded labels (OR semantics - any exclude label fails)
	if len(filter.ExcludeLabels) > 0 {
		for _, excludeLabel := range filter.ExcludeLabels {
			for _, eventLabel := range eventData.Labels {
				if strings.EqualFold(excludeLabel, eventLabel) {
					return false // Has excluded label
				}
			}
		}
	}

	return true
}

// ExtractLinearWorkItem converts Linear webhook data to template variables.
func ExtractLinearWorkItem(eventData *LinearEventData) map[string]interface{} {
	return map[string]interface{}{
		"ID":      eventData.ID,
		"Title":   eventData.Title,
		"Kind":    "LinearWebhook",
		"Type":    eventData.Type,
		"Action":  eventData.Action,
		"State":   eventData.State,
		"Labels":  strings.Join(eventData.Labels, ", "),
		"Payload": eventData.Payload,
	}
}
