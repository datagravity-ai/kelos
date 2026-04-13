package slack

import (
	"testing"

	"github.com/kelos-dev/kelos/api/v1alpha1"
)

func TestMatchesSpawner(t *testing.T) {
	tests := []struct {
		name     string
		slackCfg *v1alpha1.Slack
		msg      *SlackMessageData
		want     bool
	}{
		{
			name:     "nil slack config",
			slackCfg: nil,
			msg:      &SlackMessageData{UserID: "U1", ChannelID: "C1"},
			want:     false,
		},
		{
			name:     "no filters matches everything",
			slackCfg: &v1alpha1.Slack{},
			msg:      &SlackMessageData{UserID: "U1", ChannelID: "C1"},
			want:     true,
		},
		{
			name: "channel filter matches",
			slackCfg: &v1alpha1.Slack{
				Channels: []string{"C1", "C2"},
			},
			msg:  &SlackMessageData{UserID: "U1", ChannelID: "C1"},
			want: true,
		},
		{
			name: "channel filter rejects",
			slackCfg: &v1alpha1.Slack{
				Channels: []string{"C2", "C3"},
			},
			msg:  &SlackMessageData{UserID: "U1", ChannelID: "C1"},
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := MatchesSpawner(tt.slackCfg, tt.msg)
			if got != tt.want {
				t.Errorf("MatchesSpawner() = %v, want %v", got, tt.want)
			}
		})
	}
}

func boolPtr(v bool) *bool { return &v }

func TestMatchesTriggers(t *testing.T) {
	tests := []struct {
		name      string
		text      string
		triggers  []v1alpha1.SlackTrigger
		botUserID string
		want      bool
	}{
		{
			name:      "no triggers matches everything",
			text:      "hello world",
			triggers:  nil,
			botUserID: "UBOT",
			want:      true,
		},
		{
			name:      "trigger matches with mention",
			text:      "<@UBOT> /triage this issue",
			triggers:  []v1alpha1.SlackTrigger{{Pattern: "/triage"}},
			botUserID: "UBOT",
			want:      true,
		},
		{
			name:      "trigger matches mentionOptional",
			text:      "/triage this issue",
			triggers:  []v1alpha1.SlackTrigger{{Pattern: "^/triage", MentionOptional: boolPtr(true)}},
			botUserID: "UBOT",
			want:      true,
		},
		{
			name:      "trigger matches but no mention and not optional",
			text:      "/triage this issue",
			triggers:  []v1alpha1.SlackTrigger{{Pattern: "^/triage"}},
			botUserID: "UBOT",
			want:      false,
		},
		{
			name:      "trigger no match",
			text:      "unrelated message <@UBOT>",
			triggers:  []v1alpha1.SlackTrigger{{Pattern: "^/triage"}},
			botUserID: "UBOT",
			want:      false,
		},
		{
			name: "multiple triggers OR semantics",
			text: "I need help <@UBOT>",
			triggers: []v1alpha1.SlackTrigger{
				{Pattern: "^/triage"},
				{Pattern: "help"},
			},
			botUserID: "UBOT",
			want:      true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := MatchesTriggers(tt.text, tt.triggers, tt.botUserID)
			if got != tt.want {
				t.Errorf("MatchesTriggers(%q) = %v, want %v", tt.text, got, tt.want)
			}
		})
	}
}

func TestExtractSlackWorkItem(t *testing.T) {
	t.Run("regular message", func(t *testing.T) {
		msg := &SlackMessageData{
			UserID:    "U123",
			UserName:  "Alice",
			Body:      "fix the login page",
			Timestamp: "1234567890.123456",
			Permalink: "https://slack.com/archives/C1/p1234567890123456",
		}

		vars := ExtractSlackWorkItem(msg)

		if vars["ID"] != "1234567890.123456" {
			t.Errorf("ID = %v, want %v", vars["ID"], "1234567890.123456")
		}
		if vars["Title"] != "Alice" {
			t.Errorf("Title = %v, want %v", vars["Title"], "Alice")
		}
		if vars["Body"] != "fix the login page" {
			t.Errorf("Body = %v, want %v", vars["Body"], "fix the login page")
		}
		if vars["URL"] != "https://slack.com/archives/C1/p1234567890123456" {
			t.Errorf("URL = %v, want %v", vars["URL"], msg.Permalink)
		}
		if vars["Kind"] != "SlackMessage" {
			t.Errorf("Kind = %v, want %v", vars["Kind"], "SlackMessage")
		}
	})

	t.Run("slash command uses composite ID", func(t *testing.T) {
		msg := &SlackMessageData{
			UserID:         "U123",
			UserName:       "Alice",
			Body:           "do something",
			IsSlashCommand: true,
			SlashCommandID: "C1:/kelos:trigger123",
		}

		vars := ExtractSlackWorkItem(msg)

		if vars["ID"] != "C1:/kelos:trigger123" {
			t.Errorf("ID = %v, want %v", vars["ID"], "C1:/kelos:trigger123")
		}
	})
}

func TestShouldProcess(t *testing.T) {
	tests := []struct {
		name       string
		userID     string
		subtype    string
		hasContent bool
		selfUserID string
		want       bool
	}{
		{
			name:       "normal message",
			userID:     "U1",
			hasContent: true,
			selfUserID: "UBOT",
			want:       true,
		},
		{
			name:       "self message filtered",
			userID:     "UBOT",
			hasContent: true,
			selfUserID: "UBOT",
			want:       false,
		},
		{
			name:       "bot_message subtype filtered",
			userID:     "U1",
			subtype:    "bot_message",
			hasContent: true,
			selfUserID: "UBOT",
			want:       false,
		},
		{
			name:       "message_changed subtype filtered",
			userID:     "U1",
			subtype:    "message_changed",
			hasContent: true,
			selfUserID: "UBOT",
			want:       false,
		},
		{
			name:       "message_deleted subtype filtered",
			userID:     "U1",
			subtype:    "message_deleted",
			hasContent: true,
			selfUserID: "UBOT",
			want:       false,
		},
		{
			name:       "message_replied subtype filtered",
			userID:     "U1",
			subtype:    "message_replied",
			hasContent: true,
			selfUserID: "UBOT",
			want:       false,
		},
		{
			name:       "no content filtered",
			userID:     "U1",
			hasContent: false,
			selfUserID: "UBOT",
			want:       false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := shouldProcess(tt.userID, tt.subtype, tt.hasContent, tt.selfUserID)
			if got != tt.want {
				t.Errorf("shouldProcess() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestMatchesChannel(t *testing.T) {
	tests := []struct {
		name      string
		channelID string
		allowed   []string
		want      bool
	}{
		{"empty allowed list matches all", "C1", nil, true},
		{"in allowed list", "C1", []string{"C1", "C2"}, true},
		{"not in allowed list", "C3", []string{"C1", "C2"}, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := matchesChannel(tt.channelID, tt.allowed); got != tt.want {
				t.Errorf("matchesChannel() = %v, want %v", got, tt.want)
			}
		})
	}
}
