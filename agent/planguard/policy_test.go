package planguard

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPlanningBarrier_HotReloadedPolicyDeniesEvenAnAlreadyPublishedPlan(t *testing.T) {
	for _, policy := range []string{`{"disableAllHooks":true}`, `{"allowManagedHooksOnly":true}`, `{invalid-json`} {
		t.Run(policy, func(t *testing.T) {
			g := newGuard(t)
			path := filepath.Join(t.TempDir(), "settings.json")
			if err := os.WriteFile(path, []byte(`{}`), 0600); err != nil {
				t.Fatal(err)
			}
			if err := g.SetHookPolicyFiles([]string{path}); err != nil {
				t.Fatal(err)
			}
			publish(t, g, `{"plan":[{"step":"检查虚拟数据","status":"in_progress"}]}`)
			if got := runHook(t, g, "PreToolUse", "Bash", nil, nil); strings.Contains(got, "deny") {
				t.Fatal(got)
			}
			if err := os.WriteFile(path, []byte(policy), 0600); err != nil {
				t.Fatal(err)
			}
			if got := runHook(t, g, "PreToolUse", "Bash", nil, nil); !strings.Contains(got, `"permissionDecision":"deny"`) {
				t.Fatal("policy hot reload authorized work")
			}
			if !g.Blocked() {
				t.Fatal("policy disable did not mark the turn paused")
			}
		})
	}
}

func TestPlanningBarrier_EffectiveClaudeHooksMustBothMatchPrivateGuard(t *testing.T) {
	g := newGuard(t)
	var settings map[string]any
	if err := json.Unmarshal([]byte(g.ClaudeArgs()[1]), &settings); err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(settings)
	if err := g.CheckClaudeSettings(data); err != nil {
		t.Fatal(err)
	}
	delete(settings["hooks"].(map[string]any), "PostToolUse")
	data, _ = json.Marshal(settings)
	if err := g.CheckClaudeSettings(data); err == nil {
		t.Fatal("missing post hook accepted")
	}
}
