package core

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"
)

// Timeline must retain side-channel deduplication without dropping a different
// final answer, regardless of which configuration enables the progress style.
func TestTimelineFinalReply_SuppressesDuplicateSideChannelText(t *testing.T) {
	const sideText = "The file has been sent."
	for _, mode := range []struct{ style, cardMode string }{{"timeline", "rich"}, {"card", "timeline"}} {
		t.Run(mode.style+"-"+mode.cardMode, func(t *testing.T) {
			for _, tc := range []struct {
				name        string
				finalText   string
				inputTokens int
				wantCount   int
			}{
				{"duplicate", sideText, 0, 1},
				{"duplicateWithContext", sideText, 52000, 1},
				{"differentAnswer", "The file has been sent. Here are the instructions.", 0, 2},
			} {
				t.Run(tc.name, func(t *testing.T) {
					p := &timelineTestPlatform{style: mode.style}
					p.n = "test"
					e := NewEngine("test", &stubAgent{}, []Platform{p}, t.TempDir()+"/sessions.json", LangEnglish)
					e.display.CardMode = mode.cardMode
					const key = "test:user1"
					session := e.sessions.GetOrCreateActive(key)
					agentSession := newControllableSession("s1")
					state := &interactiveState{agentSession: agentSession, platform: p, replyCtx: "ctx-1"}
					e.interactiveStates[key] = state
					if err := e.SendToSessionWithAttachments(key, sideText, nil, nil, nil, false); err != nil {
						t.Fatal(err)
					}
					agentSession.events <- Event{Type: EventResult, Content: tc.finalText, InputTokens: tc.inputTokens, Done: true}
					e.processInteractiveEvents(state, session, e.sessions, key, "m1", time.Now(), nil, nil, nil, 0)
					got := p.getSent()
					if len(got) != tc.wantCount || got[0] != sideText {
						t.Fatalf("sent = %#v, want %d messages starting with side-channel text", got, tc.wantCount)
					}
					if tc.wantCount == 2 && got[1] != tc.finalText {
						t.Fatalf("final answer = %q, want %q", got[1], tc.finalText)
					}
					if lastTimelinePayload(t, p).State != ProgressCardStateCompleted {
						t.Fatal("duplicate suppression left the progress card unfinished")
					}
				})
			}
		})
	}
}

// MCP starts use a generic label, while results carry the actual tool name.
// Normalize before shortening arguments so even long names retain their result.
func TestTimelineProgress_MCPFailurePreservedAfterTurnCompletion(t *testing.T) {
	for _, server := range []string{"docs", "", strings.Repeat("long-server-", 30)} {
		t.Run("server="+server, func(t *testing.T) {
			state, p := timelineTestState()
			w := newTimelineProgressLocked(context.Background(), state, "test:u", "codex", LangEnglish, nil, "timeline")
			w.Start()
			display := DisplayCfg{ToolMessages: true}
			for _, tool := range []string{"search", "fetch"} {
				name := strings.Trim(server+":"+tool, ":")
				w.Handle(Event{Type: EventToolUse, ToolName: "MCP", ToolInput: name + "\n{}"}, display)
			}
			failed := false
			w.Handle(Event{Type: EventToolResult, ToolName: "fetch", ToolStatus: "failed", ToolSuccess: &failed}, display)
			items := lastTimelinePayload(t, p).Items
			if len(items) != 2 || items[0].Status != "running" || items[1].Status != "failed" {
				t.Errorf("out-of-order MCP result updated the wrong call: %+v", items)
			}
			w.Handle(Event{Type: EventToolResult, ToolName: "search", ToolStatus: "completed"}, display)
			w.Finalize(ProgressCardStateCompleted)
			items = lastTimelinePayload(t, p).Items
			if len(items) != 2 || items[0].Status != "completed" || items[1].Status != "failed" {
				t.Fatalf("completed turn lost failed MCP result: %+v", items)
			}
		})
	}
}

// Intentionally ignore request cancellation to ensure slow external I/O never
// keeps an engine/session lock held, even when a platform is unresponsive.
type timelineBlockingPlatform struct {
	timelineTestPlatform
	blockStart bool
	entered    chan struct{}
	release    chan struct{}
	once       sync.Once
}

func (p *timelineBlockingPlatform) blockRequest() {
	p.once.Do(func() {
		close(p.entered)
		<-p.release
	})
}

func (p *timelineBlockingPlatform) SendPreviewStart(ctx context.Context, replyCtx any, content string) (any, error) {
	if p.blockStart {
		p.blockRequest()
	}
	return p.cujStreamingPlatform.SendPreviewStart(ctx, replyCtx, content)
}

func (p *timelineBlockingPlatform) UpdateMessage(ctx context.Context, handle any, content string) error {
	if !p.blockStart {
		p.blockRequest()
	}
	return p.cujStreamingPlatform.UpdateMessage(ctx, handle, content)
}

