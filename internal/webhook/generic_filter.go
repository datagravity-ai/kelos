package webhook

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/PaesslerAG/jsonpath"

	"github.com/kelos-dev/kelos/api/v1alpha1"
)

// GenericEventData represents parsed generic webhook data.
type GenericEventData struct {
	// Fields extracted via fieldMapping JSONPath expressions.
	Fields map[string]string
	// Raw payload for {{.Payload}} template access.
	Payload interface{}
}

// ParseGenericWebhook parses an arbitrary JSON webhook payload.
func ParseGenericWebhook(payload []byte) (*GenericEventData, error) {
	var rawPayload interface{}
	if err := json.Unmarshal(payload, &rawPayload); err != nil {
		return nil, fmt.Errorf("invalid JSON payload: %w", err)
	}

	return &GenericEventData{
		Fields:  make(map[string]string),
		Payload: rawPayload,
	}, nil
}

// ExtractFields evaluates JSONPath expressions from fieldMapping against
// the payload and populates the Fields map. Missing fields produce empty
// strings rather than errors so that optional mappings do not block task
// creation.
func (e *GenericEventData) ExtractFields(fieldMapping map[string]string) error {
	for key, expr := range fieldMapping {
		val, err := jsonpath.Get(expr, e.Payload)
		if err != nil {
			// Missing field — not an error, just empty.
			e.Fields[key] = ""
			continue
		}
		e.Fields[key] = fmt.Sprintf("%v", val)
	}
	return nil
}

// MatchesGenericFilters checks if the payload matches all filters (AND semantics).
func MatchesGenericFilters(filters []v1alpha1.GenericWebhookFilter, payload interface{}) (bool, error) {
	for _, filter := range filters {
		val, err := jsonpath.Get(filter.Field, payload)
		if err != nil {
			return false, nil // Field doesn't exist → filter fails
		}

		strVal := fmt.Sprintf("%v", val)

		if filter.Value != nil {
			if strVal != *filter.Value {
				return false, nil
			}
		} else if filter.Pattern != "" {
			matched, err := regexp.MatchString(filter.Pattern, strVal)
			if err != nil {
				return false, fmt.Errorf("invalid regex pattern %q: %w", filter.Pattern, err)
			}
			if !matched {
				return false, nil
			}
		}
	}
	return true, nil
}

// canonicalFieldNames maps documented lowercase fieldMapping keys to the
// uppercase template variable names used by GitHub and Linear sources.
// When a user writes fieldMapping: {id: "$.data.id"}, both {{.id}} and
// {{.ID}} will work in templates.
var canonicalFieldNames = map[string]string{
	"id":    "ID",
	"title": "Title",
	"body":  "Body",
	"url":   "URL",
}

// ExtractGenericWorkItem converts generic webhook data to template variables.
// All fieldMapping keys become top-level template variables. Lowercase keys
// that match standard field names (id, title, body, url) are also promoted
// to their uppercase equivalents (ID, Title, Body, URL) for compatibility
// with GitHub and Linear source templates. The full raw payload is always
// available as {{.Payload}}.
func ExtractGenericWorkItem(eventData *GenericEventData) map[string]interface{} {
	vars := map[string]interface{}{
		"Kind":    "GenericWebhook",
		"Payload": eventData.Payload,
	}

	for key, value := range eventData.Fields {
		vars[key] = value
		if upper, ok := canonicalFieldNames[key]; ok {
			vars[upper] = value
		}
	}

	// Ensure standard fields exist even if not mapped.
	for _, stdField := range []string{"ID", "Title", "Body", "URL"} {
		if _, ok := vars[stdField]; !ok {
			vars[stdField] = ""
		}
	}

	return vars
}

// extractSourceFromPath extracts the source name from a URL path like
// /webhook/<source>. Returns empty string if the path doesn't match.
func extractSourceFromPath(path string) string {
	path = strings.TrimSuffix(path, "/")
	parts := strings.Split(path, "/")
	// Expect ["", "webhook", "<source>"]
	if len(parts) == 3 && parts[1] == "webhook" && parts[2] != "" {
		return parts[2]
	}
	return ""
}
