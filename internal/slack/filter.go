package slack

import (
	"fmt"
	"regexp"
	"strings"
	"sync"

	"github.com/kelos-dev/kelos/api/v1alpha1"
)

var triggerRegexpCache sync.Map

func getOrCompileTriggerRegexp(pattern string) (*regexp.Regexp, error) {
	if cached, ok := triggerRegexpCache.Load(pattern); ok {
		return cached.(*regexp.Regexp), nil
	}
	re, err := regexp.Compile(pattern)
	if err != nil {
		return nil, err
	}
	triggerRegexpCache.Store(pattern, re)
	return re, nil
}

// SlackMessageData holds the parsed fields from a Slack message or slash
// command needed for matching and task creation.
type SlackMessageData struct {
	// UserID is the Slack user ID of the message author.
	UserID string
	// ChannelID is the Slack channel ID where the message was posted.
	ChannelID string
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
	// HasThreadContext indicates that Body contains full thread context
	// rather than the raw message text.
	HasThreadContext bool
	// IsSlashCommand indicates this came from a slash command rather than a message event.
	IsSlashCommand bool
	// SlashCommandID is the composite ID for slash commands (channelID:command:triggerID).
	SlashCommandID string
}

// MatchesSpawner checks whether a Slack message matches the given TaskSpawner's
// Slack configuration (channels). Trigger pattern matching is handled
// separately via MatchesTriggers.
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
// triggers, bot mention, and exclude patterns. When triggers is empty, every
// message is accepted. When triggers are set, a message fires if at least one
// trigger matches (pattern matches AND (mention present OR MentionOptional)).
// After positive matching, the message is rejected if it matches any exclude
// pattern. botUserID is the Slack user ID of the bot (used for mention
// detection).
func MatchesTriggers(text string, triggers []v1alpha1.SlackTrigger, botUserID string, excludePatterns []string) bool {
	if len(triggers) > 0 && !matchesTriggers(text, triggers, botUserID) {
		return false
	}
	if matchesExcludePatterns(text, excludePatterns) {
		return false
	}
	return true
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

// hasBotMention returns true if the message text contains an @-mention of
// the bot user ID. Slack encodes mentions as <@USER_ID> or <@USER_ID|name>.
func hasBotMention(text string, botUserID string) bool {
	if botUserID == "" {
		return false
	}
	return strings.Contains(text, fmt.Sprintf("<@%s>", botUserID)) ||
		strings.Contains(text, fmt.Sprintf("<@%s|", botUserID))
}

// stripLeadingMentions removes Slack mention tokens (<@USERID> or
// <@USERID|display-name>) from the beginning of text so that exclude
// pattern matching works regardless of mention placement.
func stripLeadingMentions(text string) string {
	s := text
	for {
		s = strings.TrimSpace(s)
		if !strings.HasPrefix(s, "<@") {
			return s
		}
		end := strings.Index(s, ">")
		if end == -1 {
			return s
		}
		s = s[end+1:]
	}
}

// matchesExcludePatterns returns true if the message text (after stripping
// leading @-mentions) matches any of the given regular expressions.
func matchesExcludePatterns(text string, patterns []string) bool {
	if len(patterns) == 0 {
		return false
	}
	cleaned := stripLeadingMentions(text)
	for _, p := range patterns {
		re, err := getOrCompileTriggerRegexp(p)
		if err != nil {
			continue
		}
		if re.MatchString(cleaned) {
			return true
		}
	}
	return false
}

// matchesTriggers evaluates trigger patterns against message text with OR
// semantics. Each trigger requires pattern match AND bot mention, unless
// MentionOptional is true on that trigger.
func matchesTriggers(text string, triggers []v1alpha1.SlackTrigger, botUserID string) bool {
	mentioned := hasBotMention(text, botUserID)
	for _, t := range triggers {
		re, err := getOrCompileTriggerRegexp(t.Pattern)
		if err != nil {
			continue
		}
		if !re.MatchString(text) {
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
