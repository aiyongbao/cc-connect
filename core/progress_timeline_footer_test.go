package core

import (
	"context"
	"strings"
	"testing"
	"time"
)

// Supply recognizable metadata so hiding the other footer lines is observable.
type timelineFooterAgent struct{ *cujAgent }

func (a *timelineFooterAgent) GetModel() string   { return "timeline-footer-model" }
func (a *timelineFooterAgent) GetWorkDir() string { return "/tmp/timeline-footer-workspace" }

// Record the separate body/footer boundary through the same synchronized sender.
type timelineFooterPlatform struct{ *timelineTestPlatform }

func (p *timelineFooterPlatform) SendWithStatusFooter(ctx context.Context, replyCtx any, body, footer string) error {
	return p.Send(ctx, replyCtx, body+"\nFOOTER:\n"+footer)
}

// The elapsed line must survive hiding both optional metadata lines, while the
// master toggle and silent replies still suppress the entire final footer.
func TestTimelineFinalReply_ElapsedFooterRespectsDisplayFlags(t *testing.T) {
	for _, structured := range []bool{false, true} {
		for _, tc := range []struct {
			name                  string
			enabled, context, dir bool
			silent                bool
		}{
			{name: "elapsed-only", enabled: true},
			{name: "all-metadata", enabled: true, context: true, dir: true},
			{name: "without-workdir", enabled: true, context: true},
			{name: "disabled", context: true, dir: true},
			{name: "silent", enabled: true, context: true, dir: true, silent: true},
		} {
			name := "inline/" + tc.name
			if structured {
				name = "structured/" + tc.name
			}
			t.Run(name, func(t *testing.T) {
				env, p := newTimelineCUJEnv(t, "card", "timeline")
				env.engine.agent = &timelineFooterAgent{env.agent}
				env.engine.SetReplyFooterEnabled(tc.enabled)
				env.engine.SetShowContextIndicator(tc.context)
				env.engine.SetShowWorkdirIndicator(tc.dir)
				var platform Platform = p
				if structured {
					platform = &timelineFooterPlatform{p}
				}
				answer := "footer test answer"
				if tc.silent {
					answer = "NO_REPLY"
				}
				env.agent.setNextSessionEvents([]Event{{Type: EventResult, Content: answer, Done: true}}, 0)
				sendTimelineCUJ(env, platform, "check footer")
				// Quiet turns never render a card; completion is observable via
				// the session leaving its busy state.
				env.waitFor("turn completed", 2*time.Second, func() bool {
					return !env.engine.sessions.GetOrCreateActive("test:jack").Busy()
				})
				if tc.silent {
					if sent := p.getSent(); len(sent) != 0 {
						t.Fatalf("silent reply leaked: %v", sent)
					}
					return
				}
				// Quiet turns embed the answer (and its footer) in the terminal
				// card, so assertions cover both plain messages and card content.
				sent := strings.Join(p.getSent(), "\n")
				cards := strings.Join(p.getPreviewOpens(), "\n")
				for _, u := range p.getPreviewUpdates() {
					cards += "\n" + u.Content
				}
				rendered := sent + "\n" + cards
				if !strings.Contains(rendered, answer) {
					t.Fatalf("final answer missing: %q", rendered)
				}
				wantElapsed := 0
				if tc.enabled {
					wantElapsed = 1
				}
				if got := strings.Count(rendered, "⏱ Elapsed "); got != wantElapsed {
					t.Fatalf("elapsed line count = %d, footer enabled = %v: %q", got, tc.enabled, rendered)
				}
				if strings.Contains(rendered, "timeline-footer-model") != (tc.enabled && tc.context) {
					t.Fatalf("model visibility does not respect flags: %q", rendered)
				}
				if strings.Contains(rendered, "timeline-footer-workspace") != (tc.enabled && tc.context && tc.dir) {
					t.Fatalf("workdir visibility does not respect flags: %q", rendered)
				}
				// Quiet turns always render inside the terminal card, so the
				// structured footer sender is never involved for them.
				if strings.Contains(rendered, "FOOTER:") {
					t.Fatalf("footer did not use the expected sender: %q", sent)
				}
			})
		}
	}
}
