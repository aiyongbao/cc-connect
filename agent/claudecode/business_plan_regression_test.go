package claudecode

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/chenhg5/cc-connect/agent/planguard"
	"github.com/chenhg5/cc-connect/core"
)

func TestClaudeBusinessPlan_MergesExplicitSettingsWithoutLosingDenyOrHooks(t *testing.T) {
	for _, form := range []string{"inline", "equals", "relative-file", "last-wins"} {
		t.Run(form, func(t *testing.T) {
			dir := t.TempDir()
			home := t.TempDir()
			user := `{"env":{"CC_REVIEW_SENTINEL":"retained"},"permissions":{"deny":["Bash"]},"hooks":{"PreToolUse":[{"matcher":"Read","hooks":[{"type":"command","command":"user-hook"}]}]}}`
			args := []string{"wrapper-command", "--settings", user, "--model", "test"}
			switch form {
			case "equals":
				args = []string{"--settings=" + user}
			case "relative-file":
				if err := os.WriteFile(filepath.Join(dir, "settings.json"), []byte("// comment\n"+user), 0600); err != nil {
					t.Fatal(err)
				}
				args = []string{"--settings", "settings.json"}
			case "last-wins":
				args = []string{"--settings", `{"disableAllHooks":true}`, "--settings", user}
			}
			ctx, cancel := context.WithCancel(core.WithBusinessPlanning(context.Background()))
			defer cancel()
			cs, err := newClaudeSession(ctx, dir, os.Args[0], append([]string{"_cc-plan-test-cli"}, args...), "", "test-model", "", "saved-conversation", "default", "", "", nil, nil, nil, []string{"CLAUDE_CONFIG_DIR=" + home}, "", false, core.SpawnOptions{}, 0, 0, t.TempDir(), core.LangEnglish)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = cs.Close() })
			var effective map[string]any
			settingsCount := 0
			for i, arg := range cs.cmd.Args {
				if arg == "--settings" {
					settingsCount++
					value := []byte(cs.cmd.Args[i+1])
					if !strings.HasPrefix(strings.TrimSpace(string(value)), "{") {
						var err error
						value, err = os.ReadFile(string(value))
						if err != nil {
							t.Fatal(err)
						}
					}
					if err := json.Unmarshal(stripJSONC(value), &effective); err != nil {
						t.Fatal(err)
					}
				}
			}
			env, _ := effective["env"].(map[string]any)
			if settingsCount != 1 || env["CC_REVIEW_SENTINEL"] != "retained" {
				t.Fatalf("explicit CLI settings were replaced (settings flags=%d)", settingsCount)
			}
			if strings.Contains(strings.Join(cs.cmd.Args, " "), "CC_REVIEW_SENTINEL") {
				t.Fatal("merged settings leaked into argv")
			}
			deny := effective["permissions"].(map[string]any)["deny"].([]any)
			if len(deny) != 1 || deny[0] != "Bash" {
				t.Fatal("tool deny rule was lost")
			}
			groups := effective["hooks"].(map[string]any)["PreToolUse"].([]any)
			wantHooks := 2
			if form == "relative-file" {
				wantHooks = 1 // the disk file stays intact and the guard is added at runtime
				if err := cs.verifyPlanningSettings(); err != nil {
					t.Fatal(err)
				}
			}
			if len(groups) != wantHooks || groups[0].(map[string]any)["matcher"] != "Read" {
				t.Fatal("user hook was lost")
			}
		})
	}
}

func newPolicyRegressionSession(t *testing.T, extraEnv ...string) (*claudeSession, string, string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("POSIX hook fixture")
	}
	dir, home := t.TempDir(), t.TempDir()
	marker := filepath.Join(dir, "business-marker")
	ctx, cancel := context.WithTimeout(core.WithBusinessPlanning(context.Background()), 15*time.Second)
	t.Cleanup(cancel)
	env := append([]string{"CLAUDE_CONFIG_DIR=" + home, "CC_TEST_PLAN_MARKER=" + marker}, extraEnv...)
	cs, err := newClaudeSession(ctx, dir, os.Args[0], []string{"_cc-plan-test-cli"}, "", "test-model", "", "saved-conversation", "bypassPermissions", "", "", nil, nil, nil, env, "", false, core.SpawnOptions{}, 0, 0, t.TempDir(), core.LangEnglish)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cs.Close() })
	return cs, home, marker
}

