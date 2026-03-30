package webhook

import (
	"encoding/json"
	"strings"

	kelosv1alpha1 "github.com/kelos-dev/kelos/api/v1alpha1"
)

// MatchesLinearEvent evaluates whether a Linear webhook event matches
// the configured LinearWebhook filters.
func MatchesLinearEvent(config *kelosv1alpha1.LinearWebhook, payload []byte) bool {
	var event map[string]interface{}
	if err := json.Unmarshal(payload, &event); err != nil {
		return false
	}

	eventType, ok := event["type"].(string)
	if !ok {
		return false
	}

	// Check if event type is in the configured types list
	typeMatched := false
	for _, configType := range config.Types {
		if strings.EqualFold(eventType, configType) {
			typeMatched = true
			break
		}
	}
	if !typeMatched {
		return false
	}

	// If no filters configured, match any event of the configured types
	if len(config.Filters) == 0 {
		return true
	}

	// Check if any filter matches (OR semantics)
	for _, filter := range config.Filters {
		if strings.EqualFold(filter.Type, eventType) && matchesLinearFilter(filter, event) {
			return true
		}
	}

	return false
}

func matchesLinearFilter(filter kelosv1alpha1.LinearWebhookFilter, event map[string]interface{}) bool {
	// Check action filter
	if filter.Action != "" {
		action, ok := event["action"].(string)
		if !ok || action != filter.Action {
			return false
		}
	}

	// Extract data object (only needed for state/label filters)
	needsData := len(filter.States) > 0 || len(filter.Labels) > 0 || len(filter.ExcludeLabels) > 0
	if needsData {
		data, ok := event["data"].(map[string]interface{})
		if !ok {
			return false
		}
		return checkDataFilters(filter, data)
	}

	return true // Only action filter applied, and it passed
}

func checkDataFilters(filter kelosv1alpha1.LinearWebhookFilter, data map[string]interface{}) bool {
	// Check state filter
	if len(filter.States) > 0 {
		state, ok := data["state"].(map[string]interface{})
		if !ok {
			return false
		}
		stateName, ok := state["name"].(string)
		if !ok {
			return false
		}
		stateMatched := false
		for _, filterState := range filter.States {
			if stateName == filterState {
				stateMatched = true
				break
			}
		}
		if !stateMatched {
			return false
		}
	}

	// Check label filters
	if len(filter.Labels) > 0 || len(filter.ExcludeLabels) > 0 {
		labels, ok := data["labels"].(map[string]interface{})
		if !ok {
			return len(filter.Labels) == 0 // Only pass if no required labels
		}

		nodes, ok := labels["nodes"].([]interface{})
		if !ok {
			return len(filter.Labels) == 0 // Only pass if no required labels
		}

		var labelNames []string
		for _, node := range nodes {
			if nodeMap, ok := node.(map[string]interface{}); ok {
				if name, ok := nodeMap["name"].(string); ok {
					labelNames = append(labelNames, name)
				}
			}
		}

		// Check required labels
		if len(filter.Labels) > 0 && !containsAllLabels(labelNames, filter.Labels) {
			return false
		}

		// Check excluded labels
		if len(filter.ExcludeLabels) > 0 {
			for _, excludeLabel := range filter.ExcludeLabels {
				for _, labelName := range labelNames {
					if labelName == excludeLabel {
						return false
					}
				}
			}
		}
	}

	return true
}

func containsAllLabels(labelNames, requiredLabels []string) bool {
	for _, required := range requiredLabels {
		found := false
		for _, label := range labelNames {
			if label == required {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

// ExtractLinearWorkItem extracts template variables from a Linear webhook payload.
func ExtractLinearWorkItem(payload []byte) map[string]interface{} {
	var event map[string]interface{}
	if err := json.Unmarshal(payload, &event); err != nil {
		return nil
	}

	eventType, _ := event["type"].(string)
	action, _ := event["action"].(string)

	data, ok := event["data"].(map[string]interface{})
	if !ok {
		return nil
	}

	templateVars := map[string]interface{}{
		"Event":   eventType,
		"Action":  action,
		"Payload": event,
	}

	// Extract common fields
	if id, ok := data["id"].(string); ok {
		templateVars["ID"] = id
	}
	if title, ok := data["title"].(string); ok {
		templateVars["Title"] = title
	}
	if description, ok := data["description"].(string); ok {
		templateVars["Body"] = description
	}
	if url, ok := data["url"].(string); ok {
		templateVars["URL"] = url
	}

	// Extract state
	if state, ok := data["state"].(map[string]interface{}); ok {
		if stateName, ok := state["name"].(string); ok {
			templateVars["State"] = stateName
		}
	}

	// Extract labels
	if labels, ok := data["labels"].(map[string]interface{}); ok {
		if nodes, ok := labels["nodes"].([]interface{}); ok {
			var labelNames []string
			for _, node := range nodes {
				if nodeMap, ok := node.(map[string]interface{}); ok {
					if name, ok := nodeMap["name"].(string); ok {
						labelNames = append(labelNames, name)
					}
				}
			}
			templateVars["Labels"] = strings.Join(labelNames, ", ")
		}
	}

	return templateVars
}
