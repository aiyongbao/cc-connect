package core

import (
	"strings"
	"testing"
	"time"
)

// The unsolicited reader treats every result as final, so a provisional
// result was already relayed. The agent's backstop result for the same turn
// must not be relayed a second time.
func TestUnsolicitedReader_IgnoresTurnBackstopResult(t *testing.T) {
	p := &stubPlatformEngine{n: "test"}
	sess := newControllableSession("unsol-backstop")
	e := NewEngine("test", &stubAgent{}, []Platform{p}, "", LangEnglish)
	defer e.Stop()

	sessions := e.sessions
	iKey := "test:ch1:backstop"
	session := sessions.GetOrCreateActive(iKey)
	state := &interactiveState{agentSession: sess, platform: p, replyCtx: "ctx"}
	e.interactiveMu.Lock()
	e.interactiveStates[iKey] = state
	e.interactiveMu.Unlock()

	e.startUnsolicitedReader(state, session, sessions, iKey, "")
	defer e.stopUnsolicitedReader(state)

	sess.events <- Event{Type: EventResult, Content: "后台任务汇报", Done: false}
	sess.events <- Event{Type: EventResult, Content: "后台任务汇报", Done: true, Metadata: map[string]any{EventMetaTurnBackstop: true}}
	sess.events <- Event{Type: EventResult, Content: "下一条后台回复", Done: true}

	sent := waitForPlatformSend(p, 2, 5*time.Second)
	if len(sent) != 2 || !strings.Contains(sent[0], "后台任务汇报") || !strings.Contains(sent[1], "下一条后台回复") {
		t.Fatalf("sent = %#v, want the provisional result once followed by the next reply", sent)
	}
}

// A foreground turn held open by reported background work closes on the
// agent's backstop result and delivers its content exactly once.
func TestProcessInteractiveEvents_TurnBackstopClosesHeldTurn(t *testing.T) {
	p := &stubPlatformEngine{n: "test"}
	e := NewEngine("test", &stubAgent{}, []Platform{p}, "", LangEnglish)
	sessionKey := "test:turn-backstop"
	session := e.sessions.GetOrCreateActive(sessionKey)
	agentSession := &pendingBackgroundTestSession{
		controllableAgentSession: newControllableSession("turn-backstop"),
		checked:                  make(chan struct{}),
	}
	agentSession.pending.Store(true)
	state := &interactiveState{agentSession: agentSession, platform: p, replyCtx: "ctx-backstop"}

	release := make(chan struct{})
	go func() {
		agentSession.events <- Event{Type: EventResult, Content: "最终答复", Done: true}
		<-release
		// The adapter clears its stale background state before sending the backstop.
		agentSession.pending.Store(false)
		agentSession.events <- Event{Type: EventResult, Content: "最终答复", Done: true, Metadata: map[string]any{EventMetaTurnBackstop: true}}
	}()

	done := make(chan struct{})
	go func() {
		e.processInteractiveEvents(state, session, e.sessions, sessionKey, "m-backstop", time.Now(), nil, nil, state.replyCtx, 0)
		close(done)
	}()
	select {
	case <-agentSession.checked:
	case <-time.After(time.Second):
		t.Fatal("engine did not inspect the pending background task")
	}
	if sent := p.getSent(); len(sent) != 0 {
		t.Fatalf("held result was sent before the backstop: %#v", sent)
	}
	close(release)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("backstop result did not close the held turn")
	}
	if sent := p.getSent(); len(sent) != 1 || !strings.Contains(sent[0], "最终答复") {
		t.Fatalf("final reply = %#v, want one delivery of the held content", sent)
	}
}
