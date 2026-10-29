package sessionruntime

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

type testEventSource struct{ sink EventSink }

func (p *testEventSource) SetEventSink(sink EventSink) { p.sink = sink }

func (p *testEventSource) startTurn(ctx context.Context, run func() error) error {
	go func() {
		err := run()
		status, message := "completed", ""
		if errors.Is(err, ErrTurnInterrupted) || errors.Is(context.Cause(ctx), ErrTurnInterrupted) {
			status = "interrupted"
		} else if err != nil {
			status, message = "failed", err.Error()
		}
		p.sink.Emit(Event{Type: EventTurnCompleted, Status: status, Text: message})
	}()
	return nil
}

type providerTestSink struct {
	mu        sync.Mutex
	events    []Event
	inputs    chan InputRequest
	answers   map[string][]string
	completed chan Event
}

func newProviderTestSink(answers map[string][]string) *providerTestSink {
	return &providerTestSink{
		inputs:    make(chan InputRequest, 4),
		answers:   answers,
		completed: make(chan Event, 1),
	}
}

func (s *providerTestSink) Emit(event Event) {
	s.mu.Lock()
	s.events = append(s.events, event)
	s.mu.Unlock()
	if event.Type == EventTurnCompleted {
		s.completed <- event
	}
}

func (s *providerTestSink) RequestInput(_ context.Context, request InputRequest) (map[string][]string, error) {
	s.inputs <- request
	return s.answers, nil
}

func (s *providerTestSink) snapshot() []Event {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Event(nil), s.events...)
}

func (s *providerTestSink) waitForCompletion(t *testing.T, status string) {
	t.Helper()
	select {
	case event := <-s.completed:
		if event.Status != status || event.Text != "" {
			t.Fatalf("Turn completion = %#v, want status %q without an error", event, status)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Provider turn did not finish")
	}
}
