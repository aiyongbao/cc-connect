package core

import (
	"strings"
	"testing"
)

func TestTaskProgressInstructions_OnlyForEligibleTimelineTargets(t *testing.T) {
	for _, tc := range []struct {
		name, style, mode string
		want              bool
	}{
		{"global timeline", "card", "timeline", true},
		{"platform timeline", "timeline", "legacy", true},
		{"legacy", "legacy", "legacy", false},
		{"rich", "card", "rich", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env, p := newTimelineCUJEnv(t, tc.style, tc.mode)
			t.Cleanup(func() { _ = env.engine.Stop() })
			got := env.engine.withTaskProgressInstructions(p, "reply", "用户原始请求")
			if strings.Contains(got, taskProgressInstructions) != tc.want || !strings.HasSuffix(got, "用户原始请求") {
				t.Fatalf("instruction eligibility or original request changed: %q", got)
			}
		})
	}
	e := &Engine{display: DisplayCfg{CardMode: "timeline"}}
	if got := e.withTaskProgressInstructions(&stubPlatformEngine{}, nil, "hello"); got != "hello" {
		t.Fatalf("unsupported platform received task instructions: %q", got)
	}
}
