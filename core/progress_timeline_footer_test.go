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
				env.waitFor("turn completed", 2*time.Second, func() bool {
					return len(p.getPreviewUpdates()) > 0 && !env.engine.sessions.GetOrCreateActive("test:jack").Busy()
				})
				if tc.silent {
					if sent := p.getSent(); len(sent) != 0 {
						t.Fatalf("silent reply leaked: %v", sent)
					}
					return
				}
				sent := strings.Join(p.getSent(), "\n")
				if !strings.Contains(sent, answer) {
					t.Fatalf("final answer missing: %q", sent)
				}
				wantElapsed := 0
				if tc.enabled {
					wantElapsed = 1
				}
				if got := strings.Count(sent, "⏱ Elapsed "); got != wantElapsed {
					t.Fatalf("elapsed line count = %d, footer enabled = %v: %q", got, tc.enabled, sent)
				}
				if strings.Contains(sent, "timeline-footer-model") != (tc.enabled && tc.context) {
					t.Fatalf("model visibility does not respect flags: %q", sent)
				}
				if strings.Contains(sent, "timeline-footer-workspace") != (tc.enabled && tc.context && tc.dir) {
					t.Fatalf("workdir visibility does not respect flags: %q", sent)
				}
				if strings.Contains(sent, "FOOTER:") != (structured && tc.enabled) {
					t.Fatalf("footer did not use the expected sender: %q", sent)
				}
			})
		}
	}
}
