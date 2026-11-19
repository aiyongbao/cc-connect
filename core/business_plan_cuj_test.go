package core

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

type businessScopeAgent struct {
	cujAgent
	scopes []bool
}
type businessScopeSession struct {
	*cujAgentSession
	scoped bool
}

func (s *businessScopeSession) BusinessPlanningScoped() bool { return s.scoped }
func (a *businessScopeAgent) StartSession(ctx context.Context, id string) (AgentSession, error) {
	s, err := a.cujAgent.StartSession(ctx, id)
	if err != nil {
		return nil, err
	}
	flag := BusinessPlanningEnabled(ctx)
	a.scopes = append(a.scopes, flag)
	return &businessScopeSession{cujAgentSession: s.(*cujAgentSession), scoped: flag}, nil
}

func TestBusinessPlanScopeChangeRecyclesLiveTransportWithoutLosingConversation(t *testing.T) {
	p := &timelineTestPlatform{style: "card"}
	p.n = "test"
	a := &businessScopeAgent{}
	e := NewEngine("test", a, []Platform{p}, t.TempDir()+"/sessions.json", LangEnglish)
	t.Cleanup(func() { _ = e.Stop() })
	s := e.sessions.GetOrCreateActive("test:jack")
	for _, mode := range []string{"legacy", "timeline", "legacy"} {
		e.display.CardMode = mode
		state := e.getOrCreateInteractiveStateWith("test:jack", p, "reply", s, e.sessions, nil, "")
		if state.agentSession == nil {
			t.Fatal("transport did not start")
		}
		if state.agentSession.(BusinessPlanScopeReporter).BusinessPlanningScoped() != (mode == "timeline") {
			t.Fatal("display mode reused an incorrectly scoped CLI")
		}
	}
	if len(a.scopes) != 3 || a.scopes[0] || !a.scopes[1] || a.scopes[2] {
		t.Fatalf("wrong transport transitions: %+v", a.scopes)
	}
	if s.GetAgentSessionID() == "" {
		t.Fatal("display mode change discarded the conversation")
	}
}

type businessPlanCUJSession struct {
	*cujAgentSession
	blocked atomic_bool
}

func (s *businessPlanCUJSession) BusinessPlanBlocked() bool { return s.blocked.Get() }

func (s *businessPlanCUJSession) Send(prompt, id string, images []ImageAttachment, files []FileAttachment) error {
	s.blocked.Set(false)
	s.mu.Lock()
	switch {
	case strings.HasSuffix(prompt, "skip plan"):
		s.blocked.Set(true)
		s.pendingEvents = []Event{{Type: EventToolUse, ToolName: "Bash", ToolInput: "INTERNAL_COMMAND"}, {Type: EventToolResult, ToolStatus: "failed"}, {Type: EventResult, Content: "FALSE_COMPLETION", Done: true}}
	case strings.HasSuffix(prompt, "generate daily report"):
		s.pendingEvents = []Event{
			{Type: EventTaskProgress, TaskProgress: &TaskProgress{Source: "claude_tasks", Steps: []TaskProgressStep{{Title: "核对利润数据", Status: "in_progress"}, {Title: "编写经营日报", Status: "pending"}}}},
			{Type: EventToolUse, ToolName: "Bash", ToolInput: "INTERNAL_COMMAND"},
			{Type: EventText, Content: "INTERNAL_TECHNICAL_NARRATION"},
			{Type: EventTaskProgress, TaskProgress: &TaskProgress{Source: "claude_tasks", Steps: []TaskProgressStep{{Title: "核对利润数据", Status: "completed"}, {Title: "编写经营日报", Status: "completed"}}}},
			{Type: EventResult, Content: "虚拟利润表和日报已完成", Done: true},
		}
	default:
		s.pendingEvents = []Event{{Type: EventResult, Content: "hello", Done: true}}
	}
	s.mu.Unlock()
	return s.cujAgentSession.Send(prompt, id, images, files)
}

