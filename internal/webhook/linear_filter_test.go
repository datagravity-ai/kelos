package webhook

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-logr/logr"
	"github.com/kelos-dev/kelos/api/v1alpha1"
)

// parseAndMatchLinear is a test helper that parses a payload and calls MatchesLinearEvent.
func parseAndMatchLinear(t *testing.T, config *v1alpha1.LinearWebhook, payload []byte) (bool, error) {
	t.Helper()
	eventData, err := ParseLinearWebhook(payload)
	if err != nil {
		return false, err
	}
	return MatchesLinearEvent(config, eventData)
}

func TestMatchesLinearEvent_TypeFilter(t *testing.T) {
	spawner := &v1alpha1.LinearWebhook{
		Types: []string{"Issue", "Comment"},
	}

	tests := []struct {
		name      string
		eventType string
		want      bool
		wantErr   bool
	}{
		{
			name:      "allowed event type",
			eventType: "Issue",
			want:      true,
		},
		{
			name:      "another allowed event type",
			eventType: "Comment",
			want:      true,
		},
		{
			name:      "disallowed event type",
			eventType: "Project",
			want:      false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			payload := []byte(`{"type":"` + tt.eventType + `","action":"create","data":{"id":"123"}}`)
			got, err := parseAndMatchLinear(t, spawner, payload)
			if (err != nil) != tt.wantErr {
				t.Errorf("MatchesLinearEvent() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if got != tt.want {
				t.Errorf("MatchesLinearEvent() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestMatchesLinearEvent_ActionFilter(t *testing.T) {
	spawner := &v1alpha1.LinearWebhook{
		Types: []string{"Issue"},
		Filters: []v1alpha1.LinearWebhookFilter{
			{
				Type:   "Issue",
				Action: "create",
			},
		},
	}

	tests := []struct {
		name    string
		payload string
		want    bool
	}{
		{
			name:    "matching action",
			payload: `{"type":"Issue","action":"create","data":{"id":"123"}}`,
			want:    true,
		},
		{
			name:    "non-matching action",
			payload: `{"type":"Issue","action":"update","data":{"id":"123"}}`,
			want:    false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseAndMatchLinear(t, spawner, []byte(tt.payload))
			if err != nil {
				t.Errorf("MatchesLinearEvent() error = %v", err)
				return
			}
			if got != tt.want {
				t.Errorf("MatchesLinearEvent() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestMatchesLinearEvent_StateFilter(t *testing.T) {
	spawner := &v1alpha1.LinearWebhook{
		Types: []string{"Issue"},
		Filters: []v1alpha1.LinearWebhookFilter{
			{
				Type:   "Issue",
				States: []string{"Todo", "In Progress"},
			},
		},
	}

	tests := []struct {
		name    string
		payload string
		want    bool
	}{
		{
			name: "matching state",
			payload: `{
				"type":"Issue",
				"action":"update",
				"data":{
					"id":"123",
					"title":"Test issue",
					"state":{"name":"Todo"}
				}
			}`,
			want: true,
		},
		{
			name: "another matching state",
			payload: `{
				"type":"Issue",
				"action":"update",
				"data":{
					"id":"123",
					"title":"Test issue",
					"state":{"name":"In Progress"}
				}
			}`,
			want: true,
		},
		{
			name: "non-matching state",
			payload: `{
				"type":"Issue",
				"action":"update",
				"data":{
					"id":"123",
					"title":"Test issue",
					"state":{"name":"Done"}
				}
			}`,
			want: false,
		},
		{
			name: "no state data",
			payload: `{
				"type":"Issue",
				"action":"update",
				"data":{
					"id":"123",
					"title":"Test issue"
				}
			}`,
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseAndMatchLinear(t, spawner, []byte(tt.payload))
			if err != nil {
				t.Errorf("MatchesLinearEvent() error = %v", err)
				return
			}
			if got != tt.want {
				t.Errorf("MatchesLinearEvent() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestMatchesLinearEvent_LabelsFilter(t *testing.T) {
	spawner := &v1alpha1.LinearWebhook{
		Types: []string{"Issue"},
		Filters: []v1alpha1.LinearWebhookFilter{
			{
				Type:   "Issue",
				Labels: []string{"bug", "priority:high"},
			},
		},
	}

	tests := []struct {
		name    string
		payload string
		want    bool
	}{
		{
			name: "has all required labels",
			payload: `{
				"type":"Issue",
				"action":"create",
				"data":{
					"id":"123",
					"title":"Test issue",
					"labels":[
						{"name":"bug"},
						{"name":"priority:high"},
						{"name":"frontend"}
					]
				}
			}`,
			want: true,
		},
		{
			name: "missing required label",
			payload: `{
				"type":"Issue",
				"action":"create",
				"data":{
					"id":"123",
					"title":"Test issue",
					"labels":[
						{"name":"bug"},
						{"name":"frontend"}
					]
				}
			}`,
			want: false,
		},
		{
			name: "no labels",
			payload: `{
				"type":"Issue",
				"action":"create",
				"data":{
					"id":"123",
					"title":"Test issue",
					"labels":[]
				}
			}`,
			want: false,
		},
		{
			name: "labels field missing",
			payload: `{
				"type":"Issue",
				"action":"create",
				"data":{
					"id":"123",
					"title":"Test issue"
				}
			}`,
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseAndMatchLinear(t, spawner, []byte(tt.payload))
			if err != nil {
				t.Errorf("MatchesLinearEvent() error = %v", err)
				return
			}
			if got != tt.want {
				t.Errorf("MatchesLinearEvent() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestMatchesLinearEvent_LabelsCaseInsensitive(t *testing.T) {
	spawner := &v1alpha1.LinearWebhook{
		Types: []string{"Issue"},
		Filters: []v1alpha1.LinearWebhookFilter{
			{
				Type:   "Issue",
				Labels: []string{"Bug", "Priority:High"},
			},
		},
	}

	tests := []struct {
		name    string
		payload string
		want    bool
	}{
		{
			name: "labels match with different casing",
			payload: `{
				"type":"Issue",
				"action":"create",
				"data":{
					"id":"123",
					"title":"Test issue",
					"labels":[
						{"name":"bug"},
						{"name":"priority:high"}
					]
				}
			}`,
			want: true,
		},
		{
			name: "labels match with uppercase event labels",
			payload: `{
				"type":"Issue",
				"action":"create",
				"data":{
					"id":"123",
					"title":"Test issue",
					"labels":[
						{"name":"BUG"},
						{"name":"PRIORITY:HIGH"}
					]
				}
			}`,
			want: true,
		},
		{
			name: "labels match with mixed casing",
			payload: `{
				"type":"Issue",
				"action":"create",
				"data":{
					"id":"123",
					"title":"Test issue",
					"labels":[
						{"name":"Bug"},
						{"name":"Priority:High"},
						{"name":"frontend"}
					]
				}
			}`,
			want: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			eventData, err := ParseLinearWebhook([]byte(tt.payload))
			if err != nil {
				t.Fatalf("ParseLinearWebhook() error = %v", err)
			}
			got, err := MatchesLinearEvent(spawner, eventData)
			if err != nil {
				t.Errorf("MatchesLinearEvent() error = %v", err)
				return
			}
			if got != tt.want {
				t.Errorf("MatchesLinearEvent() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestMatchesLinearEvent_ExcludeLabelsCaseInsensitive(t *testing.T) {
	spawner := &v1alpha1.LinearWebhook{
		Types: []string{"Issue"},
		Filters: []v1alpha1.LinearWebhookFilter{
			{
				Type:          "Issue",
				ExcludeLabels: []string{"WontFix"},
			},
		},
	}

	tests := []struct {
		name    string
		payload string
		want    bool
	}{
		{
			name: "excluded label matches with different casing",
			payload: `{
				"type":"Issue",
				"action":"create",
				"data":{
					"id":"123",
					"title":"Test issue",
					"labels":[
						{"name":"bug"},
						{"name":"wontfix"}
					]
				}
			}`,
			want: false,
		},
		{
			name: "excluded label matches with uppercase",
			payload: `{
				"type":"Issue",
				"action":"create",
				"data":{
					"id":"123",
					"title":"Test issue",
					"labels":[
						{"name":"bug"},
						{"name":"WONTFIX"}
					]
				}
			}`,
			want: false,
		},
		{
			name: "no excluded label present",
			payload: `{
				"type":"Issue",
				"action":"create",
				"data":{
					"id":"123",
					"title":"Test issue",
					"labels":[
						{"name":"bug"}
					]
				}
			}`,
			want: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			eventData, err := ParseLinearWebhook([]byte(tt.payload))
			if err != nil {
				t.Fatalf("ParseLinearWebhook() error = %v", err)
			}
			got, err := MatchesLinearEvent(spawner, eventData)
			if err != nil {
				t.Errorf("MatchesLinearEvent() error = %v", err)
				return
			}
			if got != tt.want {
				t.Errorf("MatchesLinearEvent() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestMatchesLinearEvent_ExcludeLabelsFilter(t *testing.T) {
	spawner := &v1alpha1.LinearWebhook{
		Types: []string{"Issue"},
		Filters: []v1alpha1.LinearWebhookFilter{
			{
				Type:          "Issue",
				ExcludeLabels: []string{"wontfix", "duplicate"},
			},
		},
	}

	tests := []struct {
		name    string
		payload string
		want    bool
	}{
		{
			name: "no excluded labels",
			payload: `{
				"type":"Issue",
				"action":"create",
				"data":{
					"id":"123",
					"title":"Test issue",
					"labels":[
						{"name":"bug"},
						{"name":"frontend"}
					]
				}
			}`,
			want: true,
		},
		{
			name: "has excluded label",
			payload: `{
				"type":"Issue",
				"action":"create",
				"data":{
					"id":"123",
					"title":"Test issue",
					"labels":[
						{"name":"bug"},
						{"name":"wontfix"}
					]
				}
			}`,
			want: false,
		},
		{
			name: "has another excluded label",
			payload: `{
				"type":"Issue",
				"action":"create",
				"data":{
					"id":"123",
					"title":"Test issue",
					"labels":[
						{"name":"duplicate"},
						{"name":"frontend"}
					]
				}
			}`,
			want: false,
		},
		{
			name: "empty labels array",
			payload: `{
				"type":"Issue",
				"action":"create",
				"data":{
					"id":"123",
					"title":"Test issue",
					"labels":[]
				}
			}`,
			want: true,
		},
		{
			name: "no labels field",
			payload: `{
				"type":"Issue",
				"action":"create",
				"data":{
					"id":"123",
					"title":"Test issue"
				}
			}`,
			want: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseAndMatchLinear(t, spawner, []byte(tt.payload))
			if err != nil {
				t.Errorf("MatchesLinearEvent() error = %v", err)
				return
			}
			if got != tt.want {
				t.Errorf("MatchesLinearEvent() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestMatchesLinearEvent_ORSemantics(t *testing.T) {
	// Multiple filters for the same event type should use OR semantics
	spawner := &v1alpha1.LinearWebhook{
		Types: []string{"Issue"},
		Filters: []v1alpha1.LinearWebhookFilter{
			{
				Type:   "Issue",
				Action: "create",
			},
			{
				Type:   "Issue",
				Action: "update",
			},
		},
	}

	tests := []struct {
		name    string
		payload string
		want    bool
	}{
		{
			name:    "matches first filter",
			payload: `{"type":"Issue","action":"create","data":{"id":"123"}}`,
			want:    true,
		},
		{
			name:    "matches second filter",
			payload: `{"type":"Issue","action":"update","data":{"id":"123"}}`,
			want:    true,
		},
		{
			name:    "matches neither filter",
			payload: `{"type":"Issue","action":"remove","data":{"id":"123"}}`,
			want:    false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseAndMatchLinear(t, spawner, []byte(tt.payload))
			if err != nil {
				t.Errorf("MatchesLinearEvent() error = %v", err)
				return
			}
			if got != tt.want {
				t.Errorf("MatchesLinearEvent() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestMatchesLinearEvent_NoFilters(t *testing.T) {
	spawner := &v1alpha1.LinearWebhook{
		Types: []string{"Issue", "Comment"},
		// No filters - should match all allowed types
	}

	tests := []struct {
		name    string
		payload string
		want    bool
	}{
		{
			name:    "allowed type with no filters",
			payload: `{"type":"Issue","action":"create","data":{"id":"123"}}`,
			want:    true,
		},
		{
			name:    "another allowed type with no filters",
			payload: `{"type":"Comment","action":"update","data":{"id":"456"}}`,
			want:    true,
		},
		{
			name:    "disallowed type",
			payload: `{"type":"Project","action":"create","data":{"id":"789"}}`,
			want:    false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseAndMatchLinear(t, spawner, []byte(tt.payload))
			if err != nil {
				t.Errorf("MatchesLinearEvent() error = %v", err)
				return
			}
			if got != tt.want {
				t.Errorf("MatchesLinearEvent() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestMatchesLinearEvent_CommentLabelsFilter(t *testing.T) {
	spawner := &v1alpha1.LinearWebhook{
		Types: []string{"Comment"},
		Filters: []v1alpha1.LinearWebhookFilter{
			{
				Type:   "Comment",
				Labels: []string{"bug", "priority:high"},
			},
		},
	}

	tests := []struct {
		name    string
		payload string
		want    bool
	}{
		{
			name: "comment with issue having all required labels",
			payload: `{
				"type":"Comment",
				"action":"create",
				"data":{
					"id":"comment-123",
					"body":"Test comment",
					"issue":{
						"id":"issue-456",
						"title":"Parent issue",
						"labels":[
							{"name":"bug"},
							{"name":"priority:high"},
							{"name":"frontend"}
						]
					}
				}
			}`,
			want: true,
		},
		{
			name: "comment with issue missing required label",
			payload: `{
				"type":"Comment",
				"action":"create",
				"data":{
					"id":"comment-123",
					"body":"Test comment",
					"issue":{
						"id":"issue-456",
						"title":"Parent issue",
						"labels":[
							{"name":"bug"},
							{"name":"frontend"}
						]
					}
				}
			}`,
			want: false,
		},
		{
			name: "comment with issue having no labels",
			payload: `{
				"type":"Comment",
				"action":"create",
				"data":{
					"id":"comment-123",
					"body":"Test comment",
					"issue":{
						"id":"issue-456",
						"title":"Parent issue",
						"labels":[]
					}
				}
			}`,
			want: false,
		},
		{
			name: "comment with issue.labels field missing",
			payload: `{
				"type":"Comment",
				"action":"create",
				"data":{
					"id":"comment-123",
					"body":"Test comment",
					"issue":{
						"id":"issue-456",
						"title":"Parent issue"
					}
				}
			}`,
			want: false,
		},
		{
			name: "comment with issue field missing entirely",
			payload: `{
				"type":"Comment",
				"action":"create",
				"data":{
					"id":"comment-123",
					"body":"Test comment"
				}
			}`,
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseAndMatchLinear(t, spawner, []byte(tt.payload))
			if err != nil {
				t.Errorf("MatchesLinearEvent() error = %v", err)
				return
			}
			if got != tt.want {
				t.Errorf("MatchesLinearEvent() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestMatchesLinearEvent_CommentExcludeLabelsFilter(t *testing.T) {
	spawner := &v1alpha1.LinearWebhook{
		Types: []string{"Comment"},
		Filters: []v1alpha1.LinearWebhookFilter{
			{
				Type:          "Comment",
				ExcludeLabels: []string{"wontfix", "duplicate"},
			},
		},
	}

	tests := []struct {
		name    string
		payload string
		want    bool
	}{
		{
			name: "comment with issue having no excluded labels",
			payload: `{
				"type":"Comment",
				"action":"create",
				"data":{
					"id":"comment-123",
					"body":"Test comment",
					"issue":{
						"id":"issue-456",
						"title":"Parent issue",
						"labels":[
							{"name":"bug"},
							{"name":"frontend"}
						]
					}
				}
			}`,
			want: true,
		},
		{
			name: "comment with issue having excluded label",
			payload: `{
				"type":"Comment",
				"action":"create",
				"data":{
					"id":"comment-123",
					"body":"Test comment",
					"issue":{
						"id":"issue-456",
						"title":"Parent issue",
						"labels":[
							{"name":"bug"},
							{"name":"wontfix"}
						]
					}
				}
			}`,
			want: false,
		},
		{
			name: "comment with issue having another excluded label",
			payload: `{
				"type":"Comment",
				"action":"create",
				"data":{
					"id":"comment-123",
					"body":"Test comment",
					"issue":{
						"id":"issue-456",
						"title":"Parent issue",
						"labels":[
							{"name":"duplicate"},
							{"name":"frontend"}
						]
					}
				}
			}`,
			want: false,
		},
		{
			name: "comment with issue having empty labels array",
			payload: `{
				"type":"Comment",
				"action":"create",
				"data":{
					"id":"comment-123",
					"body":"Test comment",
					"issue":{
						"id":"issue-456",
						"title":"Parent issue",
						"labels":[]
					}
				}
			}`,
			want: true,
		},
		{
			name: "comment with issue.labels field missing",
			payload: `{
				"type":"Comment",
				"action":"create",
				"data":{
					"id":"comment-123",
					"body":"Test comment",
					"issue":{
						"id":"issue-456",
						"title":"Parent issue"
					}
				}
			}`,
			want: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseAndMatchLinear(t, spawner, []byte(tt.payload))
			if err != nil {
				t.Errorf("MatchesLinearEvent() error = %v", err)
				return
			}
			if got != tt.want {
				t.Errorf("MatchesLinearEvent() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestMatchesLinearEvent_IssueLabelsRegression(t *testing.T) {
	// Regression test: ensure Issue events still use data.labels (not data.issue.labels)
	spawner := &v1alpha1.LinearWebhook{
		Types: []string{"Issue"},
		Filters: []v1alpha1.LinearWebhookFilter{
			{
				Type:   "Issue",
				Labels: []string{"bug"},
			},
		},
	}

	tests := []struct {
		name    string
		payload string
		want    bool
	}{
		{
			name: "issue with labels at data.labels",
			payload: `{
				"type":"Issue",
				"action":"create",
				"data":{
					"id":"issue-123",
					"title":"Test issue",
					"labels":[
						{"name":"bug"},
						{"name":"frontend"}
					]
				}
			}`,
			want: true,
		},
		{
			name: "issue without required label",
			payload: `{
				"type":"Issue",
				"action":"create",
				"data":{
					"id":"issue-123",
					"title":"Test issue",
					"labels":[
						{"name":"frontend"}
					]
				}
			}`,
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseAndMatchLinear(t, spawner, []byte(tt.payload))
			if err != nil {
				t.Errorf("MatchesLinearEvent() error = %v", err)
				return
			}
			if got != tt.want {
				t.Errorf("MatchesLinearEvent() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestExtractLinearWorkItem_CommentIssueID(t *testing.T) {
	tests := []struct {
		name        string
		payload     string
		wantIssueID string
		hasIssueID  bool
	}{
		{
			name: "comment event with string issue ID",
			payload: `{
				"type":"Comment",
				"action":"create",
				"data":{
					"id":"comment-123",
					"body":"Test comment",
					"issue":{
						"id":"issue-456",
						"title":"Parent issue"
					}
				}
			}`,
			wantIssueID: "issue-456",
			hasIssueID:  true,
		},
		{
			name: "comment event with numeric issue ID",
			payload: `{
				"type":"Comment",
				"action":"create",
				"data":{
					"id":"comment-123",
					"body":"Test comment",
					"issue":{
						"id":789,
						"title":"Parent issue"
					}
				}
			}`,
			wantIssueID: "789",
			hasIssueID:  true,
		},
		{
			name: "comment event without issue",
			payload: `{
				"type":"Comment",
				"action":"create",
				"data":{
					"id":"comment-123",
					"body":"Test comment"
				}
			}`,
			hasIssueID: false,
		},
		{
			name: "issue event should not have IssueID",
			payload: `{
				"type":"Issue",
				"action":"create",
				"data":{
					"id":"issue-123",
					"title":"Test issue"
				}
			}`,
			hasIssueID: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			eventData, err := ParseLinearWebhook([]byte(tt.payload))
			if err != nil {
				t.Errorf("ParseLinearWebhook() error = %v", err)
				return
			}

			vars := ExtractLinearWorkItem(eventData)

			issueID, hasIssueID := vars["IssueID"]
			if hasIssueID != tt.hasIssueID {
				t.Errorf("ExtractLinearWorkItem() has IssueID = %v, want %v", hasIssueID, tt.hasIssueID)
				return
			}

			if tt.hasIssueID {
				if issueIDStr, ok := issueID.(string); !ok || issueIDStr != tt.wantIssueID {
					t.Errorf("ExtractLinearWorkItem() IssueID = %v, want %v", issueID, tt.wantIssueID)
				}
			}
		})
	}
}

func TestExtractLinearWorkItem_StateAndLabels(t *testing.T) {
	payload := `{
		"type":"Issue",
		"action":"create",
		"data":{
			"id":"issue-123",
			"title":"Test Issue",
			"state":{"name":"Todo"},
			"labels":[
				{"name":"bug"},
				{"name":"urgent"}
			]
		}
	}`

	eventData, err := ParseLinearWebhook([]byte(payload))
	if err != nil {
		t.Fatalf("ParseLinearWebhook() error = %v", err)
	}

	vars := ExtractLinearWorkItem(eventData)

	if vars["State"] != "Todo" {
		t.Errorf("ExtractLinearWorkItem() State = %v, want %v", vars["State"], "Todo")
	}
	if vars["Labels"] != "bug, urgent" {
		t.Errorf("ExtractLinearWorkItem() Labels = %v, want %v", vars["Labels"], "bug, urgent")
	}
	if vars["ID"] != "issue-123" {
		t.Errorf("ExtractLinearWorkItem() ID = %v, want %v", vars["ID"], "issue-123")
	}
	if vars["Title"] != "Test Issue" {
		t.Errorf("ExtractLinearWorkItem() Title = %v, want %v", vars["Title"], "Test Issue")
	}
	if vars["Type"] != "Issue" {
		t.Errorf("ExtractLinearWorkItem() Type = %v, want %v", vars["Type"], "Issue")
	}
	if vars["Action"] != "create" {
		t.Errorf("ExtractLinearWorkItem() Action = %v, want %v", vars["Action"], "create")
	}
}

func TestParseLinearWebhook(t *testing.T) {
	tests := []struct {
		name     string
		payload  string
		wantType string
		wantID   string
		wantErr  bool
	}{
		{
			name: "issue event",
			payload: `{
				"type":"Issue",
				"action":"create",
				"data":{
					"id":"issue-123",
					"title":"Test Issue",
					"state":{"name":"Todo"}
				}
			}`,
			wantType: "Issue",
			wantID:   "issue-123",
			wantErr:  false,
		},
		{
			name: "comment event",
			payload: `{
				"type":"Comment",
				"action":"update",
				"data":{
					"id":"comment-456",
					"body":"Updated comment"
				}
			}`,
			wantType: "Comment",
			wantID:   "comment-456",
			wantErr:  false,
		},
		{
			name: "numeric ID",
			payload: `{
				"type":"Issue",
				"action":"create",
				"data":{
					"id":789,
					"title":"Numeric ID Issue"
				}
			}`,
			wantType: "Issue",
			wantID:   "789",
			wantErr:  false,
		},
		{
			name:    "invalid JSON",
			payload: `{invalid json}`,
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseLinearWebhook([]byte(tt.payload))
			if (err != nil) != tt.wantErr {
				t.Errorf("ParseLinearWebhook() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !tt.wantErr {
				if got.Type != tt.wantType {
					t.Errorf("ParseLinearWebhook() Type = %v, want %v", got.Type, tt.wantType)
				}
				if got.ID != tt.wantID {
					t.Errorf("ParseLinearWebhook() ID = %v, want %v", got.ID, tt.wantID)
				}
			}
		})
	}
}

func TestParseLinearWebhook_StateAndLabels(t *testing.T) {
	payload := `{
		"type":"Issue",
		"action":"create",
		"data":{
			"id":"issue-123",
			"title":"Test issue",
			"state":{"name":"Todo"},
			"labels":[
				{"name":"bug"},
				{"name":"urgent"}
			]
		}
	}`

	got, err := ParseLinearWebhook([]byte(payload))
	if err != nil {
		t.Fatalf("ParseLinearWebhook() error = %v", err)
	}

	if got.State != "Todo" {
		t.Errorf("ParseLinearWebhook() State = %v, want %v", got.State, "Todo")
	}
	if len(got.Labels) != 2 || got.Labels[0] != "bug" || got.Labels[1] != "urgent" {
		t.Errorf("ParseLinearWebhook() Labels = %v, want [bug urgent]", got.Labels)
	}
}

func TestSpawnerNeedsLinearLabels(t *testing.T) {
	tests := []struct {
		name    string
		spawner *v1alpha1.TaskSpawner
		payload string
		want    bool
	}{
		{
			name: "comment event with label filter and no labels in payload",
			spawner: &v1alpha1.TaskSpawner{
				Spec: v1alpha1.TaskSpawnerSpec{
					When: v1alpha1.When{
						LinearWebhook: &v1alpha1.LinearWebhook{
							Types: []string{"Comment"},
							Filters: []v1alpha1.LinearWebhookFilter{
								{Type: "Comment", Labels: []string{"bug"}},
							},
						},
					},
				},
			},
			payload: `{"type":"Comment","action":"create","data":{"id":"c1","issue":{"id":"i1","title":"Test"}}}`,
			want:    true,
		},
		{
			name: "comment event with excludeLabels filter and no labels in payload",
			spawner: &v1alpha1.TaskSpawner{
				Spec: v1alpha1.TaskSpawnerSpec{
					When: v1alpha1.When{
						LinearWebhook: &v1alpha1.LinearWebhook{
							Types: []string{"Comment"},
							Filters: []v1alpha1.LinearWebhookFilter{
								{Type: "Comment", ExcludeLabels: []string{"wontfix"}},
							},
						},
					},
				},
			},
			payload: `{"type":"Comment","action":"create","data":{"id":"c1","issue":{"id":"i1","title":"Test"}}}`,
			want:    true,
		},
		{
			name: "comment event with labels already in payload",
			spawner: &v1alpha1.TaskSpawner{
				Spec: v1alpha1.TaskSpawnerSpec{
					When: v1alpha1.When{
						LinearWebhook: &v1alpha1.LinearWebhook{
							Types: []string{"Comment"},
							Filters: []v1alpha1.LinearWebhookFilter{
								{Type: "Comment", Labels: []string{"bug"}},
							},
						},
					},
				},
			},
			payload: `{"type":"Comment","action":"create","data":{"id":"c1","issue":{"id":"i1","labels":[{"name":"bug"}]}}}`,
			want:    false,
		},
		{
			name: "comment event with no label filter",
			spawner: &v1alpha1.TaskSpawner{
				Spec: v1alpha1.TaskSpawnerSpec{
					When: v1alpha1.When{
						LinearWebhook: &v1alpha1.LinearWebhook{
							Types: []string{"Comment"},
							Filters: []v1alpha1.LinearWebhookFilter{
								{Type: "Comment", Action: "create"},
							},
						},
					},
				},
			},
			payload: `{"type":"Comment","action":"create","data":{"id":"c1","issue":{"id":"i1"}}}`,
			want:    false,
		},
		{
			name: "issue event is not enriched",
			spawner: &v1alpha1.TaskSpawner{
				Spec: v1alpha1.TaskSpawnerSpec{
					When: v1alpha1.When{
						LinearWebhook: &v1alpha1.LinearWebhook{
							Types: []string{"Issue"},
							Filters: []v1alpha1.LinearWebhookFilter{
								{Type: "Issue", Labels: []string{"bug"}},
							},
						},
					},
				},
			},
			payload: `{"type":"Issue","action":"create","data":{"id":"i1","title":"Test"}}`,
			want:    false,
		},
		{
			name: "label filter on Issue type does not trigger enrichment for Comment",
			spawner: &v1alpha1.TaskSpawner{
				Spec: v1alpha1.TaskSpawnerSpec{
					When: v1alpha1.When{
						LinearWebhook: &v1alpha1.LinearWebhook{
							Types: []string{"Issue", "Comment"},
							Filters: []v1alpha1.LinearWebhookFilter{
								{Type: "Issue", Labels: []string{"bug"}},
								{Type: "Comment", Action: "create"},
							},
						},
					},
				},
			},
			payload: `{"type":"Comment","action":"create","data":{"id":"c1","issue":{"id":"i1"}}}`,
			want:    false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			eventData, err := ParseLinearWebhook([]byte(tt.payload))
			if err != nil {
				t.Fatalf("ParseLinearWebhook() error = %v", err)
			}
			got := spawnerNeedsLinearLabels(tt.spawner, eventData)
			if got != tt.want {
				t.Errorf("spawnerNeedsLinearLabels() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestEnrichLinearCommentLabels(t *testing.T) {
	// Set up a mock Linear API server that returns labels for the issue
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req linearGraphQLRequest
		json.NewDecoder(r.Body).Decode(&req)

		resp := map[string]interface{}{
			"data": map[string]interface{}{
				"issue": map[string]interface{}{
					"labels": map[string]interface{}{
						"nodes": []map[string]interface{}{
							{"name": "bug"},
							{"name": "priority:high"},
						},
					},
				},
			},
		}
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	// Override fetchLinearIssueLabels for this test by using a payload
	// that enrichLinearCommentLabels will process, and swap in a test
	// fetcher.
	origFetcher := linearLabelFetcher
	linearLabelFetcher = func(ctx context.Context, issueID string) ([]string, error) {
		return fetchLinearIssueLabelsFromURL(ctx, server.URL, "test-key", issueID)
	}
	defer func() { linearLabelFetcher = origFetcher }()

	payload := `{
		"type":"Comment",
		"action":"create",
		"data":{
			"id":"comment-123",
			"body":"Test comment",
			"issue":{
				"id":"issue-456",
				"title":"Parent issue"
			}
		}
	}`

	eventData, err := ParseLinearWebhook([]byte(payload))
	if err != nil {
		t.Fatalf("ParseLinearWebhook() error = %v", err)
	}

	enrichLinearCommentLabels(context.Background(), logr.Discard(), eventData)

	// Check that labels were injected into the payload
	dataObj := eventData.Payload["data"].(map[string]interface{})
	issue := dataObj["issue"].(map[string]interface{})
	labels, ok := issue["labels"].([]interface{})
	if !ok {
		t.Fatal("Expected labels to be injected into issue")
	}
	if len(labels) != 2 {
		t.Fatalf("Expected 2 labels, got %d", len(labels))
	}

	label0 := labels[0].(map[string]interface{})
	if label0["name"] != "bug" {
		t.Errorf("Expected first label 'bug', got %v", label0["name"])
	}
	label1 := labels[1].(map[string]interface{})
	if label1["name"] != "priority:high" {
		t.Errorf("Expected second label 'priority:high', got %v", label1["name"])
	}

	// Check convenience field was also updated
	if len(eventData.Labels) != 2 || eventData.Labels[0] != "bug" || eventData.Labels[1] != "priority:high" {
		t.Errorf("Expected eventData.Labels = [bug priority:high], got %v", eventData.Labels)
	}
}

func TestEnrichLinearCommentLabels_MatchesFilterAfterEnrichment(t *testing.T) {
	// End-to-end test: Comment payload without labels -> enrich -> filter matches
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := map[string]interface{}{
			"data": map[string]interface{}{
				"issue": map[string]interface{}{
					"labels": map[string]interface{}{
						"nodes": []map[string]interface{}{
							{"name": "bug"},
							{"name": "priority:high"},
						},
					},
				},
			},
		}
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	origFetcher := linearLabelFetcher
	linearLabelFetcher = func(ctx context.Context, issueID string) ([]string, error) {
		return fetchLinearIssueLabelsFromURL(ctx, server.URL, "test-key", issueID)
	}
	defer func() { linearLabelFetcher = origFetcher }()

	config := &v1alpha1.LinearWebhook{
		Types: []string{"Comment"},
		Filters: []v1alpha1.LinearWebhookFilter{
			{Type: "Comment", Labels: []string{"bug"}},
		},
	}

	// Comment payload without labels — should NOT match before enrichment
	payload := `{
		"type":"Comment",
		"action":"create",
		"data":{
			"id":"comment-123",
			"body":"Test comment",
			"issue":{"id":"issue-456","title":"Parent issue"}
		}
	}`

	eventData, err := ParseLinearWebhook([]byte(payload))
	if err != nil {
		t.Fatalf("ParseLinearWebhook() error = %v", err)
	}

	// Before enrichment — filter should not match
	matched, err := MatchesLinearEvent(config, eventData)
	if err != nil {
		t.Fatalf("MatchesLinearEvent() error = %v", err)
	}
	if matched {
		t.Error("Expected no match before enrichment")
	}

	// Enrich
	enrichLinearCommentLabels(context.Background(), logr.Discard(), eventData)

	// After enrichment — filter should match
	matched, err = MatchesLinearEvent(config, eventData)
	if err != nil {
		t.Fatalf("MatchesLinearEvent() error = %v", err)
	}
	if !matched {
		t.Error("Expected match after enrichment")
	}
}
