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
		name            string
		text            string
		triggers        []v1alpha1.SlackTrigger
		botUserID       string
		excludePatterns []string
		want            bool
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
		{
			name:            "excludePatterns rejects matching message",
			text:            "<@UBOT1> /solve fix this",
			excludePatterns: []string{"^/solve"},
			botUserID:       "UBOT1",
			want:            false,
		},
		{
			name:            "excludePatterns allows non-matching message",
			text:            "<@UBOT1> this is broken",
			excludePatterns: []string{"^/solve"},
			botUserID:       "UBOT1",
			want:            true,
		},
		{
			name:            "excludePatterns multiple patterns OR semantics",
			text:            "<@UBOT1> /deploy now",
			excludePatterns: []string{"^/solve", "^/deploy"},
			botUserID:       "UBOT1",
			want:            false,
		},
		{
			name:            "excludePatterns applied to thread replies",
			text:            "<@UBOT1> /solve go",
			excludePatterns: []string{"^/solve"},
			botUserID:       "UBOT1",
			want:            false,
		},
		{
			name:            "excludePatterns allows non-matching thread reply",
			text:            "<@UBOT1> more context",
			excludePatterns: []string{"^/solve"},
			botUserID:       "UBOT1",
			want:            true,
		},
		{
			name:            "excludePatterns invalid regex skipped",
			text:            "<@UBOT1> /solve fix",
			excludePatterns: []string{"[invalid", "^/solve"},
			botUserID:       "UBOT1",
			want:            false,
		},
		{
			name:            "excludePatterns empty list has no effect",
			text:            "<@UBOT1> anything",
			excludePatterns: []string{},
			botUserID:       "UBOT1",
			want:            true,
		},
		{
			name:            "excludePatterns with triggers both must pass",
			text:            "<@UBOT1> fix the /solve issue",
			triggers:        []v1alpha1.SlackTrigger{{Pattern: "fix"}},
			excludePatterns: []string{"^/solve"},
			botUserID:       "UBOT1",
			want:            true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := MatchesTriggers(tt.text, tt.triggers, tt.botUserID, tt.excludePatterns)
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

func TestHasBotMention(t *testing.T) {
	tests := []struct {
		name      string
		text      string
		botUserID string
		want      bool
	}{
		{"mention present", "hey <@UBOT1> fix", "UBOT1", true},
		{"mention with display name", "hey <@UBOT1|kelos-bot> fix", "UBOT1", true},
		{"mention absent", "hey fix this", "UBOT1", false},
		{"empty bot user ID", "hey <@UBOT1> fix", "", false},
		{"partial ID does not match", "hey <@UBOT10> fix", "UBOT1", false},
		{"mention without angle brackets", "hey @UBOT1 fix", "UBOT1", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := hasBotMention(tt.text, tt.botUserID); got != tt.want {
				t.Errorf("hasBotMention() = %v, want %v", got, tt.want)
			}
		})
	}
}

func Test_matchesTriggers(t *testing.T) {
	tests := []struct {
		name      string
		text      string
		triggers  []v1alpha1.SlackTrigger
		botUserID string
		want      bool
	}{
		{
			name:      "pattern matches with mention",
			text:      "<@UBOT1> deploy prod",
			triggers:  []v1alpha1.SlackTrigger{{Pattern: "deploy"}},
			botUserID: "UBOT1",
			want:      true,
		},
		{
			name:      "pattern matches without mention requires mention",
			text:      "deploy prod",
			triggers:  []v1alpha1.SlackTrigger{{Pattern: "deploy"}},
			botUserID: "UBOT1",
			want:      false,
		},
		{
			name:      "mentionOptional allows pattern only",
			text:      "deploy prod",
			triggers:  []v1alpha1.SlackTrigger{{Pattern: "deploy", MentionOptional: boolPtr(true)}},
			botUserID: "UBOT1",
			want:      true,
		},
		{
			name:      "pattern does not match",
			text:      "<@UBOT1> rollback",
			triggers:  []v1alpha1.SlackTrigger{{Pattern: "deploy"}},
			botUserID: "UBOT1",
			want:      false,
		},
		{
			name: "OR semantics across triggers",
			text: "<@UBOT1> rollback",
			triggers: []v1alpha1.SlackTrigger{
				{Pattern: "deploy"},
				{Pattern: "rollback"},
			},
			botUserID: "UBOT1",
			want:      true,
		},
		{
			name:      "invalid regex skipped",
			text:      "<@UBOT1> fix it",
			triggers:  []v1alpha1.SlackTrigger{{Pattern: "[invalid"}, {Pattern: "fix"}},
			botUserID: "UBOT1",
			want:      true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := matchesTriggers(tt.text, tt.triggers, tt.botUserID); got != tt.want {
				t.Errorf("matchesTriggers() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestStripLeadingMentions(t *testing.T) {
	tests := []struct {
		name string
		text string
		want string
	}{
		{"no mention", "hello world", "hello world"},
		{"single mention", "<@UBOT1> hello", "hello"},
		{"mention with display name", "<@UBOT1|kelos-bot> hello", "hello"},
		{"multiple mentions", "<@U1> <@U2> hello", "hello"},
		{"mention only", "<@UBOT1>", ""},
		{"empty string", "", ""},
		{"mention mid-text preserved", "hello <@UBOT1> world", "hello <@UBOT1> world"},
		{"malformed mention no closing bracket", "<@UBOT1 hello", "<@UBOT1 hello"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := stripLeadingMentions(tt.text); got != tt.want {
				t.Errorf("stripLeadingMentions() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestMatchesExcludePatterns(t *testing.T) {
	tests := []struct {
		name     string
		text     string
		patterns []string
		want     bool
	}{
		{"empty list never matches", "/solve fix", nil, false},
		{"anchored pattern matches", "/solve fix", []string{"^/solve"}, true},
		{"non-matching pattern", "/triage check", []string{"^/solve"}, false},
		{"mention before pattern stripped", "<@UBOT1> /solve fix", []string{"^/solve"}, true},
		{"mention with display name stripped", "<@UBOT1|gravity> /solve fix", []string{"^/solve"}, true},
		{"unanchored pattern matches anywhere", "please /solve this", []string{"/solve"}, true},
		{"anchored pattern does not match mid-text", "please /solve this", []string{"^/solve"}, false},
		{"multiple patterns second matches", "/deploy now", []string{"^/solve", "^/deploy"}, true},
		{"invalid regex skipped", "/solve fix", []string{"[invalid", "^/solve"}, true},
		{"empty text", "", []string{"^/solve"}, false},
		{"case insensitive regex", "Deploy to prod", []string{"(?i)^deploy"}, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := matchesExcludePatterns(tt.text, tt.patterns); got != tt.want {
				t.Errorf("matchesExcludePatterns() = %v, want %v", got, tt.want)
			}
		})
	}
}
