package webhook

import (
	"encoding/json"
	"testing"

	kelosv1alpha1 "github.com/kelos-dev/kelos/api/v1alpha1"
)

func boolPtr(b bool) *bool { return &b }

func TestMatchesGitHubEvent(t *testing.T) {
	tests := []struct {
		name      string
		cfg       *kelosv1alpha1.GitHubWebhook
		eventType string
		payload   interface{}
		want      bool
	}{
		{
			name: "event type not in list",
			cfg: &kelosv1alpha1.GitHubWebhook{
				Events: []string{"issues"},
			},
			eventType: "push",
			payload:   map[string]interface{}{},
			want:      false,
		},
		{
			name: "event type matches, no filters",
			cfg: &kelosv1alpha1.GitHubWebhook{
				Events: []string{"issues", "push"},
			},
			eventType: "push",
			payload:   map[string]interface{}{},
			want:      true,
		},
		{
			name: "issue_comment with action filter match",
			cfg: &kelosv1alpha1.GitHubWebhook{
				Events: []string{"issue_comment"},
				Filters: []kelosv1alpha1.GitHubWebhookFilter{
					{Event: "issue_comment", Action: "created"},
				},
			},
			eventType: "issue_comment",
			payload: map[string]interface{}{
				"action": "created",
				"sender": map[string]interface{}{"login": "user1"},
				"issue":  map[string]interface{}{"number": 42, "state": "open"},
			},
			want: true,
		},
		{
			name: "issue_comment action mismatch",
			cfg: &kelosv1alpha1.GitHubWebhook{
				Events: []string{"issue_comment"},
				Filters: []kelosv1alpha1.GitHubWebhookFilter{
					{Event: "issue_comment", Action: "created"},
				},
			},
			eventType: "issue_comment",
			payload: map[string]interface{}{
				"action": "deleted",
				"sender": map[string]interface{}{"login": "user1"},
			},
			want: false,
		},
		{
			name: "bodyContains filter match",
			cfg: &kelosv1alpha1.GitHubWebhook{
				Events: []string{"issue_comment"},
				Filters: []kelosv1alpha1.GitHubWebhookFilter{
					{Event: "issue_comment", Action: "created", BodyContains: "/fix"},
				},
			},
			eventType: "issue_comment",
			payload: map[string]interface{}{
				"action":  "created",
				"sender":  map[string]interface{}{"login": "user1"},
				"comment": map[string]interface{}{"body": "please /fix this bug"},
			},
			want: true,
		},
		{
			name: "bodyContains filter no match",
			cfg: &kelosv1alpha1.GitHubWebhook{
				Events: []string{"issue_comment"},
				Filters: []kelosv1alpha1.GitHubWebhookFilter{
					{Event: "issue_comment", Action: "created", BodyContains: "/fix"},
				},
			},
			eventType: "issue_comment",
			payload: map[string]interface{}{
				"action":  "created",
				"sender":  map[string]interface{}{"login": "user1"},
				"comment": map[string]interface{}{"body": "looks good to me"},
			},
			want: false,
		},
		{
			name: "label filter match",
			cfg: &kelosv1alpha1.GitHubWebhook{
				Events: []string{"issues"},
				Filters: []kelosv1alpha1.GitHubWebhookFilter{
					{Event: "issues", Labels: []string{"bug"}},
				},
			},
			eventType: "issues",
			payload: map[string]interface{}{
				"action": "opened",
				"sender": map[string]interface{}{"login": "user1"},
				"issue": map[string]interface{}{
					"number": 1,
					"state":  "open",
					"labels": []interface{}{
						map[string]interface{}{"name": "bug"},
						map[string]interface{}{"name": "priority/high"},
					},
				},
			},
			want: true,
		},
		{
			name: "label filter no match",
			cfg: &kelosv1alpha1.GitHubWebhook{
				Events: []string{"issues"},
				Filters: []kelosv1alpha1.GitHubWebhookFilter{
					{Event: "issues", Labels: []string{"bug"}},
				},
			},
			eventType: "issues",
			payload: map[string]interface{}{
				"action": "opened",
				"sender": map[string]interface{}{"login": "user1"},
				"issue": map[string]interface{}{
					"number": 1,
					"state":  "open",
					"labels": []interface{}{
						map[string]interface{}{"name": "feature"},
					},
				},
			},
			want: false,
		},
		{
			name: "author filter match",
			cfg: &kelosv1alpha1.GitHubWebhook{
				Events: []string{"issue_comment"},
				Filters: []kelosv1alpha1.GitHubWebhookFilter{
					{Event: "issue_comment", Author: "admin"},
				},
			},
			eventType: "issue_comment",
			payload: map[string]interface{}{
				"action": "created",
				"sender": map[string]interface{}{"login": "admin"},
			},
			want: true,
		},
		{
			name: "author filter case insensitive",
			cfg: &kelosv1alpha1.GitHubWebhook{
				Events: []string{"issue_comment"},
				Filters: []kelosv1alpha1.GitHubWebhookFilter{
					{Event: "issue_comment", Author: "Admin"},
				},
			},
			eventType: "issue_comment",
			payload: map[string]interface{}{
				"action": "created",
				"sender": map[string]interface{}{"login": "admin"},
			},
			want: true,
		},
		{
			name: "push event with branch filter match",
			cfg: &kelosv1alpha1.GitHubWebhook{
				Events: []string{"push"},
				Filters: []kelosv1alpha1.GitHubWebhookFilter{
					{Event: "push", Branch: "main"},
				},
			},
			eventType: "push",
			payload: map[string]interface{}{
				"ref":    "refs/heads/main",
				"sender": map[string]interface{}{"login": "user1"},
			},
			want: true,
		},
		{
			name: "push event with branch glob match",
			cfg: &kelosv1alpha1.GitHubWebhook{
				Events: []string{"push"},
				Filters: []kelosv1alpha1.GitHubWebhookFilter{
					{Event: "push", Branch: "release-*"},
				},
			},
			eventType: "push",
			payload: map[string]interface{}{
				"ref":    "refs/heads/release-1.0",
				"sender": map[string]interface{}{"login": "user1"},
			},
			want: true,
		},
		{
			name: "push event with branch filter no match",
			cfg: &kelosv1alpha1.GitHubWebhook{
				Events: []string{"push"},
				Filters: []kelosv1alpha1.GitHubWebhookFilter{
					{Event: "push", Branch: "main"},
				},
			},
			eventType: "push",
			payload: map[string]interface{}{
				"ref":    "refs/heads/feature-branch",
				"sender": map[string]interface{}{"login": "user1"},
			},
			want: false,
		},
		{
			name: "draft filter match",
			cfg: &kelosv1alpha1.GitHubWebhook{
				Events: []string{"pull_request"},
				Filters: []kelosv1alpha1.GitHubWebhookFilter{
					{Event: "pull_request", Draft: boolPtr(false)},
				},
			},
			eventType: "pull_request",
			payload: map[string]interface{}{
				"action":       "opened",
				"sender":       map[string]interface{}{"login": "user1"},
				"pull_request": map[string]interface{}{"number": 1, "state": "open", "draft": false},
			},
			want: true,
		},
		{
			name: "state filter match",
			cfg: &kelosv1alpha1.GitHubWebhook{
				Events: []string{"issues"},
				Filters: []kelosv1alpha1.GitHubWebhookFilter{
					{Event: "issues", State: "open"},
				},
			},
			eventType: "issues",
			payload: map[string]interface{}{
				"action": "opened",
				"sender": map[string]interface{}{"login": "user1"},
				"issue":  map[string]interface{}{"number": 1, "state": "open"},
			},
			want: true,
		},
		{
			name: "OR semantics - second filter matches",
			cfg: &kelosv1alpha1.GitHubWebhook{
				Events: []string{"issue_comment", "push"},
				Filters: []kelosv1alpha1.GitHubWebhookFilter{
					{Event: "issue_comment", BodyContains: "/deploy"},
					{Event: "push", Branch: "main"},
				},
			},
			eventType: "push",
			payload: map[string]interface{}{
				"ref":    "refs/heads/main",
				"sender": map[string]interface{}{"login": "user1"},
			},
			want: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			payloadBytes, _ := json.Marshal(tt.payload)
			got := MatchesGitHubEvent(tt.cfg, tt.eventType, payloadBytes)
			if got != tt.want {
				t.Errorf("MatchesGitHubEvent() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestExtractGitHubWorkItem(t *testing.T) {
	payload := map[string]interface{}{
		"action": "created",
		"sender": map[string]interface{}{"login": "testuser"},
		"issue": map[string]interface{}{
			"number":   42,
			"title":    "Fix the bug",
			"body":     "There is a bug",
			"state":    "open",
			"html_url": "https://github.com/org/repo/issues/42",
			"labels":   []interface{}{map[string]interface{}{"name": "bug"}},
		},
		"comment": map[string]interface{}{
			"body": "/fix please address this",
		},
	}

	payloadBytes, _ := json.Marshal(payload)
	data := ExtractGitHubWorkItem("issue_comment", payloadBytes)

	if data == nil {
		t.Fatal("ExtractGitHubWorkItem returned nil")
	}

	if data["Event"] != "issue_comment" {
		t.Errorf("Event = %v, want issue_comment", data["Event"])
	}
	if data["Action"] != "created" {
		t.Errorf("Action = %v, want created", data["Action"])
	}
	if data["Sender"] != "testuser" {
		t.Errorf("Sender = %v, want testuser", data["Sender"])
	}
	if data["Number"] != 42 {
		t.Errorf("Number = %v, want 42", data["Number"])
	}
	// Body should be the comment body (overrides issue body)
	if data["Body"] != "/fix please address this" {
		t.Errorf("Body = %v, want /fix please address this", data["Body"])
	}
	if data["Kind"] != "Issue" {
		t.Errorf("Kind = %v, want Issue", data["Kind"])
	}

	// Check Payload is accessible
	if data["Payload"] == nil {
		t.Error("Payload should not be nil")
	}
}
