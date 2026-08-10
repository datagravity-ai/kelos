package sessionruntime

import (
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

func TestProjectHistoryNormalizesProviderEventsAndPreservesState(t *testing.T) {
	startedAt := time.Date(2026, time.August, 8, 12, 0, 0, 0, time.UTC)
	events := []Event{
		{ID: 1, Type: EventUserMessage, TurnID: "turn-1", Text: "first request"},
		{ID: 2, Type: EventTurnStarted, TurnID: "turn-1", Timestamp: &startedAt, Status: "running"},
		{ID: 3, Type: EventAssistantDelta, TurnID: "turn-1", Text: "Claude "},
		{ID: 4, Type: EventAssistantDelta, TurnID: "turn-1", Text: "answer"},
		{ID: 5, Type: EventAssistantMessage, TurnID: "turn-1", Text: "Claude answer"},
		{ID: 6, Type: EventToolStarted, TurnID: "turn-1", ToolID: "tool-1", ToolName: "shell", Status: "running"},
		{ID: 7, Type: EventToolCompleted, TurnID: "turn-1", ToolID: "tool-1", ToolName: "shell", Output: strings.Repeat("output", 2*maxHistoryToolOutputBytes), Status: "completed"},
		{ID: 8, Type: EventInputRequested, TurnID: "turn-1", InputID: "input-1", Questions: []InputQuestion{{ID: "choice", Question: "Choose"}}, Status: "pending"},
		{ID: 9, Type: EventInputResolved, InputID: "input-1", Status: "answered"},
		{ID: 10, Type: EventTurnCompleted, TurnID: "turn-1", Status: "completed"},
		{ID: 11, Type: EventUserMessage, TurnID: "turn-2", Text: "active request"},
		{ID: 12, Type: EventTurnStarted, TurnID: "turn-2", Timestamp: &startedAt, Status: "running"},
		{ID: 13, Type: EventAssistantDelta, TurnID: "turn-2", Text: "Open"},
		{ID: 14, Type: EventAssistantDelta, TurnID: "turn-2", Text: "Code answer"},
		{ID: 15, Type: EventInputRequested, TurnID: "turn-2", InputID: "input-2", Questions: []InputQuestion{{ID: "confirm", Question: "Continue?"}}, Status: "pending"},
		{ID: 16, Type: EventUserMessage, TurnID: "turn-3", Text: "queued first", Attachments: []Attachment{{ID: "attachment-1", Name: "screen.png", MediaType: "image/png", SizeBytes: 7}}},
		{ID: 17, Type: EventUserMessage, TurnID: "turn-4", Text: "queued second"},
	}

	items, state, essential := projectHistory(events)
	if state.ActiveTurnID != "turn-2" || state.ActiveTurnStarted == nil || !state.ActiveTurnStarted.Equal(startedAt) {
		t.Fatalf("history state = %#v, want active turn-2", state)
	}
	if !state.WaitingForInput || state.TurnInterrupting {
		t.Fatalf("history state = %#v, want waiting active turn", state)
	}
	if len(state.QueuedTurns) != 2 || state.QueuedTurns[0].TurnID != "turn-3" || state.QueuedTurns[1].TurnID != "turn-4" {
		t.Fatalf("queued turns = %#v", state.QueuedTurns)
	}
	if len(state.QueuedTurns[0].Attachments) != 1 || state.QueuedTurns[0].Attachments[0].Name != "screen.png" {
		t.Fatalf("queued turn attachments = %#v", state.QueuedTurns[0].Attachments)
	}
	if len(essential) != 1 || essential[0].Type != EventInputRequested || essential[0].InputID != "input-2" || essential[0].TurnID != "" {
		t.Fatalf("essential history events = %#v", essential)
	}

	projected, cursor := historyItemsPage(items, 0, 100, maxHistoryByteLimit)
	if cursor != 0 {
		t.Fatalf("history cursor = %d, want all items", cursor)
	}
	assertEventTypes(t, projected,
		EventUserMessage,
		EventAssistantMessage,
		EventToolStarted,
		EventToolCompleted,
		EventInputRequested,
		EventInputResolved,
		EventUserMessage,
		EventAssistantMessage,
	)
	for _, event := range projected {
		if event.TurnID != "" || event.RequestID != "" {
			t.Fatalf("projected event retains transient state: %#v", event)
		}
	}
	if projected[1].Text != "Claude answer" || projected[7].Text != "OpenCode answer" {
		t.Fatalf("assistant messages = %q, %q", projected[1].Text, projected[7].Text)
	}
	if len(projected[3].Output) > maxHistoryToolOutputBytes || !strings.Contains(projected[3].Output, historyTruncationMarker) {
		t.Fatalf("tool output preview has %d bytes", len(projected[3].Output))
	}
}

func TestHistoryItemsPageHonorsItemAndByteLimits(t *testing.T) {
	events := make([]Event, 0, 5)
	for id := int64(1); id <= 5; id++ {
		events = append(events, Event{ID: id, Type: EventUserMessage, Text: strings.Repeat(string(rune('a'+id)), 256)})
	}
	items, _, _ := projectHistory(events)

	page, cursor := historyItemsPage(items, 0, 2, maxHistoryByteLimit)
	assertHistoryEventIDs(t, page, 4, 5)
	if cursor != 4 {
		t.Fatalf("history cursor = %d, want 4", cursor)
	}
	page, cursor = historyItemsPage(items, cursor, 2, maxHistoryByteLimit)
	assertHistoryEventIDs(t, page, 2, 3)
	if cursor != 2 {
		t.Fatalf("history cursor = %d, want 2", cursor)
	}
	page, cursor = historyItemsPage(items, cursor, 2, maxHistoryByteLimit)
	assertHistoryEventIDs(t, page, 1)
	if cursor != 0 {
		t.Fatalf("history cursor = %d, want end of history", cursor)
	}

	page, cursor = historyItemsPage(items, 0, len(items), historyItemWireBytes(items[len(items)-1]))
	assertHistoryEventIDs(t, page, 5)
	if cursor != 5 {
		t.Fatalf("byte-limited history cursor = %d, want 5", cursor)
	}
}

func TestProjectHistoryKeepsActiveAssistantStreaming(t *testing.T) {
	items, state, _ := projectHistory([]Event{
		{ID: 1, Type: EventUserMessage, TurnID: "turn-1", Text: "request"},
		{ID: 2, Type: EventTurnStarted, TurnID: "turn-1", Status: "running"},
		{ID: 3, Type: EventAssistantDelta, TurnID: "turn-1", Text: "partial "},
		{ID: 4, Type: EventAssistantDelta, TurnID: "turn-1", Text: "answer"},
	})
	if state.ActiveTurnID != "turn-1" {
		t.Fatalf("active turn = %q, want turn-1", state.ActiveTurnID)
	}
	page, cursor := historyItemsPage(items, 0, DefaultHistoryItemLimit, DefaultHistoryByteLimit)
	if cursor != 0 {
		t.Fatalf("history cursor = %d, want all items", cursor)
	}
	assertEventTypes(t, page, EventUserMessage, EventAssistantDelta)
	if page[1].Text != "partial answer" {
		t.Fatalf("active assistant text = %q", page[1].Text)
	}
}

func TestProjectHistoryBoundsLargeMessages(t *testing.T) {
	items, _, _ := projectHistory([]Event{{
		ID:   1,
		Type: EventAssistantMessage,
		Text: strings.Repeat("界", maxHistoryMessageBytes),
	}})
	page, cursor := historyItemsPage(items, 0, DefaultHistoryItemLimit, DefaultHistoryByteLimit)
	if cursor != 0 || len(page) != 1 {
		t.Fatalf("history page = %#v, cursor %d", page, cursor)
	}
	if len(page[0].Text) > maxHistoryMessageBytes || !strings.Contains(page[0].Text, historyTruncationMarker) || !utf8.ValidString(page[0].Text) {
		t.Fatalf("message preview is not a bounded UTF-8 string")
	}
}

func TestProjectHistoryBoundsQueuedMessages(t *testing.T) {
	_, state, _ := projectHistory([]Event{{
		ID:     1,
		Type:   EventUserMessage,
		TurnID: "turn-1",
		Text:   strings.Repeat("界", maxHistoryMessageBytes),
	}})
	if len(state.QueuedTurns) != 1 {
		t.Fatalf("queued turns = %#v, want one turn", state.QueuedTurns)
	}
	text := state.QueuedTurns[0].Text
	if len(text) > maxHistoryMessageBytes || !strings.Contains(text, historyTruncationMarker) || !utf8.ValidString(text) {
		t.Fatalf("queued message preview is not a bounded UTF-8 string")
	}
}

func assertHistoryEventIDs(t *testing.T, events []Event, want ...int64) {
	t.Helper()
	if len(events) != len(want) {
		t.Fatalf("history events = %#v, want IDs %v", events, want)
	}
	for index, event := range events {
		if event.ID != want[index] {
			t.Fatalf("history events = %#v, want IDs %v", events, want)
		}
	}
}
