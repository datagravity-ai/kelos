package sessionruntime

import (
	"path/filepath"
	"testing"
	"time"

	kelos "github.com/kelos-dev/kelos/api/v1alpha2"
)

func TestLatestNotification(t *testing.T) {
	for _, test := range []struct {
		name   string
		events []Event
		kind   kelos.SessionNotificationType
	}{
		{name: "empty"},
		{name: "completed", events: []Event{{Type: EventTurnCompleted, Status: "completed"}}, kind: "TurnCompleted"},
		{name: "failed", events: []Event{{Type: EventTurnCompleted, Status: "failed"}}, kind: "TurnFailed"},
		{name: "input", events: []Event{{Type: EventInputRequested, InputID: "first"}}, kind: "InputRequested"},
		{name: "interrupted", events: []Event{{Type: EventInputRequested}, {Type: EventTurnCompleted, Status: "interrupted"}}},
		{name: "resolved", events: []Event{{Type: EventTurnCompleted, Status: "completed"}, {Type: EventInputRequested, InputID: "first"}, {Type: EventInputResolved, InputID: "first"}}},
		{name: "another input pending", events: []Event{{Type: EventInputRequested, InputID: "first"}, {Type: EventInputRequested, InputID: "second"}, {Type: EventInputResolved, InputID: "second"}}, kind: "InputRequested"},
		{name: "completed after input", events: []Event{{Type: EventInputRequested, InputID: "first"}, {Type: EventInputResolved, InputID: "first"}, {Type: EventTurnCompleted, Status: "completed"}}, kind: "TurnCompleted"},
		{name: "failed with pending input cancelled", events: []Event{{Type: EventInputRequested, InputID: "first"}, {Type: EventTurnCompleted, Status: "failed"}, {Type: EventInputResolved, InputID: "first", Status: "cancelled"}}, kind: "TurnFailed"},
		{name: "completed with pending input cancelled", events: []Event{{Type: EventInputRequested, InputID: "first"}, {Type: EventTurnCompleted, Status: "completed"}, {Type: EventInputResolved, InputID: "first", Status: "cancelled"}}, kind: "TurnCompleted"},
		{name: "unrelated events", events: []Event{{Type: EventTurnCompleted, Status: "failed"}, {Type: EventRuntimeStatus}, {Type: EventAssistantDelta}}, kind: "TurnFailed"},
	} {
		t.Run(test.name, func(t *testing.T) {
			journal := NewJournal()
			defer journal.Close()
			timestamp := time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC)
			for _, event := range test.events {
				event.Timestamp = &timestamp
				if err := journal.Append(event); err != nil {
					t.Fatal(err)
				}
			}
			notification := journal.LatestNotification()
			if test.kind == "" {
				if notification != nil {
					t.Fatalf("notification = %#v, want none", notification)
				}
				return
			}
			if notification == nil || notification.Type != test.kind || notification.ID == "" || !notification.EventTime.Time.Equal(timestamp) {
				t.Fatalf("notification = %#v, want %s at %v", notification, test.kind, timestamp)
			}
		})
	}
}

func TestNotificationIdentitySurvivesRestartAndChangesOnReset(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.jsonl")
	journal, err := OpenJournal(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := journal.Append(Event{Type: EventTurnCompleted, Status: "completed"}); err != nil {
		t.Fatal(err)
	}
	want := journal.LatestNotification()
	journal.Close()
	journal, err = OpenJournal(path)
	if err != nil {
		t.Fatal(err)
	}
	defer journal.Close()
	got := journal.LatestNotification()
	if got == nil || got.ID != want.ID || !got.EventTime.Equal(&want.EventTime) {
		t.Fatalf("notification after restart = %#v, want %#v", got, want)
	}
	replacement := NewJournal()
	defer replacement.Close()
	if err := replacement.Append(Event{Type: EventTurnCompleted, Status: "completed"}); err != nil {
		t.Fatal(err)
	}
	if replacement.LatestNotification().ID == want.ID {
		t.Fatal("A reset conversation reused the previous notification ID")
	}
}
