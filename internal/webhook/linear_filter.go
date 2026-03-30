package webhook

import (
	"encoding/json"
	"strings"

	kelosv1alpha1 "github.com/kelos-dev/kelos/api/v1alpha1"
)

// linearWebhookPayload is a minimal representation of a Linear webhook payload.
type linearWebhookPayload struct {
	Action string `json:"action"`
	Type   string `json:"type"`
	Data   struct {
		ID          string `json:"id"`
		Title       string `json:"title"`
		Description string `json:"description"`
		URL         string `json:"url"`
		State       *struct {
			Name string `json:"name"`
		} `json:"state"`
		Labels struct {
			Nodes []struct {
				Name string `json:"name"`
			} `json:"nodes"`
		} `json:"labels"`
	} `json:"data"`
}

// MatchesLinearEvent checks whether a Linear webhook event matches a TaskSpawner's
// LinearWebhook configuration.
func MatchesLinearEvent(cfg *kelosv1alpha1.LinearWebhook, payload []byte) bool {
	var p linearWebhookPayload
	if err := json.Unmarshal(payload, &p); err != nil {
		return false
	}

	// Check if the resource type is in the configured types list
	typeMatched := false
	for _, t := range cfg.Types {
		if strings.EqualFold(t, p.Type) {
			typeMatched = true
			break
		}
	}
	if !typeMatched {
		return false
	}

	// If no filters, all matching types trigger
	if len(cfg.Filters) == 0 {
		return true
	}

	// OR semantics: any matching filter triggers
	for _, f := range cfg.Filters {
		if matchesLinearFilter(&f, &p) {
			return true
		}
	}

	return false
}

func matchesLinearFilter(f *kelosv1alpha1.LinearWebhookFilter, p *linearWebhookPayload) bool {
	if !strings.EqualFold(f.Type, p.Type) {
		return false
	}

	if f.Action != "" && !strings.EqualFold(f.Action, p.Action) {
		return false
	}

	if len(f.States) > 0 {
		if p.Data.State == nil {
			return false
		}
		stateMatched := false
		for _, s := range f.States {
			if strings.EqualFold(s, p.Data.State.Name) {
				stateMatched = true
				break
			}
		}
		if !stateMatched {
			return false
		}
	}

	if len(f.Labels) > 0 {
		labels := extractLinearLabels(p)
		if !containsAllLabels(labels, f.Labels) {
			return false
		}
	}

	if len(f.ExcludeLabels) > 0 {
		labels := extractLinearLabels(p)
		for _, excl := range f.ExcludeLabels {
			for _, l := range labels {
				if strings.EqualFold(l, excl) {
					return false
				}
			}
		}
	}

	return true
}

func extractLinearLabels(p *linearWebhookPayload) []string {
	labels := make([]string, len(p.Data.Labels.Nodes))
	for i, l := range p.Data.Labels.Nodes {
		labels[i] = l.Name
	}
	return labels
}

// ExtractLinearWorkItem extracts template variables from a Linear webhook payload.
func ExtractLinearWorkItem(payload []byte) map[string]interface{} {
	var p linearWebhookPayload
	if err := json.Unmarshal(payload, &p); err != nil {
		return nil
	}

	data := map[string]interface{}{
		"Event":  p.Type,
		"Action": p.Action,
		"Title":  p.Data.Title,
		"Body":   p.Data.Description,
		"URL":    p.Data.URL,
		"ID":     p.Data.ID,
		"Kind":   p.Type,
	}

	if p.Data.State != nil {
		data["State"] = p.Data.State.Name
	}

	data["Labels"] = extractLinearLabels(&p)

	// Parse full payload for {{.Payload.*}} access
	var fullPayload map[string]interface{}
	json.Unmarshal(payload, &fullPayload)
	data["Payload"] = fullPayload

	return data
}

// containsAllLabels checks if the issue has all the required labels.
func containsAllLabels(have []string, want []string) bool {
	set := make(map[string]struct{}, len(have))
	for _, l := range have {
		set[l] = struct{}{}
	}
	for _, w := range want {
		if _, ok := set[w]; !ok {
			return false
		}
	}
	return true
}
