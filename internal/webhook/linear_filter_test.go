package webhook

import (
	"encoding/json"
	"testing"

	kelosv1alpha1 "github.com/kelos-dev/kelos/api/v1alpha1"
)

func TestMatchesLinearEvent(t *testing.T) {
	tests := []struct {
		name    string
		cfg     *kelosv1alpha1.LinearWebhook
		payload interface{}
		want    bool
	}{
		{
			name: "type not in list",
			cfg: &kelosv1alpha1.LinearWebhook{
				Types: []string{"Issue"},
			},
			payload: map[string]interface{}{
				"type":   "Comment",
				"action": "create",
			},
			want: false,
		},
		{
			name: "type matches, no filters",
			cfg: &kelosv1alpha1.LinearWebhook{
				Types: []string{"Issue"},
			},
			payload: map[string]interface{}{
				"type":   "Issue",
				"action": "create",
			},
			want: true,
		},
		{
			name: "type case insensitive",
			cfg: &kelosv1alpha1.LinearWebhook{
				Types: []string{"Issue"},
			},
			payload: map[string]interface{}{
				"type":   "issue",
				"action": "create",
			},
			want: true,
		},
		{
			name: "action filter match",
			cfg: &kelosv1alpha1.LinearWebhook{
				Types: []string{"Issue"},
				Filters: []kelosv1alpha1.LinearWebhookFilter{
					{Type: "Issue", Action: "create"},
				},
			},
			payload: map[string]interface{}{
				"type":   "Issue",
				"action": "create",
				"data":   map[string]interface{}{"id": "123"},
			},
			want: true,
		},
		{
			name: "action filter no match",
			cfg: &kelosv1alpha1.LinearWebhook{
				Types: []string{"Issue"},
				Filters: []kelosv1alpha1.LinearWebhookFilter{
					{Type: "Issue", Action: "create"},
				},
			},
			payload: map[string]interface{}{
				"type":   "Issue",
				"action": "update",
			},
			want: false,
		},
		{
			name: "state filter match",
			cfg: &kelosv1alpha1.LinearWebhook{
				Types: []string{"Issue"},
				Filters: []kelosv1alpha1.LinearWebhookFilter{
					{Type: "Issue", States: []string{"Todo"}},
				},
			},
			payload: map[string]interface{}{
				"type":   "Issue",
				"action": "create",
				"data": map[string]interface{}{
					"state": map[string]interface{}{"name": "Todo"},
				},
			},
			want: true,
		},
		{
			name: "state filter no match",
			cfg: &kelosv1alpha1.LinearWebhook{
				Types: []string{"Issue"},
				Filters: []kelosv1alpha1.LinearWebhookFilter{
					{Type: "Issue", States: []string{"Todo"}},
				},
			},
			payload: map[string]interface{}{
				"type":   "Issue",
				"action": "create",
				"data": map[string]interface{}{
					"state": map[string]interface{}{"name": "Done"},
				},
			},
			want: false,
		},
		{
			name: "label filter match",
			cfg: &kelosv1alpha1.LinearWebhook{
				Types: []string{"Issue"},
				Filters: []kelosv1alpha1.LinearWebhookFilter{
					{Type: "Issue", Labels: []string{"bug"}},
				},
			},
			payload: map[string]interface{}{
				"type":   "Issue",
				"action": "create",
				"data": map[string]interface{}{
					"labels": map[string]interface{}{
						"nodes": []interface{}{
							map[string]interface{}{"name": "bug"},
							map[string]interface{}{"name": "priority"},
						},
					},
				},
			},
			want: true,
		},
		{
			name: "excludeLabels filter",
			cfg: &kelosv1alpha1.LinearWebhook{
				Types: []string{"Issue"},
				Filters: []kelosv1alpha1.LinearWebhookFilter{
					{Type: "Issue", ExcludeLabels: []string{"wontfix"}},
				},
			},
			payload: map[string]interface{}{
				"type":   "Issue",
				"action": "create",
				"data": map[string]interface{}{
					"labels": map[string]interface{}{
						"nodes": []interface{}{
							map[string]interface{}{"name": "wontfix"},
						},
					},
				},
			},
			want: false,
		},
		{
			name: "OR semantics - second filter matches",
			cfg: &kelosv1alpha1.LinearWebhook{
				Types: []string{"Issue", "Comment"},
				Filters: []kelosv1alpha1.LinearWebhookFilter{
					{Type: "Issue", Action: "create"},
					{Type: "Comment", Action: "create"},
				},
			},
			payload: map[string]interface{}{
				"type":   "Comment",
				"action": "create",
			},
			want: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			payloadBytes, _ := json.Marshal(tt.payload)
			got := MatchesLinearEvent(tt.cfg, payloadBytes)
			if got != tt.want {
				t.Errorf("MatchesLinearEvent() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestExtractLinearWorkItem(t *testing.T) {
	payload := map[string]interface{}{
		"type":   "Issue",
		"action": "create",
		"data": map[string]interface{}{
			"id":          "abc-123",
			"title":       "Fix login page",
			"description": "The login page is broken",
			"url":         "https://linear.app/team/issue/ABC-123",
			"state":       map[string]interface{}{"name": "Todo"},
			"labels": map[string]interface{}{
				"nodes": []interface{}{
					map[string]interface{}{"name": "bug"},
				},
			},
		},
	}

	payloadBytes, _ := json.Marshal(payload)
	data := ExtractLinearWorkItem(payloadBytes)

	if data == nil {
		t.Fatal("ExtractLinearWorkItem returned nil")
	}

	if data["Event"] != "Issue" {
		t.Errorf("Event = %v, want Issue", data["Event"])
	}
	if data["Action"] != "create" {
		t.Errorf("Action = %v, want create", data["Action"])
	}
	if data["Title"] != "Fix login page" {
		t.Errorf("Title = %v, want Fix login page", data["Title"])
	}
	if data["ID"] != "abc-123" {
		t.Errorf("ID = %v, want abc-123", data["ID"])
	}
	if data["State"] != "Todo" {
		t.Errorf("State = %v, want Todo", data["State"])
	}
	if data["Payload"] == nil {
		t.Error("Payload should not be nil")
	}
}
