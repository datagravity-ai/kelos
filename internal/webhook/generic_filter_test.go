package webhook

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kelos-dev/kelos/api/v1alpha1"
)

func TestParseGenericWebhook(t *testing.T) {
	tests := []struct {
		name    string
		payload string
		wantErr bool
	}{
		{
			name:    "valid JSON object",
			payload: `{"action":"created","data":{"id":"123"}}`,
			wantErr: false,
		},
		{
			name:    "valid JSON array",
			payload: `[{"id":"1"},{"id":"2"}]`,
			wantErr: false,
		},
		{
			name:    "invalid JSON",
			payload: `not json`,
			wantErr: true,
		},
		{
			name:    "empty object",
			payload: `{}`,
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := ParseGenericWebhook([]byte(tt.payload))
			if tt.wantErr {
				assert.Error(t, err)
				assert.Nil(t, result)
			} else {
				assert.NoError(t, err)
				assert.NotNil(t, result)
				assert.NotNil(t, result.Fields)
				assert.NotNil(t, result.Payload)
			}
		})
	}
}

func TestExtractFields(t *testing.T) {
	payload := `{
		"action": "created",
		"data": {
			"event": {
				"event_id": "evt-123",
				"title": "Something broke",
				"level": "error"
			},
			"url": "https://sentry.io/issues/123"
		}
	}`

	eventData, err := ParseGenericWebhook([]byte(payload))
	require.NoError(t, err)

	fieldMapping := map[string]string{
		"id":    "$.data.event.event_id",
		"title": "$.data.event.title",
		"url":   "$.data.url",
		"level": "$.data.event.level",
	}

	err = eventData.ExtractFields(fieldMapping)
	assert.NoError(t, err)

	assert.Equal(t, "evt-123", eventData.Fields["id"])
	assert.Equal(t, "Something broke", eventData.Fields["title"])
	assert.Equal(t, "https://sentry.io/issues/123", eventData.Fields["url"])
	assert.Equal(t, "error", eventData.Fields["level"])
}

func TestExtractFields_MissingFields(t *testing.T) {
	payload := `{"action": "created"}`

	eventData, err := ParseGenericWebhook([]byte(payload))
	require.NoError(t, err)

	fieldMapping := map[string]string{
		"id":    "$.data.id",
		"title": "$.data.title",
	}

	err = eventData.ExtractFields(fieldMapping)
	assert.NoError(t, err)

	// Missing fields produce empty strings, not errors
	assert.Equal(t, "", eventData.Fields["id"])
	assert.Equal(t, "", eventData.Fields["title"])
}

func TestExtractFields_NestedArrayAccess(t *testing.T) {
	payload := `{
		"data": {
			"properties": {
				"Name": {
					"title": [{"plain_text": "My Task"}]
				}
			}
		}
	}`

	eventData, err := ParseGenericWebhook([]byte(payload))
	require.NoError(t, err)

	fieldMapping := map[string]string{
		"title": "$.data.properties.Name.title[0].plain_text",
	}

	err = eventData.ExtractFields(fieldMapping)
	assert.NoError(t, err)

	assert.Equal(t, "My Task", eventData.Fields["title"])
}

func strPtr(s string) *string {
	return &s
}

func TestMatchesGenericFilters_NoFilters(t *testing.T) {
	payload := `{"action": "created"}`
	eventData, err := ParseGenericWebhook([]byte(payload))
	require.NoError(t, err)

	matched, err := MatchesGenericFilters(nil, eventData.Payload)
	assert.NoError(t, err)
	assert.True(t, matched)
}

