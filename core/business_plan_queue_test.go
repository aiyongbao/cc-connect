package core

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"
)

type queuedScopeAgent struct {
	cujAgent
	scopes  []bool
	resumes []string
	refresh atomic_bool
}
type queuedScopeSession struct {
	*cujAgentSession
	scoped    bool
	owner     *queuedScopeAgent
	refreshed bool
}

func (s *queuedScopeSession) NeedsSessionRefresh() bool    { return s.owner.refresh.Get() != s.refreshed }
func (s *queuedScopeSession) BusinessPlanningScoped() bool { return s.scoped }
func (s *queuedScopeSession) Send(prompt, id string, images []ImageAttachment, files []FileAttachment) error {
	if BusinessPlanRequested(prompt) != s.scoped {
		return &BusinessPlanSetupError{Err: fmt.Errorf("incorrect planning scope for queued request")}
	}
	s.mu.Lock()
	s.reply = "completed " + prompt[strings.LastIndex(prompt, "\n")+1:]
	s.mu.Unlock()
	return s.cujAgentSession.Send(prompt, id, images, files)
}
func (a *queuedScopeAgent) StartSession(ctx context.Context, id string) (AgentSession, error) {
	s, err := a.cujAgent.StartSession(ctx, id)
	if err != nil {
		return nil, err
	}
	a.mu.Lock()
	a.scopes = append(a.scopes, BusinessPlanningEnabled(ctx))
	a.resumes = append(a.resumes, id)
	a.mu.Unlock()
	return &queuedScopeSession{cujAgentSession: s.(*cujAgentSession), scoped: BusinessPlanningEnabled(ctx), owner: a, refreshed: a.refresh.Get()}, nil
}

// CUJ: start work, queue two follow-ups, reload the display mode, and continue
// the same saved conversation. Both queued requests must reach the user.
func TestCUJ_STREAM8_QueuedModeChangePreservesConversationAndAllMessages(t *testing.T) {
	for _, modes := range [][2]string{{"legacy", "timeline"}, {"timeline", "legacy"}, {"timeline", "timeline"}} {
		t.Run(modes[0]+"_to_"+modes[1], func(t *testing.T) {
			p := &timelineTestPlatform{style: "card"}
			p.n = "test"
			a := &queuedScopeAgent{}
			a.setNextSessionEvents([]Event{{Type: EventThinking, Content: "held first turn"}}, 0)
			e := NewEngine("test", a, []Platform{p}, t.TempDir()+"/sessions.json", LangEnglish)
			e.display.CardMode = modes[0]
			e.SetStreamPreviewCfg(StreamPreviewCfg{Enabled: true})
			env := &cujEnv{t: t, engine: e, plat: &p.stubPlatformEngine}
			t.Cleanup(func() { _ = e.Stop() })
			sendTimelineCUJ(env, p, "first")
			env.waitFor("first delivery", 2*time.Second, func() bool {
				a.mu.Lock()
				defer a.mu.Unlock()
				return len(a.sessions) == 1 && len(a.sessions[0].getSentPrompts()) == 1
			})
			sendTimelineCUJ(env, p, "second")
			sendTimelineCUJ(env, p, "third")
			env.waitFor("both queued", 2*time.Second, func() bool {
				return strings.Count(strings.Join(p.getSent(), "\n"), e.i18n.T(MsgMessageQueued)) >= 2
			})
			// Same operation performed by /reload, while the first turn is held.
			if modes[0] == modes[1] {
				a.refresh.Set(true)
			} else {
				e.SetDisplayConfig(DisplayCfg{CardMode: modes[1]})
			}
			a.mu.Lock()
			first := a.sessions[0]
			a.mu.Unlock()
			first.events <- Event{Type: EventResult, Content: "first complete", Done: true}
			env.waitFor("all replies", 3*time.Second, func() bool {
				sent := strings.Join(p.getSent(), "\n")
				return strings.Contains(sent, "completed second") && strings.Contains(sent, "completed third") && !e.sessions.GetOrCreateActive("test:jack").Busy()
			})
			a.mu.Lock()
			defer a.mu.Unlock()
			if len(a.scopes) != 2 || a.scopes[1] != (modes[1] == "timeline") || a.resumes[1] != "cuj-agent-session" {
				t.Fatalf("lost scope or saved conversation: scopes=%v resumes=%v", a.scopes, a.resumes)
			}
			history := e.sessions.GetOrCreateActive("test:jack").GetHistory(0)
			var users []string
			for _, entry := range history {
				if entry.Role == "user" {
					users = append(users, entry.Content)
				}
			}
			if strings.Join(users, ",") != "first,second,third" {
				t.Fatalf("lost or duplicated queued history: %v", users)
			}
		})
	}
}
