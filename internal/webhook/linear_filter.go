package webhook

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/go-logr/logr"
	"github.com/kelos-dev/kelos/api/v1alpha1"
)

// LinearEventData holds parsed Linear event information for template rendering.
type LinearEventData struct {
	// Type (e.g., "Issue", "Comment", "Project")
	Type string
	// Action (e.g., "create", "update", "remove")
	Action string
	// Parsed event payload for template access
	Payload map[string]interface{}
	// Standard template variables
	ID    string
	Title string
	// Extracted convenience fields
	State  string
	Labels []string
}

// ParseLinearWebhook parses a Linear webhook payload using manual JSON parsing.
func ParseLinearWebhook(payload []byte) (*LinearEventData, error) {
	var raw map[string]interface{}
	if err := json.Unmarshal(payload, &raw); err != nil {
		return nil, fmt.Errorf("failed to parse Linear webhook JSON: %w", err)
	}

	data := &LinearEventData{
		Payload: raw,
	}

	// Extract type from payload
	if typ, ok := raw["type"].(string); ok {
		data.Type = typ
	}

	// Extract action from payload
	if action, ok := raw["action"].(string); ok {
		data.Action = action
	}

	// Extract data object for further processing
	var dataObj map[string]interface{}
	if d, ok := raw["data"].(map[string]interface{}); ok {
		dataObj = d
	}

	// Extract common fields based on type
	if dataObj != nil {
		// Extract ID (could be string or number)
		if id, ok := dataObj["id"].(string); ok {
			data.ID = id
		} else if id, ok := dataObj["id"].(float64); ok {
			data.ID = fmt.Sprintf("%.0f", id)
		}

		// Extract title
		if title, ok := dataObj["title"].(string); ok {
			data.Title = title
		}

		// Extract state
		if state, ok := dataObj["state"].(map[string]interface{}); ok {
			if stateName, ok := state["name"].(string); ok {
				data.State = stateName
			}
		}

		// Extract labels
		if labels, ok := dataObj["labels"].([]interface{}); ok {
			for _, label := range labels {
				if labelMap, ok := label.(map[string]interface{}); ok {
					if labelName, ok := labelMap["name"].(string); ok {
						data.Labels = append(data.Labels, labelName)
					}
				}
			}
		}
	}

	return data, nil
}

// MatchesLinearEvent evaluates whether a Linear webhook event matches the spawner's filters.
func MatchesLinearEvent(config *v1alpha1.LinearWebhook, eventData *LinearEventData) (bool, error) {
	// Check if event type is in the allowed list
	typeMatched := false
	for _, allowedType := range config.Types {
		if strings.EqualFold(allowedType, eventData.Type) {
			typeMatched = true
			break
		}
	}
	if !typeMatched {
		return false, nil
	}

	// If no filters, all events of the allowed types match
	if len(config.Filters) == 0 {
		return true, nil
	}

	// Apply filters with OR semantics for the same event type
	for _, filter := range config.Filters {
		if filter.Type != eventData.Type {
			continue
		}

		if matchesLinearFilter(filter, eventData) {
			return true, nil
		}
	}

	return false, nil
}

// extractLabels extracts labels from a data object, checking both data.labels
// and data.issue.labels (for Comment webhooks).
// Returns nil if labels are missing or empty in both locations.
func extractLabels(dataObj map[string]interface{}) []interface{} {
	// Try data.labels first (Issue events) — only if non-empty
	if labels, ok := dataObj["labels"].([]interface{}); ok && labels != nil && len(labels) > 0 {
		return labels
	}

	// Fall back to data.issue.labels (Comment and IssueLabel events) — only if non-empty
	if issue, ok := dataObj["issue"].(map[string]interface{}); ok {
		if labels, ok := issue["labels"].([]interface{}); ok && labels != nil && len(labels) > 0 {
			return labels
		}
	}

	return nil
}

// matchesLinearFilter checks if event data matches a specific Linear filter.
func matchesLinearFilter(filter v1alpha1.LinearWebhookFilter, eventData *LinearEventData) bool {
	// Action filter
	if filter.Action != "" && filter.Action != eventData.Action {
		return false
	}

	// Get data object for further filtering
	var dataObj map[string]interface{}
	if d, ok := eventData.Payload["data"].(map[string]interface{}); ok {
		dataObj = d
	}

	if dataObj == nil {
		// If no data object and we have state/label filters, this doesn't match
		if len(filter.States) > 0 || len(filter.Labels) > 0 || len(filter.ExcludeLabels) > 0 {
			return false
		}
		// Otherwise, it matches (only action filter matters)
		return true
	}

	// State filter
	if len(filter.States) > 0 {
		if state, ok := dataObj["state"].(map[string]interface{}); ok {
			if stateName, ok := state["name"].(string); ok {
				stateMatches := false
				for _, allowedState := range filter.States {
					if allowedState == stateName {
						stateMatches = true
						break
					}
				}
				if !stateMatches {
					return false
				}
			} else {
				// No state name found, but state filter required
				return false
			}
		} else {
			// No state object found, but state filter required
			return false
		}
	}

	// Labels filter (all required labels must be present)
	if len(filter.Labels) > 0 {
		labels := extractLabels(dataObj)
		if labels == nil {
			// No labels found, but labels filter required
			return false
		}

		// Build set of present label names
		presentLabels := make(map[string]bool)
		for _, label := range labels {
			if labelObj, ok := label.(map[string]interface{}); ok {
				if labelName, ok := labelObj["name"].(string); ok {
					presentLabels[labelName] = true
				}
			}
		}

		// Check all required labels are present
		for _, requiredLabel := range filter.Labels {
			if !presentLabels[requiredLabel] {
				return false
			}
		}
	}

	// ExcludeLabels filter (issue must NOT have any of these labels)
	if len(filter.ExcludeLabels) > 0 {
		labels := extractLabels(dataObj)
		if labels != nil {
			// Build set of present label names
			presentLabels := make(map[string]bool)
			for _, label := range labels {
				if labelObj, ok := label.(map[string]interface{}); ok {
					if labelName, ok := labelObj["name"].(string); ok {
						presentLabels[labelName] = true
					}
				}
			}

			// Check that none of the excluded labels are present
			for _, excludeLabel := range filter.ExcludeLabels {
				if presentLabels[excludeLabel] {
					return false
				}
			}
		}
	}

	return true
}

