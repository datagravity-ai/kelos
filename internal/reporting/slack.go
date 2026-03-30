package reporting

import (
	"context"
	"fmt"

	"github.com/slack-go/slack"
)

// SlackReporter posts and updates thread replies in Slack channels.
type SlackReporter struct {
	// BotToken is the Bot User OAuth Token (xoxb-...).
	BotToken string
}

// PostThreadReply posts a new message as a thread reply and returns the
// reply's message timestamp.
func (r *SlackReporter) PostThreadReply(ctx context.Context, channel, threadTS, text string) (string, error) {
	api := slack.New(r.BotToken)
	_, ts, err := api.PostMessageContext(ctx, channel,
		slack.MsgOptionText(text, false),
		slack.MsgOptionTS(threadTS),
	)
	if err != nil {
		return "", fmt.Errorf("posting Slack thread reply: %w", err)
	}
	return ts, nil
}

// UpdateMessage updates an existing Slack message in place.
func (r *SlackReporter) UpdateMessage(ctx context.Context, channel, messageTS, text string) error {
	api := slack.New(r.BotToken)
	_, _, _, err := api.UpdateMessageContext(ctx, channel, messageTS,
		slack.MsgOptionText(text, false),
	)
	if err != nil {
		return fmt.Errorf("updating Slack message: %w", err)
	}
	return nil
}

// FormatSlackAccepted returns the thread reply text for an accepted task.
func FormatSlackAccepted(taskName string) string {
	return fmt.Sprintf("Working on your request... (Task: %s)", taskName)
}

// FormatSlackSucceeded returns the thread reply text for a succeeded task.
func FormatSlackSucceeded(taskName string) string {
	return fmt.Sprintf("Done! (Task: %s)", taskName)
}

// FormatSlackFailed returns the thread reply text for a failed task.
func FormatSlackFailed(taskName string) string {
	return fmt.Sprintf("Failed. (Task: %s)", taskName)
}
