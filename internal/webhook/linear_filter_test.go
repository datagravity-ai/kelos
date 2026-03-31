package webhook

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/kelos-dev/kelos/api/v1alpha1"
)

func TestParseLinearWebhook(t *testing.T) {
	tests := []struct {
		name     string
		payload  string
		expected *LinearEventData
		wantErr  bool
	}{
		{
			name: "issue created event",
			payload: `{
				"type": "Issue",
				"action": "create",
				"data": {
					"id": "issue-123",
					"title": "Test issue",
					"state": {"name": "Todo"},
					"labels": [
						{"name": "bug"},
						{"name": "urgent"}
					]
				}
			}`,
			expected: &LinearEventData{
				ID:     "issue-123",
				Title:  "Test issue",
				Type:   "Issue",
				Action: "create",
				State:  "Todo",
				Labels: []string{"bug", "urgent"},
			},
			wantErr: false,
		},
		{
			name: "comment created event",
			payload: `{
				"type": "Comment",
				"action": "create",
				"data": {
					"id": "comment-456",
					"body": "This is a comment"
				}
			}`,
			expected: &LinearEventData{
				ID:     "comment-456",
				Title:  "",
				Type:   "Comment",
				Action: "create",
				State:  "",
				Labels: nil,
			},
			wantErr: false,
		},
		{
			name:     "invalid JSON",
			payload:  `invalid json`,
			expected: nil,
			wantErr:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := ParseLinearWebhook([]byte(tt.payload))

			if tt.wantErr {
				assert.Error(t, err)
				assert.Nil(t, result)
			} else {
				assert.NoError(t, err)
				assert.NotNil(t, result)
				assert.Equal(t, tt.expected.ID, result.ID)
				assert.Equal(t, tt.expected.Title, result.Title)
				assert.Equal(t, tt.expected.Type, result.Type)
				assert.Equal(t, tt.expected.Action, result.Action)
				assert.Equal(t, tt.expected.State, result.State)
				assert.Equal(t, tt.expected.Labels, result.Labels)
			}
		})
	}
}

func TestMatchesLinearEvent(t *testing.T) {
	tests := []struct {
		name     string
		config   *v1alpha1.LinearWebhook
		payload  string
		expected bool
		wantErr  bool
	}{
		{
			name: "matches allowed type with no filters",
			config: &v1alpha1.LinearWebhook{
				Types: []string{"Issue"},
			},
			payload: `{
				"type": "Issue",
				"action": "create",
				"data": {"id": "123", "title": "Test"}
			}`,
			expected: true,
			wantErr:  false,
		},
		{
			name: "does not match disallowed type",
			config: &v1alpha1.LinearWebhook{
				Types: []string{"Comment"},
			},
			payload: `{
				"type": "Issue",
				"action": "create",
				"data": {"id": "123", "title": "Test"}
			}`,
			expected: false,
			wantErr:  false,
		},
		{
			name: "matches with action filter",
			config: &v1alpha1.LinearWebhook{
				Types: []string{"Issue"},
				Filters: []v1alpha1.LinearWebhookFilter{
					{
						Type:   "Issue",
						Action: "create",
					},
				},
			},
			payload: `{
				"type": "Issue",
				"action": "create",
				"data": {"id": "123", "title": "Test"}
			}`,
			expected: true,
			wantErr:  false,
		},
		{
			name: "does not match wrong action",
			config: &v1alpha1.LinearWebhook{
				Types: []string{"Issue"},
				Filters: []v1alpha1.LinearWebhookFilter{
					{
						Type:   "Issue",
						Action: "update",
					},
				},
			},
			payload: `{
				"type": "Issue",
				"action": "create",
				"data": {"id": "123", "title": "Test"}
			}`,
			expected: false,
			wantErr:  false,
		},
		{
			name: "matches with state filter",
			config: &v1alpha1.LinearWebhook{
				Types: []string{"Issue"},
				Filters: []v1alpha1.LinearWebhookFilter{
					{
						Type:   "Issue",
						States: []string{"Todo", "In Progress"},
					},
				},
			},
			payload: `{
				"type": "Issue",
				"action": "create",
				"data": {
					"id": "123",
					"title": "Test",
					"state": {"name": "Todo"}
				}
			}`,
			expected: true,
			wantErr:  false,
		},
		{
			name: "does not match wrong state",
			config: &v1alpha1.LinearWebhook{
				Types: []string{"Issue"},
				Filters: []v1alpha1.LinearWebhookFilter{
					{
						Type:   "Issue",
						States: []string{"Done"},
					},
				},
			},
			payload: `{
				"type": "Issue",
				"action": "create",
				"data": {
					"id": "123",
					"title": "Test",
					"state": {"name": "Todo"}
				}
			}`,
			expected: false,
			wantErr:  false,
		},
		{
			name: "matches with required labels",
			config: &v1alpha1.LinearWebhook{
				Types: []string{"Issue"},
				Filters: []v1alpha1.LinearWebhookFilter{
					{
						Type:   "Issue",
						Labels: []string{"bug"},
					},
				},
			},
			payload: `{
				"type": "Issue",
				"action": "create",
				"data": {
					"id": "123",
					"title": "Test",
					"labels": [{"name": "bug"}, {"name": "urgent"}]
				}
			}`,
			expected: true,
			wantErr:  false,
		},
		{
			name: "does not match missing required label",
			config: &v1alpha1.LinearWebhook{
				Types: []string{"Issue"},
				Filters: []v1alpha1.LinearWebhookFilter{
					{
						Type:   "Issue",
						Labels: []string{"feature"},
					},
				},
			},
			payload: `{
				"type": "Issue",
				"action": "create",
				"data": {
					"id": "123",
					"title": "Test",
					"labels": [{"name": "bug"}, {"name": "urgent"}]
				}
			}`,
			expected: false,
			wantErr:  false,
		},
		{
			name: "excludes based on exclude labels",
			config: &v1alpha1.LinearWebhook{
				Types: []string{"Issue"},
				Filters: []v1alpha1.LinearWebhookFilter{
					{
						Type:          "Issue",
						ExcludeLabels: []string{"wontfix"},
					},
				},
			},
			payload: `{
				"type": "Issue",
				"action": "create",
				"data": {
					"id": "123",
					"title": "Test",
					"labels": [{"name": "bug"}, {"name": "wontfix"}]
				}
			}`,
			expected: false,
			wantErr:  false,
		},
		{
			name:     "invalid JSON payload",
			config:   &v1alpha1.LinearWebhook{Types: []string{"Issue"}},
			payload:  `invalid json`,
			expected: false,
			wantErr:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := MatchesLinearEvent(tt.config, []byte(tt.payload))

			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				assert.Equal(t, tt.expected, result)
			}
		})
	}
}

func TestExtractLinearWorkItem(t *testing.T) {
	eventData := &LinearEventData{
		ID:      "issue-123",
		Title:   "Test Issue",
		Type:    "Issue",
		Action:  "create",
		State:   "Todo",
		Labels:  []string{"bug", "urgent"},
		Payload: map[string]interface{}{"key": "value"},
	}

	result := ExtractLinearWorkItem(eventData)

	expected := map[string]interface{}{
		"ID":      "issue-123",
		"Title":   "Test Issue",
		"Kind":    "LinearWebhook",
		"Type":    "Issue",
		"Action":  "create",
		"State":   "Todo",
		"Labels":  "bug, urgent",
		"Payload": map[string]interface{}{"key": "value"},
	}

	assert.Equal(t, expected, result)
}
