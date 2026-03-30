package reporting

import (
	"context"
	"testing"
)

func TestFormatSlackMessages(t *testing.T) {
	tests := []struct {
		name     string
		fn       func(string) string
		taskName string
		want     string
	}{
		{
			name:     "accepted",
			fn:       FormatSlackAccepted,
			taskName: "spawner-1234567890.123456",
			want:     "Working on your request... (Task: spawner-1234567890.123456)",
		},
		{
			name:     "succeeded",
			fn:       FormatSlackSucceeded,
			taskName: "spawner-1234567890.123456",
			want:     "Done! (Task: spawner-1234567890.123456)",
		},
		{
			name:     "failed",
			fn:       FormatSlackFailed,
			taskName: "spawner-1234567890.123456",
			want:     "Failed. (Task: spawner-1234567890.123456)",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.fn(tt.taskName)
			if got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestSlackReporterConstruction(t *testing.T) {
	reporter := &SlackReporter{BotToken: "xoxb-test-token"}
	if reporter.BotToken != "xoxb-test-token" {
		t.Errorf("BotToken = %q, want %q", reporter.BotToken, "xoxb-test-token")
	}
}

func TestSlackReporter_PostThreadReplyError(t *testing.T) {
	reporter := &SlackReporter{BotToken: "xoxb-invalid"}
	_, err := reporter.PostThreadReply(context.Background(), "C123", "1234.5678", "test")
	if err == nil {
		t.Error("expected error with invalid token, got nil")
	}
}

func TestSlackReporter_UpdateMessageError(t *testing.T) {
	reporter := &SlackReporter{BotToken: "xoxb-invalid"}
	err := reporter.UpdateMessage(context.Background(), "C123", "1234.5678", "test")
	if err == nil {
		t.Error("expected error with invalid token, got nil")
	}
}