// CUJ: a missed plan is visibly paused rather than falsely completed; retry
// shows real business steps; a greeting neither reuses the plan nor runs tools.
func TestCUJ_STREAM6_MissedPlanPausesThenRetryShowsBusinessStepsAndGreetingStaysSimple(t *testing.T) {
	p := &timelineTestPlatform{style: "timeline"}
	p.n = "test"
	s := &businessPlanCUJSession{cujAgentSession: newCUJAgentSession()}
	e := NewEngine("test", &resultAgent{session: s}, []Platform{p}, t.TempDir()+"/sessions.json", LangChinese)
	e.display.CardMode = "timeline"
	e.SetStreamPreviewCfg(StreamPreviewCfg{Enabled: true})
	env := &cujEnv{t: t, engine: e, plat: &p.stubPlatformEngine}
	t.Cleanup(func() { _ = e.Stop() })

	sendTimelineCUJ(env, p, "skip plan")
	env.waitFor("planning stop", 2*time.Second, func() bool {
		return !e.sessions.GetOrCreateActive("test:jack").Busy() && strings.Contains(strings.Join(p.getSent(), "\n"), e.i18n.T(MsgTimelinePlanBlocked))
	})
	if strings.Contains(strings.Join(p.getSent(), "\n"), "FALSE_COMPLETION") {
		t.Fatal("blocked business work claimed completion")
	}
	if payload := lastTimelinePayload(t, p); payload.State != ProgressCardStatePaused || !payload.BusinessOnly || len(payload.Steps) != 0 {
		t.Fatalf("wrong planning stop card: %+v", payload)
	}

	sendTimelineCUJ(env, p, "generate daily report")
	env.waitFor("business report", 2*time.Second, func() bool {
		return !e.sessions.GetOrCreateActive("test:jack").Busy() && strings.Contains(strings.Join(p.getSent(), "\n"), "虚拟利润表和日报已完成")
	})
	payload := lastTimelinePayload(t, p)
	if !payload.BusinessOnly || len(payload.Steps) != 2 || payload.Steps[0].Title != "核对利润数据" || payload.Steps[1].Status != "completed" {
		t.Fatalf("real plan was not shown: %+v", payload)
	}
	if strings.Contains(payload.LatestActivity, "INTERNAL_") {
		t.Fatal("technical narration replaced business progress")
	}
	if strings.Contains(strings.Join(p.getSent(), "\n"), "INTERNAL_") {
		t.Fatal("technical progress escaped to chat")
	}

	before := len(p.getPreviewOpens())
	sendTimelineCUJ(env, p, "hello")
	env.waitFor("greeting", 2*time.Second, func() bool {
		return !e.sessions.GetOrCreateActive("test:jack").Busy() && strings.Contains(strings.Join(p.getSent(), "\n"), "hello")
	})
	if len(p.getPreviewOpens()) != before {
		t.Fatal("greeting opened a new task card")
	}
	for _, entry := range e.sessions.GetOrCreateActive("test:jack").GetHistory(0) {
		if strings.Contains(entry.Content, "FALSE_COMPLETION") || strings.Contains(entry.Content, taskProgressInstructions) {
			t.Fatal("false completion or runtime planning policy leaked into history")
		}
	}
}

func TestCUJ_STREAM7_PlanningSetupFailurePreservesHistoryAndResumeForRetry(t *testing.T) {
	env, p := newTimelineCUJEnv(t, "timeline", "timeline")
	t.Cleanup(func() { _ = env.engine.Stop() })
	session := env.engine.sessions.GetOrCreateActive("test:jack")
	session.SetAgentSessionID("saved-report-session", env.agent.Name())
	session.AddHistory("assistant", "saved previous report")
	env.engine.sessions.Save()
	env.agent.failStartCount = 1
	env.agent.failStartErr = &BusinessPlanSetupError{Err: errors.New("CLI cannot activate the planning hook")}

	sendTimelineCUJ(env, p, "generate daily report")
	env.waitFor("planning prerequisite failure", 2*time.Second, func() bool {
		return !session.Busy() && strings.Contains(strings.Join(p.getSent(), "\n"), env.engine.i18n.T(MsgTimelinePlanUnavailable))
	})
	if session.GetAgentSessionID() != "saved-report-session" {
		t.Fatal("planning failure discarded the saved conversation")
	}
	sendTimelineCUJ(env, p, "/history")
	env.waitFor("preserved history", 2*time.Second, func() bool { return strings.Contains(strings.Join(p.getSent(), "\n"), "saved previous report") })
	env.agent.setNextSessionEvents([]Event{{Type: EventResult, Content: "retry complete", Done: true}}, 0)
	sendTimelineCUJ(env, p, "retry after configuration is fixed")
	env.waitFor("retry result", 2*time.Second, func() bool {
		return !session.Busy() && strings.Contains(strings.Join(p.getSent(), "\n"), "retry complete")
	})
}