// A typo followed immediately by /new must cancel the old turn even if its
// initial progress card is still in flight, and the next prompt must work.
func TestCUJ_STREAM2_TimelineNewDuringInitialCard(t *testing.T) {
	p := &timelineBlockingPlatform{blockStart: true, entered: make(chan struct{}), release: make(chan struct{})}
	p.style, p.n = "timeline", "test"
	release := sync.OnceFunc(func() { close(p.release) })
	t.Cleanup(release)
	a := &cujAgent{}
	e := NewEngine("test", a, []Platform{p}, t.TempDir()+"/sessions.json", LangEnglish)
	t.Cleanup(e.cancel)
	e.display.CardMode = "timeline"
	env := &cujEnv{t: t, engine: e, plat: &p.stubPlatformEngine, agent: a}
	old := e.sessions.GetOrCreateActive("test:jack")
	sendTimelineCUJ(env, p, "/enw")
	select {
	case <-p.entered:
	case <-time.After(2 * time.Second):
		t.Fatal("initial card did not start")
	}
	sendTimelineCUJ(env, p, "/new")
	env.waitFor("new session", 2*time.Second, func() bool {
		return strings.Contains(strings.Join(p.getSent(), "\n"), e.i18n.T(MsgNewSessionCreated))
	})
	release()
	env.waitFor("old turn exited", 2*time.Second, func() bool { return !old.Busy() })
	if got := lastTimelinePayload(t, &p.timelineTestPlatform).State; got != ProgressCardStateInterrupted {
		t.Fatalf("old card state = %q, want interrupted", got)
	}
	if strings.Contains(strings.Join(p.getSent(), "\n"), "ok") {
		t.Fatal("cancelled turn sent its final answer")
	}
	sendTimelineCUJ(env, p, "continue after reset")
	env.waitFor("next prompt answered", 2*time.Second, func() bool {
		return strings.Contains(strings.Join(p.getSent(), "\n"), "ok") && !e.sessions.GetOrCreateActive("test:jack").Busy()
	})
	if lastTimelinePayload(t, &p.timelineTestPlatform).State != ProgressCardStateCompleted {
		t.Fatal("new turn did not complete")
	}
}

// Both stop entrypoints must release other sessions before slow card I/O ends.
// The final interrupted card must still follow the in-flight running snapshot.
func TestTimelineStop_SlowCardRequestDoesNotBlockOtherSessions(t *testing.T) {
	for _, action := range []string{"button", "command"} {
		for _, request := range []string{"start", "update"} {
			t.Run(action+"/"+request, func(t *testing.T) {
				p := &timelineBlockingPlatform{blockStart: request == "start", entered: make(chan struct{}), release: make(chan struct{})}
				p.style, p.n = "timeline", "test"
				release := sync.OnceFunc(func() { close(p.release) })
				t.Cleanup(release)
				e := NewEngine("test", &cujAgent{}, []Platform{p}, t.TempDir()+"/sessions.json", LangEnglish)
				t.Cleanup(e.cancel)
				state := &interactiveState{platform: p, replyCtx: "ctx"}
				e.interactiveStates["test:u"] = state
				w := newTimelineProgressLocked(e.ctx, state, "test:u", "codex", LangEnglish, nil, "timeline")
				token := w.payload.TurnID
				if !p.blockStart {
					w.Start()
				}
				updated := make(chan struct{})
				go func() {
					if p.blockStart {
						w.Start()
					} else {
						w.Handle(Event{Type: EventThinking, Content: "slow update"}, DisplayCfg{ThinkingMessages: true})
					}
					close(updated)
				}()
				select {
				case <-p.entered:
				case <-time.After(time.Second):
					t.Fatal("card request did not start")
				}
				stopped := make(chan struct{})
				go func() {
					if action == "button" {
						e.stopTimelineTurn("test:u", token)
					} else {
						e.stopInteractiveSessionWithOptions("test:u", false)
					}
					close(stopped)
				}()
				select {
				case <-w.stopped:
				case <-time.After(time.Second):
					t.Error("stop signal waited on a card request")
				}
				queued := make(chan struct{})
				go func() {
					e.ensureInteractiveStateForQueueing("test:v", p, "other-context")
					close(queued)
				}()
				select {
				case <-queued:
				case <-time.After(time.Second):
					t.Error("stop blocked an unrelated session behind a card request")
				}
				release()
				for _, done := range []<-chan struct{}{updated, stopped, queued} {
					select {
					case <-done:
					case <-time.After(time.Second):
						t.Fatal("request/stop did not finish after release")
					}
				}
				if len(p.getPreviewOpens()) != 1 || lastTimelinePayload(t, &p.timelineTestPlatform).State != ProgressCardStateInterrupted {
					t.Fatal("in-flight progress overwrote interruption or opened a duplicate card")
				}
			})
		}
	}
}
