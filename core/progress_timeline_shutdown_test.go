package core

import (
	"testing"
	"time"
)

// Shutdown seals cards before cancelling the engine context. Deliver late
// progress in that window to reproduce the fallback card deterministically.
type timelineShutdownSession struct {
	*controllableAgentSession
	beforeEvents func()
}

func (s *timelineShutdownSession) Events() <-chan Event {
	s.beforeEvents()
	return s.events
}

func TestProcessInteractiveEvents_ShutdownTimelineDoesNotOpenLegacyCard(t *testing.T) {
	p := &timelineTestPlatform{style: "card"}
	p.n = "test"
	e := NewEngine("test", &stubAgent{}, []Platform{p}, "", LangEnglish)
	t.Cleanup(e.cancel)
	e.display.CardMode = "timeline"
	const key = "test:shutdown"
	session := e.sessions.GetOrCreateActive(key)
	if _, ok := session.TryLock(); !ok {
		t.Fatal("failed to mark session busy")
	}
	as := &timelineShutdownSession{controllableAgentSession: newControllableSession("shutdown")}
	state := &interactiveState{agentSession: as, busySession: session, platform: p, replyCtx: "ctx"}
	e.interactiveStates[key] = state
	as.beforeEvents = func() {
		state.timeline.Handle(Event{Type: EventThinking, Content: "before shutdown"}, e.display)
		e.interruptActiveSessionsBeforeShutdown()
	}
	as.events <- Event{Type: EventToolUse, ToolName: "Bash", ToolInput: "late command"}
	as.events <- Event{Type: EventThinking, Content: "late thinking"}
	as.events <- Event{Type: EventToolResult, ToolName: "Bash", ToolResult: "late result"}
	as.events <- Event{Type: EventResult, Content: "NO_REPLY", Done: true}
	e.processInteractiveEvents(state, session, e.sessions, key, "message", time.Now(), nil, nil, state.replyCtx, 0)
	if got := len(p.getPreviewOpens()); got != 1 {
		t.Fatalf("opened %d progress cards, want only the original timeline card", got)
	}
	payload := lastTimelinePayload(t, p)
	if payload.Style != progressStyleTimeline || payload.State != ProgressCardStateInterrupted || len(payload.Items) != 1 || payload.Items[0].Text != "before shutdown" {
		t.Fatalf("late progress changed the interrupted card: %+v", payload)
	}
	if sent := p.getSent(); len(sent) != 0 {
		t.Fatalf("late progress escaped as standalone messages: %q", sent)
	}
}

func TestTimelineProgress_SealedCardsAbsorbLateProgress(t *testing.T) {
	for _, terminal := range []ProgressCardState{ProgressCardStateInterrupted, ProgressCardStateCompleted, ProgressCardStateFailed, ProgressCardStatePaused} {
		t.Run(string(terminal), func(t *testing.T) {
			state, p := timelineTestState()
			w := newTimelineProgressLocked(t.Context(), state, "test:sealed", "agent", LangEnglish, nil, "timeline")
			w.Handle(Event{Type: EventThinking, Content: "original"}, DisplayCfg{ThinkingMessages: true})
			w.Finalize(terminal)
			before := p.getLatestCardContent()
			for _, kind := range []EventType{EventThinking, EventToolUse, EventToolResult} {
				if !w.Handle(Event{Type: kind, Content: "late", ToolName: "Bash", ToolInput: "late", ToolResult: "late"}, DisplayCfg{ThinkingMessages: true, ToolMessages: true}) {
					t.Fatalf("sealed timeline released %s to legacy progress", kind)
				}
			}
			if w.Handle(Event{Type: EventResult, Content: "answer", Done: true}, DisplayCfg{}) {
				t.Fatal("timeline swallowed final response handling")
			}
			if p.getLatestCardContent() != before {
				t.Fatal("late events modified a sealed card")
			}
		})
	}
}
