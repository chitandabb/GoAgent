package diagnosis

import "testing"

func TestTerminalTaskStatusAndEventMapping(t *testing.T) {
	tests := []struct {
		status TaskStatus
		event  TaskEventType
	}{
		{status: TaskSucceeded, event: TaskEventSucceeded},
		{status: TaskFailed, event: TaskEventFailed},
		{status: TaskCancelled, event: TaskEventCancelled},
	}
	for _, test := range tests {
		event, ok := test.status.TerminalEvent()
		if !ok || event != test.event || !test.status.IsTerminal() {
			t.Errorf("status %q maps to (%q, %t), want %q", test.status, event, ok, test.event)
		}
		status, ok := test.event.TerminalStatus()
		if !ok || status != test.status || !test.event.IsTerminal() {
			t.Errorf("event %q maps to (%q, %t), want %q", test.event, status, ok, test.status)
		}
	}
}

func TestNonTerminalTaskStatusesAndEvents(t *testing.T) {
	for _, status := range []TaskStatus{TaskPending, TaskRunning, TaskCancelRequested, "unknown"} {
		if status.IsTerminal() {
			t.Errorf("status %q must not be terminal", status)
		}
		if event, ok := status.TerminalEvent(); ok || event != "" {
			t.Errorf("status %q maps to (%q, %t), want no terminal event", status, event, ok)
		}
	}
	for _, event := range []TaskEventType{
		TaskEventCreated, TaskEventCancelRequested, TaskEventStarted, TaskEventReclaimed,
		TaskEventRetryScheduled, TaskEventRequeued, "unknown",
	} {
		if event.IsTerminal() {
			t.Errorf("event %q must not be terminal", event)
		}
		if status, ok := event.TerminalStatus(); ok || status != "" {
			t.Errorf("event %q maps to (%q, %t), want no terminal status", event, status, ok)
		}
	}
}
