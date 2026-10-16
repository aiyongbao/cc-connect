package core

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// rich capability makes this test catch accidental takeover by global rich mode.
func (p *timelineTestPlatform) BuildRichCard(CardStatus, string, []ToolStep, string, bool, string) string {
	return "UNEXPECTED RICH CARD"
}

func newTimelineCUJEnv(t *testing.T, style, cardMode string) (*cujEnv, *timelineTestPlatform) {
	t.Helper()
	p := &timelineTestPlatform{style: style}
	p.n = "test"
	agent := &cujAgent{}
	e := NewEngine("test", agent, []Platform{p}, t.TempDir()+"/sessions.json", LangEnglish)
	e.display.CardMode = cardMode
	e.SetStreamPreviewCfg(StreamPreviewCfg{Enabled: true})
	return &cujEnv{t: t, engine: e, plat: &p.stubPlatformEngine, agent: agent}, p
}

func sendTimelineCUJ(env *cujEnv, p Platform, content string) {
	env.engine.ReceiveMessage(p, &Message{SessionKey: "test:jack", UserID: "jack", MessageID: "msg-" + content, Content: content, ReplyCtx: "ctx-jack"})
}

// Regression journey: ask → choose → follow up. Prompt cards must occur between
// immutable progress segments; the full final reply is a separate message even
// with global rich mode and streaming previews enabled.
func TestCUJ_STREAM1_TimelineSplitsAfterUserChoice(t *testing.T) {
	for _, tc := range []struct{ style, mode string }{{"timeline", "rich"}, {"card", "timeline"}} {
		t.Run(tc.style+"-"+tc.mode, func(t *testing.T) {
			env, p := newTimelineCUJEnv(t, tc.style, tc.mode)
			env.engine.SetReplyFooterEnabled(true)
			env.engine.SetShowContextIndicator(false)
			env.engine.SetShowWorkdirIndicator(false)
			env.agent.setNextSessionEvents([]Event{
				{Type: EventThinking, Content: "plan before question"},
				{Type: EventText, Content: "draft before question"},
				{Type: EventPermissionRequest, ToolName: "AskUserQuestion", RequestID: "q1", Questions: []UserQuestion{{Question: "Continue?", Options: []UserQuestionOption{{Label: "Yes"}, {Label: "No"}}}}},
				{Type: EventThinking, Content: "plan after choice"},
				{Type: EventToolUse, ToolName: "Bash", ToolInput: "pwd"},
				{Type: EventToolResult, ToolName: "Bash", ToolResult: "hidden output"},
				{Type: EventText, Content: "draft after choice"},
				{Type: EventResult, Content: "complete final answer", Done: true},
			}, 0)
			sendTimelineCUJ(env, p, "start")
			env.waitFor("question", 2*time.Second, func() bool { return strings.Contains(strings.Join(p.getSent(), "\n"), "Continue?") })
			if len(p.getPreviewOpens()) != 1 || lastTimelinePayload(t, p).State != ProgressCardStatePaused {
				t.Fatal("first card was not sealed before question")
			}
			oldToken := lastTimelinePayload(t, p).TurnID
			// User action 2, through the same inbound boundary as real selection replies.
			sendTimelineCUJ(env, p, "1")
			env.waitFor("final answer", 2*time.Second, func() bool { return strings.Contains(strings.Join(p.getSent(), "\n"), "complete final answer") })
			// A choice starts a new progress card, but the independent final
			// answer must still carry exactly one elapsed-only footer.
			if sent := strings.Join(p.getSent(), "\n"); strings.Count(sent, "⏱ Elapsed ") != 1 {
				t.Fatalf("missing or duplicated final elapsed footer after choice: %q", sent)
			}
			opens := p.getPreviewOpens()
			if len(opens) != 2 {
				t.Fatalf("expected 2 progress cards, got %d", len(opens))
			}
			newPayload := lastTimelinePayload(t, p)
			if newPayload.State != ProgressCardStateCompleted || newPayload.TurnID == oldToken {
				t.Fatalf("new card lifecycle: %+v", newPayload)
			}
			for _, update := range p.getPreviewUpdates() {
				payload, _ := ParseProgressCardPayload(update.Content)
				if payload.TurnID == oldToken && (payload.State == ProgressCardStateCompleted || strings.Contains(update.Content, "after choice")) {
					t.Fatal("old card updated after choice")
				}
				if strings.Contains(update.Content, "complete final answer") || strings.Contains(update.Content, "hidden output") {
					t.Fatal("answer or full tool output leaked into progress")
				}
			}
			for _, text := range p.getSent() {
				if strings.Contains(text, "draft") || strings.Contains(text, "UNEXPECTED RICH") {
					t.Fatalf("unexpected intermediate message: %s", text)
				}
			}
			// User action 3: next prompt, then replay old card's button.
			env.waitFor("turn unlocked", 2*time.Second, func() bool { return !env.engine.sessions.GetOrCreateActive("test:jack").Busy() })
			env.agent.mu.Lock()
			s := env.agent.sessions[0]
			env.agent.mu.Unlock()
			s.mu.Lock()
			s.pendingEvents = []Event{{Type: EventThinking, Content: "new running task"}}
			s.mu.Unlock()
			sendTimelineCUJ(env, p, "next")
			env.waitFor("third card", 2*time.Second, func() bool { return len(p.getPreviewOpens()) == 3 })
			env.engine.handleCardNav("act:/timeline-stop "+oldToken, "test:jack")
			env.engine.handleCardNav("act:/timeline-stop "+newPayload.TurnID, "test:jack")
			current := lastTimelinePayload(t, p)
			if current.State != ProgressCardStateRunning {
				t.Fatal("old stop cancelled new task")
			}
			env.engine.handleCardNav("act:/timeline-stop "+current.TurnID, "test:jack")
			if lastTimelinePayload(t, p).State != ProgressCardStateInterrupted {
				t.Fatal("current button failed to interrupt")
			}
		})
	}
}

