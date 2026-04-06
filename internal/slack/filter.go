package slack

import (
	"regexp"
	"strings"

	"github.com/kelos-dev/kelos/api/v1alpha1"
)

// SlackMessageData holds the parsed fields from a Slack message or slash
// command needed for matching and task creation.
type SlackMessageData struct {
	// UserID is the Slack user ID of the message author.
	UserID string
	// ChannelID is the Slack channel ID where the message was posted.
	ChannelID string
	// ChannelName is the human-readable channel name.
	ChannelName string
	// UserName is the display name of the message author.
	UserName string
	// Text is the raw message text.
	Text string
	// ThreadTS is the parent message timestamp when this is a thread reply.
	ThreadTS string
	// Timestamp is the message's own timestamp (used as ID and thread_ts for replies).
	Timestamp string
	// Permalink is the Slack permalink URL for the message.
	Permalink string
	// Body is the processed message body (trigger prefix stripped, or full thread context).
	Body string
	// IsSlashCommand indicates this came from a slash command rather than a message event.
	IsSlashCommand bool
	// SlashCommandID is the composite ID for slash commands (channelID:command:triggerID).
	SlashCommandID string
}

// MatchesSpawner checks whether a Slack message matches the given TaskSpawner's
// Slack configuration (channels and allowed users). Trigger command matching
// is handled separately during message preprocessing.
func MatchesSpawner(slackCfg *v1alpha1.Slack, msg *SlackMessageData) bool {
	if slackCfg == nil {
		return false
	}
	if !matchesChannel(msg.ChannelID, slackCfg.Channels) {
		return false
	}
	return true
}

// MatchesTriggers checks whether a message should fire based on configured
// triggers and bot mention. When triggers is empty, every message is accepted.
// When triggers are set, a message fires if at least one trigger matches
// (pattern matches AND (mention present OR MentionOptional)).
// botUserID is the Slack user ID of the bot (used for mention detection).
func MatchesTriggers(text string, triggers []v1alpha1.SlackTrigger, botUserID string) bool {
	if len(triggers) == 0 {
		return true
	}

	mentioned := strings.Contains(text, "<@"+botUserID+">")

	for _, t := range triggers {
		if t.Pattern == "" {
			continue
		}
		matched, err := regexp.MatchString(t.Pattern, text)
		if err != nil || !matched {
			continue
		}
		if t.MentionOptional != nil && *t.MentionOptional {
			return true
		}
		if mentioned {
			return true
		}
	}
	return false
}

// ExtractSlackWorkItem builds the template variables map from a Slack message
// for use with taskbuilder.BuildTask. The keys match the standard template
// variables available in promptTemplate and branch.
func ExtractSlackWorkItem(msg *SlackMessageData) map[string]interface{} {
	id := msg.Timestamp
	if msg.IsSlashCommand {
		id = msg.SlashCommandID
	}

	return map[string]interface{}{
		"ID":    id,
		"Title": msg.UserName,
		"Body":  msg.Body,
		"URL":   msg.Permalink,
		"Kind":  "SlackMessage",
	}
}

// matchesChannel returns true if channelID is in the allowed list,
// or if the allowed list is empty (all channels permitted).
func matchesChannel(channelID string, allowed []string) bool {
	if len(allowed) == 0 {
		return true
	}
	for _, id := range allowed {
		if id == channelID {
			return true
		}
	}
	return false
}