// spawnerNeedsLinearLabels returns true if the spawner has any Comment filter
// that uses Labels or ExcludeLabels and the parsed event is a Comment whose
// issue labels are missing from the payload (the common case for Linear
// Comment webhooks).
func spawnerNeedsLinearLabels(spawner *v1alpha1.TaskSpawner, eventData *LinearEventData) bool {
	if eventData.Type != "Comment" {
		return false
	}

	lw := spawner.Spec.When.LinearWebhook
	if lw == nil {
		return false
	}

	// Check if any Comment filter uses label-based filtering
	for _, f := range lw.Filters {
		if f.Type != "Comment" {
			continue
		}
		if len(f.Labels) > 0 || len(f.ExcludeLabels) > 0 {
			// Only enrich if issue labels are absent from the payload
			dataObj, _ := eventData.Payload["data"].(map[string]interface{})
			if dataObj == nil {
				return true
			}
			labels := extractLabels(dataObj)
			return labels == nil
		}
	}

	return false
}

// linearLabelFetcher is the function used to fetch issue labels from the
// Linear API. It is a package-level variable so tests can swap in a stub.
var linearLabelFetcher = fetchLinearIssueLabels

// enrichLinearCommentLabels fetches labels from the Linear API for Comment
// events and injects them into the parsed payload at data.issue.labels so
// that downstream label filtering works.
func enrichLinearCommentLabels(ctx context.Context, log logr.Logger, eventData *LinearEventData) {
	dataObj, _ := eventData.Payload["data"].(map[string]interface{})
	if dataObj == nil {
		return
	}

	// Extract the parent issue ID from data.issue.id
	issue, _ := dataObj["issue"].(map[string]interface{})
	if issue == nil {
		log.Info("Comment webhook has no issue object, cannot enrich labels")
		return
	}

	var issueID string
	switch id := issue["id"].(type) {
	case string:
		issueID = id
	case float64:
		issueID = fmt.Sprintf("%.0f", id)
	}
	if issueID == "" {
		log.Info("Comment webhook has no issue ID, cannot enrich labels")
		return
	}

	labels, err := linearLabelFetcher(ctx, issueID)
	if err != nil {
		log.Error(err, "Failed to fetch Linear issue labels", "issueID", issueID)
		return
	}
	if labels == nil {
		// LINEAR_API_KEY not set — nothing to enrich
		return
	}

	log.Info("Enriched Comment event with issue labels from Linear API", "issueID", issueID, "labels", labels)

	// Inject labels into data.issue.labels as []interface{} matching the
	// format that extractLabels/matchesLinearFilter expect.
	labelObjs := make([]interface{}, len(labels))
	for i, name := range labels {
		labelObjs[i] = map[string]interface{}{"name": name}
	}
	issue["labels"] = labelObjs

	// Also update the convenience field on LinearEventData
	eventData.Labels = labels
}

// ExtractLinearWorkItem extracts template variables from Linear webhook events for task creation.
func ExtractLinearWorkItem(eventData *LinearEventData) map[string]interface{} {
	vars := map[string]interface{}{
		"Type":    eventData.Type,
		"Action":  eventData.Action,
		"Payload": eventData.Payload,
		"State":   eventData.State,
		"Labels":  strings.Join(eventData.Labels, ", "),
		// Standard variables for compatibility
		"ID":    eventData.ID,
		"Title": eventData.Title,
		"Kind":  "LinearWebhook",
	}

	// For Comment events, extract the parent issue ID
	if eventData.Type == "Comment" {
		if dataObj, ok := eventData.Payload["data"].(map[string]interface{}); ok {
			if issue, ok := dataObj["issue"].(map[string]interface{}); ok {
				if issueID, ok := issue["id"].(string); ok {
					vars["IssueID"] = issueID
				} else if issueID, ok := issue["id"].(float64); ok {
					vars["IssueID"] = fmt.Sprintf("%.0f", issueID)
				}
			}
		}
	}

	return vars
}