func TestTimelineFinalReply_NO_REPLYRemainsSilent(t *testing.T) {
	env, p := newTimelineCUJEnv(t, "timeline", "rich")
	env.agent.setNextSessionEvents([]Event{{Type: EventText, Content: "NO_"}, {Type: EventText, Content: "REPLY"}, {Type: EventResult, Content: "NO_REPLY", Done: true}}, 0)
	sendTimelineCUJ(env, p, "silent")
	env.waitFor("silent card completed", 2*time.Second, func() bool {
		updates := p.getPreviewUpdates()
		if len(updates) == 0 {
			return false
		}
		payload, ok := ParseProgressCardPayload(updates[len(updates)-1].Content)
		return ok && payload.State == ProgressCardStateCompleted
	})
	// Progress is sealed before result persistence and silent-reply handling.
	// Wait for the whole turn so assertions and TempDir cleanup cannot race it.
	env.waitFor("silent turn finished", 2*time.Second, func() bool {
		return !env.engine.sessions.GetOrCreateActive("test:jack").Busy()
	})
	if len(p.getSent()) != 0 {
		t.Fatalf("silent reply leaked: %v", p.getSent())
	}
}

type timelineCancelSession struct {
	*cujAgentSession
	cancels   int
	cancelErr error
}

func (s *timelineCancelSession) CancelTurn() error { s.cancels++; return s.cancelErr }

func TestTimelineStop_GracefulCancelPreservesSession(t *testing.T) {
	state, p := timelineTestState()
	agentSession := &timelineCancelSession{cujAgentSession: newCUJAgentSession()}
	state.agentSession = agentSession
	e := NewEngine("test", &cujAgent{}, []Platform{p}, t.TempDir()+"/sessions.json", LangEnglish)
	e.interactiveStates["test:jack"] = state
	w := newTimelineProgressLocked(context.Background(), state, "test:jack", "codex", LangEnglish, nil, "timeline")
	w.Start()
	e.handleCardNav("act:/timeline-stop "+w.payload.TurnID, "test:jack")
	if agentSession.cancels != 1 || !agentSession.Alive() || e.interactiveStates["test:jack"] != state {
		t.Fatal("graceful cancellation destroyed session")
	}
	if lastTimelinePayload(t, p).State != ProgressCardStateInterrupted {
		t.Fatal("graceful stop did not seal progress")
	}
}

// CancelTurn failures must fall back to process cleanup without unlocking an
// already unlocked engine mutex.
func TestTimelineStop_CancelFailureFallsBackToClose(t *testing.T) {
	state, p := timelineTestState()
	s := &timelineCancelSession{cujAgentSession: newCUJAgentSession(), cancelErr: errors.New("cancel failed")}
	state.agentSession = s
	e := NewEngine("test", &cujAgent{}, []Platform{p}, t.TempDir()+"/sessions.json", LangEnglish)
	e.interactiveStates["test:jack"] = state
	w := newTimelineProgressLocked(context.Background(), state, "test:jack", "codex", LangEnglish, nil, "timeline")
	w.Start()
	e.handleCardNav("act:/timeline-stop "+w.payload.TurnID, "test:jack")
	if !state.isStopped() || lastTimelinePayload(t, p).State != ProgressCardStateInterrupted {
		t.Fatal("failed graceful cancel did not stop the task")
	}
}

func TestTimelineFinalReply_HiddenToolsDoNotReplaceFinalAnswerWithDraft(t *testing.T) {
	env, p := newTimelineCUJEnv(t, "card", "timeline")
	env.engine.display.ToolMessages = false
	env.agent.setNextSessionEvents([]Event{
		{Type: EventText, Content: "working draft"},
		{Type: EventToolUse, ToolName: "Bash", ToolInput: "pwd"},
		{Type: EventText, Content: "final chunks"},
		{Type: EventResult, Content: "authoritative final answer", Done: true},
	}, 0)
	sendTimelineCUJ(env, p, "start")
	env.waitFor("final answer", 2*time.Second, func() bool { return strings.Contains(strings.Join(p.getSent(), "\n"), "authoritative final answer") })
	if strings.Contains(strings.Join(p.getSent(), "\n"), "draft") {
		t.Fatal("draft replaced final answer")
	}
}