func TestMatchesGenericFilters_ExactMatch(t *testing.T) {
	payload := `{"action": "created", "level": "error"}`
	eventData, err := ParseGenericWebhook([]byte(payload))
	require.NoError(t, err)

	tests := []struct {
		name    string
		filters []v1alpha1.GenericWebhookFilter
		want    bool
	}{
		{
			name: "matches exact value",
			filters: []v1alpha1.GenericWebhookFilter{
				{Field: "$.action", Value: strPtr("created")},
			},
			want: true,
		},
		{
			name: "does not match different value",
			filters: []v1alpha1.GenericWebhookFilter{
				{Field: "$.action", Value: strPtr("updated")},
			},
			want: false,
		},
		{
			name: "AND semantics - all must match",
			filters: []v1alpha1.GenericWebhookFilter{
				{Field: "$.action", Value: strPtr("created")},
				{Field: "$.level", Value: strPtr("error")},
			},
			want: true,
		},
		{
			name: "AND semantics - one fails",
			filters: []v1alpha1.GenericWebhookFilter{
				{Field: "$.action", Value: strPtr("created")},
				{Field: "$.level", Value: strPtr("warning")},
			},
			want: false,
		},
		{
			name: "missing field fails filter",
			filters: []v1alpha1.GenericWebhookFilter{
				{Field: "$.nonexistent", Value: strPtr("anything")},
			},
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			matched, err := MatchesGenericFilters(tt.filters, eventData.Payload)
			assert.NoError(t, err)
			assert.Equal(t, tt.want, matched)
		})
	}
}

func TestMatchesGenericFilters_RegexMatch(t *testing.T) {
	payload := `{"platform": "python-django", "level": "error"}`
	eventData, err := ParseGenericWebhook([]byte(payload))
	require.NoError(t, err)

	tests := []struct {
		name    string
		filters []v1alpha1.GenericWebhookFilter
		want    bool
		wantErr bool
	}{
		{
			name: "matches regex pattern",
			filters: []v1alpha1.GenericWebhookFilter{
				{Field: "$.platform", Pattern: "python|go|node"},
			},
			want: true,
		},
		{
			name: "does not match regex pattern",
			filters: []v1alpha1.GenericWebhookFilter{
				{Field: "$.platform", Pattern: "^ruby"},
			},
			want: false,
		},
		{
			name: "invalid regex returns error",
			filters: []v1alpha1.GenericWebhookFilter{
				{Field: "$.platform", Pattern: "[invalid"},
			},
			want:    false,
			wantErr: true,
		},
		{
			name: "mixed exact and regex filters",
			filters: []v1alpha1.GenericWebhookFilter{
				{Field: "$.level", Value: strPtr("error")},
				{Field: "$.platform", Pattern: "python"},
			},
			want: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			matched, err := MatchesGenericFilters(tt.filters, eventData.Payload)
			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
			assert.Equal(t, tt.want, matched)
		})
	}
}

func TestMatchesGenericFilters_NestedFields(t *testing.T) {
	payload := `{
		"action": "created",
		"data": {
			"event": {
				"level": "error",
				"platform": "go"
			}
		}
	}`
	eventData, err := ParseGenericWebhook([]byte(payload))
	require.NoError(t, err)

	filters := []v1alpha1.GenericWebhookFilter{
		{Field: "$.data.event.level", Value: strPtr("error")},
		{Field: "$.data.event.platform", Pattern: "go|python"},
	}

	matched, err := MatchesGenericFilters(filters, eventData.Payload)
	assert.NoError(t, err)
	assert.True(t, matched)
}

func TestMatchesGenericFilters_NumericValues(t *testing.T) {
	payload := `{"status_code": 500, "retry_count": 3}`
	eventData, err := ParseGenericWebhook([]byte(payload))
	require.NoError(t, err)

	// Numeric values are converted to strings via fmt.Sprintf("%v")
	filters := []v1alpha1.GenericWebhookFilter{
		{Field: "$.status_code", Value: strPtr("500")},
	}

	matched, err := MatchesGenericFilters(filters, eventData.Payload)
	assert.NoError(t, err)
	assert.True(t, matched)
}

