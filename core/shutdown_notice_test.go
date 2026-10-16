package core

import (
	"context"
	"strings"
	"testing"
)

func TestEngineStop_ShutdownInterruptionNoticeOnlyForBusySessions(t *testing.T) {
	p := &stubPlatformEngine{n: "test"}
	e := NewEngine("test", &stubAgent{}, []Platform{p}, "", LangChinese)
	e.SetShutdownInterruptionNotice(true)

	busy := e.sessions.GetOrCreateActive("test:busy")
	if _, ok := busy.TryLock(); !ok {
		t.Fatal("failed to mark test session busy")
	}
	e.interactiveStates["test:busy"] = &interactiveState{
		agentSession: &stubAgentSession{},
		busySession:  busy,
		platform:     p,
		replyCtx:     "busy-context",
	}
	idle := e.sessions.GetOrCreateActive("test:idle")
	e.interactiveStates["test:idle"] = &interactiveState{
		agentSession: &stubAgentSession{},
		busySession:  idle,
		platform:     p,
		replyCtx:     "idle-context",
	}

	if err := e.Stop(); err != nil {
		t.Fatalf("Stop returned error: %v", err)
	}
	sent := p.getSent()
	if len(sent) != 1 || !strings.Contains(sent[0], "服务即将停止") {
		t.Fatalf("shutdown notice = %#v, want one notice for the busy session", sent)
	}
}

func TestEngineStop_ShutdownInterruptionNoticeDisabled(t *testing.T) {
	p := &stubPlatformEngine{n: "test"}
	e := NewEngine("test", &stubAgent{}, []Platform{p}, "", LangChinese)
	session := e.sessions.GetOrCreateActive("test:busy")
	if _, ok := session.TryLock(); !ok {
		t.Fatal("failed to mark test session busy")
	}
	e.interactiveStates["test:busy"] = &interactiveState{
		agentSession: &stubAgentSession{},
		busySession:  session,
		platform:     p,
		replyCtx:     "context",
	}

	if err := e.Stop(); err != nil {
		t.Fatalf("Stop returned error: %v", err)
	}
	if sent := p.getSent(); len(sent) != 0 {
		t.Fatalf("disabled shutdown notice sent: %#v", sent)
	}
}

func TestEngineStop_SealsTimelineWhenShutdownNoticeDisabled(t *testing.T) {
	state, p := timelineTestState()
	e := NewEngine("test", &stubAgent{}, []Platform{p}, "", LangChinese)
	session := e.sessions.GetOrCreateActive("test:timeline")
	if _, ok := session.TryLock(); !ok {
		t.Fatal("failed to mark test session busy")
	}
	state.busySession = session
	state.agentSession = &stubAgentSession{}
	w := newTimelineProgressLocked(context.Background(), state, "test:timeline", "codex", LangChinese, nil, "timeline")
	if w == nil {
		t.Fatal("failed to create timeline progress")
	}
	w.Start()
	w.Handle(Event{Type: EventThinking, Content: "still working"}, DisplayCfg{ThinkingMessages: true})
	e.interactiveStates["test:timeline"] = state

	if err := e.Stop(); err != nil {
		t.Fatalf("Stop returned error: %v", err)
	}
	if got := lastTimelinePayload(t, p).State; got != ProgressCardStateInterrupted {
		t.Fatalf("timeline state = %q, want interrupted", got)
	}
	if sent := p.getSent(); len(sent) != 0 {
		t.Fatalf("shutdown notice sent while disabled: %#v", sent)
	}
}

func TestEngineStop_ShutdownInterruptionSealsTimelineCard(t *testing.T) {
	state, p := timelineTestState()
	e := NewEngine("test", &stubAgent{}, []Platform{p}, "", LangChinese)
	e.SetShutdownInterruptionNotice(true)
	session := e.sessions.GetOrCreateActive("test:timeline")
	if _, ok := session.TryLock(); !ok {
		t.Fatal("failed to mark test session busy")
	}
	state.busySession = session
	state.agentSession = &stubAgentSession{}
	w := newTimelineProgressLocked(context.Background(), state, "test:timeline", "codex", LangChinese, nil, "timeline")
	if w == nil {
		t.Fatal("failed to create timeline progress")
	}
	w.Start()
	w.Handle(Event{Type: EventThinking, Content: "still working"}, DisplayCfg{ThinkingMessages: true})
	e.interactiveStates["test:timeline"] = state

	if err := e.Stop(); err != nil {
		t.Fatalf("Stop returned error: %v", err)
	}
	if got := lastTimelinePayload(t, p).State; got != ProgressCardStateInterrupted {
		t.Fatalf("timeline state = %q, want interrupted", got)
	}
	if sent := p.getSent(); len(sent) != 1 || !strings.Contains(sent[0], "服务即将停止") {
		t.Fatalf("shutdown notice = %#v, want one notice", sent)
	}
}