func TestClaudeBusinessPlan_RechecksDisabledPolicyBeforeResumedTurn(t *testing.T) {
	cs, home, marker := newPolicyRegressionSession(t)
	if err := os.WriteFile(filepath.Join(home, "settings.json"), []byte(`{"disableAllHooks":true}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := cs.Send(core.WithBusinessPlanInstructions("generate virtual report"), "turn", nil, nil); err == nil {
		t.Fatal("disabled policy did not stop resumed turn")
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("work executed after hook disable")
	}
	if cs.CurrentSessionID() != "saved-conversation" && cs.CurrentSessionID() != "plan-fixture" {
		t.Fatal("policy failure discarded saved conversation")
	}
	// Repairing configuration retries the same native transport.
	if err := os.WriteFile(filepath.Join(home, "settings.json"), []byte(`{}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := cs.Send(core.WithBusinessPlanInstructions("hello"), "retry", nil, nil); err != nil {
		t.Fatal(err)
	}
}

func TestClaudeBusinessPlan_DisableDuringTurnCannotBypassActualHook(t *testing.T) {
	cs, _, marker := newPolicyRegressionSession(t, "CC_TEST_PLAN_RELOAD_IN_TURN=1")
	if err := cs.Send(core.WithBusinessPlanInstructions("generate virtual report"), "turn", nil, nil); err != nil {
		t.Fatal(err)
	}
	for {
		select {
		case event := <-cs.Events():
			if event.Type == core.EventError {
				t.Fatal(event.Error)
			}
			if event.Type == core.EventResult && event.Done {
				if event.Content != "policy paused" || !cs.BusinessPlanBlocked() {
					t.Fatal("policy disable did not pause business work")
				}
				if _, err := os.Stat(marker); !os.IsNotExist(err) {
					t.Fatal("business tool executed after policy was disabled")
				}
				return
			}
		case <-cs.ctx.Done():
			t.Fatal("policy fixture did not finish")
		}
	}
}

func TestClaudeBusinessPlan_RejectsCLIWithoutEffectiveGuards(t *testing.T) {
	cs, _, _ := newPolicyRegressionSession(t, "CC_TEST_PLAN_DROP_EFFECTIVE_HOOKS=1")
	if err := cs.Send(core.WithBusinessPlanInstructions("generate virtual report"), "turn", nil, nil); err == nil {
		t.Fatal("CLI with missing effective hooks accepted a turn")
	}
}

func TestPlanningSettingsResponse_DoesNotLeakCredentials(t *testing.T) {
	g, err := planguard.New()
	if err != nil {
		t.Fatal(err)
	}
	defer g.Close()
	cs := &claudeSession{planGuard: g, planningWaiters: map[string]chan error{planningSettingsPrefix + "1": make(chan error, 1)}}
	cs.handlePlanningSettings(map[string]any{"response": map[string]any{"subtype": "success", "request_id": planningSettingsPrefix + "1", "response": map[string]any{"effective": map[string]any{"env": map[string]any{"API_KEY": "private-test-value"}}}}})
	if err := <-cs.planningWaiters[planningSettingsPrefix+"1"]; err == nil || strings.Contains(err.Error(), "private-test-value") {
		t.Fatal("unverified or sensitive settings error")
	}
}

func TestClaudeBusinessPlan_ExplicitSettingsFileChangeRequiresSafeReconnect(t *testing.T) {
	dir, home := t.TempDir(), t.TempDir()
	path := filepath.Join(dir, "user-settings.json")
	if err := os.WriteFile(path, []byte(`{"permissions":{"deny":["Bash"]}}`), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(core.WithBusinessPlanning(context.Background()), 15*time.Second)
	defer cancel()
	cs, err := newClaudeSession(ctx, dir, os.Args[0], []string{"_cc-plan-test-cli", "--settings", path}, "", "test-model", "", "saved-conversation", "default", "", "", nil, nil, nil, []string{"CLAUDE_CONFIG_DIR=" + home}, "", false, core.SpawnOptions{}, 0, 0, t.TempDir(), core.LangEnglish)
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()
	if cs.NeedsSessionRefresh() {
		t.Fatal("unchanged source requested a reconnect")
	}
	if err := os.WriteFile(path, []byte(`{"permissions":{"deny":["Bash","Read"]}}`), 0600); err != nil {
		t.Fatal(err)
	}
	if !cs.NeedsSessionRefresh() {
		t.Fatal("explicit settings change did not request a reconnect")
	}
	if err := cs.Send(core.WithBusinessPlanInstructions("generate virtual report"), "changed", nil, nil); err == nil {
		t.Fatal("old permission snapshot accepted work")
	}
}

func TestClaudeBusinessPlan_PolicyPathsUseCLIWorkingDirectory(t *testing.T) {
	dir := t.TempDir()
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	relative, err := filepath.Rel(cwd, dir)
	if err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(dir, "settings.json")
	if err := os.WriteFile(file, []byte(`{}`), 0600); err != nil {
		t.Fatal(err)
	}
	settings, err := splitPlanningSettings(relative, []string{"--settings", "settings.json"})
	if err != nil {
		t.Fatal(err)
	}
	if !filepath.IsAbs(settings.source) || !filepath.IsAbs(settings.resolvedSource) {
		t.Fatal("hook subprocess would resolve settings against a different cwd")
	}
	for _, p := range planningPolicyFiles(relative, []string{"CLAUDE_CONFIG_DIR=custom-config"}) {
		if !filepath.IsAbs(p) {
			t.Fatal("policy path is not absolute")
		}
	}
}

func TestClaudeBusinessPlan_UnsupportedFileOverlayDoesNotStartWork(t *testing.T) {
	dir, configHome := t.TempDir(), t.TempDir()
	source := filepath.Join(dir, "settings.json")
	if err := os.WriteFile(source, []byte(`{}`), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(core.WithBusinessPlanning(context.Background()), 10*time.Second)
	defer cancel()
	cs, err := newClaudeSession(ctx, dir, os.Args[0], []string{"_cc-plan-test-cli", "--settings", source}, "", "test-model", "", "saved-conversation", "default", "", "", nil, nil, nil, []string{"CLAUDE_CONFIG_DIR=" + configHome, "CC_TEST_PLAN_REJECT_OVERLAY=1"}, "", false, core.SpawnOptions{}, 0, 0, t.TempDir(), core.LangEnglish)
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()
	var setup *core.BusinessPlanSetupError
	if err := cs.Send(core.WithBusinessPlanInstructions("hello"), "test", nil, nil); !errors.As(err, &setup) {
		t.Fatalf("unsupported overlay did not stop the turn: %v", err)
	}
	if cs.planningOverlayApplied.Load() || cs.planGuard.Enabled() {
		t.Fatal("the rejected overlay activated a planning turn")
	}
}