func TestMatchesGenericFilters_BooleanValues(t *testing.T) {
	payload := `{"critical": true}`
	eventData, err := ParseGenericWebhook([]byte(payload))
	require.NoError(t, err)

	filters := []v1alpha1.GenericWebhookFilter{
		{Field: "$.critical", Value: strPtr("true")},
	}

	matched, err := MatchesGenericFilters(filters, eventData.Payload)
	assert.NoError(t, err)
	assert.True(t, matched)
}

func TestExtractGenericWorkItem(t *testing.T) {
	eventData := &GenericEventData{
		Fields: map[string]string{
			"id":       "evt-123",
			"title":    "Something broke",
			"severity": "critical",
		},
		Payload: map[string]interface{}{"key": "value"},
	}

	result := ExtractGenericWorkItem(eventData)

	// Lowercase keys from fieldMapping are preserved
	assert.Equal(t, "evt-123", result["id"])
	assert.Equal(t, "Something broke", result["title"])
	assert.Equal(t, "critical", result["severity"])
	// Canonical uppercase aliases are promoted for compatibility with
	// GitHub/Linear source templates
	assert.Equal(t, "evt-123", result["ID"])
	assert.Equal(t, "Something broke", result["Title"])
	assert.Equal(t, "GenericWebhook", result["Kind"])
	assert.Equal(t, map[string]interface{}{"key": "value"}, result["Payload"])
	// Standard fields that aren't mapped should have empty defaults
	assert.Equal(t, "", result["Body"])
	assert.Equal(t, "", result["URL"])
}

func TestExtractGenericWorkItem_UppercaseKeysNotOverwritten(t *testing.T) {
	// If the user explicitly maps uppercase keys, they should not be
	// clobbered by the canonical promotion or empty defaults.
	eventData := &GenericEventData{
		Fields: map[string]string{
			"ID":    "explicit-id",
			"Title": "explicit-title",
		},
		Payload: map[string]interface{}{},
	}

	result := ExtractGenericWorkItem(eventData)

	assert.Equal(t, "explicit-id", result["ID"])
	assert.Equal(t, "explicit-title", result["Title"])
}

func TestExtractGenericWorkItem_StandardFieldsDefaulted(t *testing.T) {
	eventData := &GenericEventData{
		Fields:  map[string]string{},
		Payload: map[string]interface{}{},
	}

	result := ExtractGenericWorkItem(eventData)

	assert.Equal(t, "", result["ID"])
	assert.Equal(t, "", result["Title"])
	assert.Equal(t, "", result["Body"])
	assert.Equal(t, "", result["URL"])
}

func TestExtractSourceFromPath(t *testing.T) {
	tests := []struct {
		path string
		want string
	}{
		{"/webhook/notion", "notion"},
		{"/webhook/sentry", "sentry"},
		{"/webhook/my-source", "my-source"},
		{"/webhook/notion/", "notion"},
		{"/webhook/", ""},
		{"/webhook", ""},
		{"/", ""},
		{"/other/path", ""},
		{"/webhook/a/b", ""},
	}

	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			got := extractSourceFromPath(tt.path)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestValidateGenericSignature(t *testing.T) {
	payload := []byte(`{"test": true}`)
	secret := []byte("my-secret")
	sig := computeHMAC(payload, secret)

	tests := []struct {
		name      string
		signature string
		prefix    string
		wantErr   bool
	}{
		{
			name:      "valid signature with sha256= prefix",
			signature: "sha256=" + sig,
			prefix:    "sha256=",
			wantErr:   false,
		},
		{
			name:      "valid signature without prefix",
			signature: sig,
			prefix:    "",
			wantErr:   false,
		},
		{
			name:      "invalid signature",
			signature: "sha256=invalid",
			prefix:    "sha256=",
			wantErr:   true,
		},
		{
			name:      "missing prefix",
			signature: sig,
			prefix:    "sha256=",
			wantErr:   true,
		},
		{
			name:      "empty signature",
			signature: "",
			prefix:    "",
			wantErr:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateGenericSignature(payload, tt.signature, secret, tt.prefix)
			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}
