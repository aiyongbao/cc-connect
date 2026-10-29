package core

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// initialContextEchoSession exposes the actual prompt and attachment selection
// through the agent boundary and echoes the prompt as the user-visible answer.
type initialContextEchoSession struct {
	*cujAgentSession
	inputs chan initialContextInput
}

type initialContextInput struct {
	prompt string
	images []ImageAttachment
	files  []FileAttachment
}

func newInitialContextEchoSession() *initialContextEchoSession {
	return &initialContextEchoSession{newCUJAgentSession(), make(chan initialContextInput, 4)}
}

func (s *initialContextEchoSession) Send(prompt, _ string, images []ImageAttachment, files []FileAttachment) error {
	s.inputs <- initialContextInput{prompt, images, files}
	s.events <- Event{Type: EventResult, Content: prompt, Done: true}
	return nil
}

// A saved ID must suppress only bootstrap background. A rejected resume must
// retain it for the replacement conversation, including its attachments.
func TestProcessInteractiveMessage_InitialContextFollowsSelectedSession(t *testing.T) {
	for _, tc := range []struct {
		name       string
		saved      bool
		failResume bool
	}{
		{name: "fresh"},
		{name: "resumed", saved: true},
		{name: "failed resume starts fresh", saved: true, failResume: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := &stubPlatformEngine{n: "test"}
			as := newInitialContextEchoSession()
			agent := &controllableAgent{startSessionFn: func(_ context.Context, id string) (AgentSession, error) {
				if id != "" && tc.failResume {
					return nil, errors.New("saved conversation unavailable")
				}
				return as, nil
			}}
			e := NewEngine("test", agent, []Platform{p}, "", LangEnglish)
			t.Cleanup(func() { _ = e.Stop() })
			const key = "test:topic"
			session := e.sessions.GetOrCreateActive(key)
			if tc.saved {
				session.SetAgentSessionID("saved-conversation", agent.Name())
			}
			e.ReceiveMessage(p, &Message{
				SessionKey: key, Platform: "test", UserID: "user", ReplyCtx: "ctx", Content: "retry latest task",
				ExtraContent: "ordinary quote and group history",
				Images:       []ImageAttachment{{MimeType: "image/current"}}, Files: []FileAttachment{{FileName: "current.txt"}},
				InitialContext: &InitialMessageContext{
					Text:   "original topic question",
					Images: []ImageAttachment{{MimeType: "image/root"}}, Files: []FileAttachment{{FileName: "root.txt"}},
				},
			})
			var got initialContextInput
			select {
			case got = <-as.inputs:
			case <-time.After(2 * time.Second):
				t.Fatal("message did not reach agent")
			}
			wantInitial := !tc.saved || tc.failResume
			if strings.Contains(got.prompt, "original topic question") != wantInitial {
				t.Fatalf("bootstrap selection differs from actual resume: %q", got.prompt)
			}
			for _, want := range []string{"ordinary quote and group history", "retry latest task"} {
				if !strings.Contains(got.prompt, want) {
					t.Fatalf("lost current message or normal context %q: %q", want, got.prompt)
				}
			}
			wantAttachments := 1
			if wantInitial {
				wantAttachments++
			}
			if len(got.images) != wantAttachments || len(got.files) != wantAttachments || got.files[len(got.files)-1].FileName != "current.txt" || got.images[len(got.images)-1].MimeType != "image/current" {
				t.Fatalf("bootstrap/current attachment selection: %+v", got)
			}
			waitForSessionIdle(t, session)
			history := session.GetHistory(0)
			if len(history) == 0 || strings.Contains(history[0].Content, "original topic question") != wantInitial {
				t.Fatalf("history disagrees with delivered prompt: %+v", history)
			}
		})
	}
}

// Even a newly activated platform topic can arrive while an existing agent
// turn is busy; neither the queued message nor its history may replay the root.
func TestHandleMessage_QueuedFollowupSkipsInitialContext(t *testing.T) {
	p := &stubPlatformEngine{n: "test"}
	as := newQueuingSession("existing")
	e := NewEngine("test", &controllableAgent{nextSession: as}, []Platform{p}, "", LangEnglish)
	t.Cleanup(func() { _ = e.Stop() })
	const key = "test:topic"
	e.ReceiveMessage(p, &Message{SessionKey: key, Platform: "test", Content: "latest two questions", ReplyCtx: "ctx"})
	waitForQueuedSends(t, as, 1)
	e.ReceiveMessage(p, &Message{
		SessionKey: key, Platform: "test", Content: "retry", ReplyCtx: "ctx",
		ExtraContent: "normal quote", InitialContext: &InitialMessageContext{Text: "obsolete root"},
	})
	as.events <- Event{Type: EventResult, Content: "first answer", Done: true}
	waitForQueuedSends(t, as, 2)
	as.events <- Event{Type: EventResult, Content: "second answer", Done: true}
	session := e.sessions.GetOrCreateActive(key)
	waitForSessionIdle(t, session)
	as.sendMu.Lock()
	prompt := as.sendCalls[1]
	as.sendMu.Unlock()
	if strings.Contains(prompt, "obsolete root") || !strings.Contains(prompt, "normal quote\nretry") {
		t.Fatalf("queued context corrupted: %q", prompt)
	}
}

// CUJ: start a topic, ask follow-up questions, restart, then retry. The echo
// agent lets the platform's visible answer prove which task was actually sent.
func testThreadRetryAfterRestartSkipsRootContext(t *testing.T) {
	store := filepath.Join(t.TempDir(), "sessions.json")
	p := &stubPlatformEngine{n: "test"}
	newEngine := func() *Engine {
		a := &controllableAgent{startSessionFn: func(context.Context, string) (AgentSession, error) {
			return newInitialContextEchoSession(), nil
		}}
		e := NewEngine("test", a, []Platform{p}, store, LangEnglish)
		t.Cleanup(func() { _ = e.Stop() })
		return e
	}
	e := newEngine()
	const key = "test:thread"
	send := func(content string, initial *InitialMessageContext) {
		e.ReceiveMessage(p, &Message{SessionKey: key, Platform: "test", UserID: "user", Content: content, ReplyCtx: "ctx", InitialContext: initial})
		waitForSessionIdle(t, e.sessions.GetOrCreateActive(key))
	}
	send("first request", &InitialMessageContext{Text: "original topic"})
	send("latest two questions", &InitialMessageContext{Text: "original topic"})
	visible := p.getSent()
	if strings.Contains(visible[len(visible)-1], "original topic") {
		t.Fatalf("live conversation replayed the root task: %q", visible)
	}
	if err := e.Stop(); err != nil {
		t.Fatal(err)
	}
	e = newEngine()
	send("retry", &InitialMessageContext{Text: "original topic"})
	visible = p.getSent()
	if len(visible) < 3 || strings.Contains(visible[len(visible)-1], "original topic") || !strings.Contains(visible[len(visible)-1], "retry") {
		t.Fatalf("retry after restart replayed the root task: %q", visible)
	}
}
