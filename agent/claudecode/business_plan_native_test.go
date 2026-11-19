package claudecode

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/chenhg5/cc-connect/core"
)

// Opt-in local CLI compatibility test. The provider is an HTTP fixture on
// loopback; no real provider, production data, or messaging service is used.
func TestClaudeBusinessPlan_NativeCLIHotReloadCannotSkipGuard(t *testing.T) {
	if os.Getenv("CC_TEST_REAL_CLAUDE") != "1" {
		t.Skip("set CC_TEST_REAL_CLAUDE=1 for local CLI compatibility test")
	}
	t.Run("private-settings", func(t *testing.T) { testNativeCLIHotReload(t, false) })
	t.Run("file-overlay", func(t *testing.T) { testNativeCLIHotReload(t, true) })
}

func testNativeCLIHotReload(t *testing.T, useFile bool) {
	t.Helper()
	bin, err := exec.LookPath("claude")
	if err != nil {
		t.Fatal(err)
	}
	dir, home := t.TempDir(), t.TempDir()
	policy, marker := filepath.Join(home, "settings.json"), filepath.Join(dir, "unplanned-business")
	if err := os.WriteFile(policy, []byte(`{}`), 0600); err != nil {
		t.Fatal(err)
	}
	var args []string
	if useFile {
		source := filepath.Join(dir, "original-settings.json")
		if err := os.WriteFile(source, []byte(`{}`), 0600); err != nil {
			t.Fatal(err)
		}
		args = []string{"--settings", source}
	}
	var issued atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()
		if strings.Contains(r.URL.Path, "count_tokens") {
			fmt.Fprint(w, `{"input_tokens":100}`)
			return
		}
		var req struct {
			Model string `json:"model"`
			Tools []struct {
				Name string `json:"name"`
			} `json:"tools"`
		}
		if json.NewDecoder(r.Body).Decode(&req) != nil {
			http.Error(w, "bad request", 400)
			return
		}
		bash := false
		for _, tool := range req.Tools {
			if tool.Name == "Bash" {
				bash = true
			}
		}
		tool := bash && issued.CompareAndSwap(false, true)
		if tool {
			// Change policy after Send and its effective-settings check, while the
			// model turn is in flight, just before it requests substantive work.
			if err := os.WriteFile(policy, []byte(`{"disableAllHooks":true}`), 0600); err != nil {
				http.Error(w, "cannot change test policy", 500)
				return
			}
			// Allow the native settings watcher to observe the original policy.
			time.Sleep(2 * time.Second)
		}
		var call *nativeTestToolCall
		if tool {
			call = &nativeTestToolCall{"toolu_review", "Bash", map[string]any{"command": "printf review > " + marker, "description": "Write a temporary review marker"}}
		}
		writeNativeTestReply(w, req.Model, call)
	}))
	defer server.Close()
	ctx, cancel := context.WithTimeout(core.WithBusinessPlanning(context.Background()), 35*time.Second)
	defer cancel()
	env := []string{"CLAUDE_CONFIG_DIR=" + home, "ANTHROPIC_API_KEY=local-test-placeholder", "ANTHROPIC_AUTH_TOKEN=", "CLAUDE_CODE_OAUTH_TOKEN=", "ANTHROPIC_BASE_URL=" + server.URL, "CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1"}
	cs, err := newClaudeSession(ctx, dir, bin, args, "", "claude-sonnet-4-5", "", "", "bypassPermissions", "", "", nil, nil, nil, env, "", false, core.SpawnOptions{}, 0, 0, t.TempDir(), core.LangEnglish)
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()
	if err := cs.Send(core.WithBusinessPlanInstructions("Write the temporary review marker."), "native-review", nil, nil); err != nil {
		t.Fatal(err)
	}
	for {
		select {
		case event := <-cs.Events():
			if event.Type == core.EventError {
				t.Fatal(event.Error)
			}
			if event.Type == core.EventResult && event.Done {
				if !issued.Load() {
					t.Fatal("mock provider did not issue the business tool")
				}
				if _, err := os.Stat(marker); !os.IsNotExist(err) {
					t.Fatal("native CLI skipped hook and executed business tool")
				}
				if !cs.BusinessPlanBlocked() {
					t.Fatal("policy failure was not marked paused")
				}
				return
			}
		case <-ctx.Done():
			t.Fatal("native CLI fixture did not finish")
		}
	}
}

type nativeTestToolCall struct {
	id    string
	name  string
	input map[string]any
}

func writeNativeTestReply(w http.ResponseWriter, model string, call *nativeTestToolCall) {
	w.Header().Set("Content-Type", "text/event-stream")
	emit := func(name string, value any) {
		encoded, _ := json.Marshal(value)
		fmt.Fprintf(w, "event: %s\ndata: %s\n\n", name, encoded)
	}
	emit("message_start", map[string]any{"type": "message_start", "message": map[string]any{"id": "msg_review", "type": "message", "role": "assistant", "model": model, "content": []any{}, "stop_reason": nil, "stop_sequence": nil, "usage": map[string]int{"input_tokens": 100, "output_tokens": 0}}})
	stop := "end_turn"
	if call != nil {
		emit("content_block_start", map[string]any{"type": "content_block_start", "index": 0, "content_block": map[string]any{"type": "tool_use", "id": call.id, "name": call.name, "input": map[string]any{}}})
		input, _ := json.Marshal(call.input)
		emit("content_block_delta", map[string]any{"type": "content_block_delta", "index": 0, "delta": map[string]any{"type": "input_json_delta", "partial_json": string(input)}})
		stop = "tool_use"
	} else {
		emit("content_block_start", map[string]any{"type": "content_block_start", "index": 0, "content_block": map[string]any{"type": "text", "text": ""}})
		emit("content_block_delta", map[string]any{"type": "content_block_delta", "index": 0, "delta": map[string]any{"type": "text_delta", "text": "fixture final reply"}})
	}
	emit("content_block_stop", map[string]any{"type": "content_block_stop", "index": 0})
	emit("message_delta", map[string]any{"type": "message_delta", "delta": map[string]any{"stop_reason": stop, "stop_sequence": nil}, "usage": map[string]int{"output_tokens": 20}})
	emit("message_stop", map[string]any{"type": "message_stop"})
}

// Exercise the real CLI permission decision with an existing business plan.
// The settings directory differs from cwd and contains a literal glob character.
func TestClaudeBusinessPlan_NativeCLIPreservesPermissionPathBase(t *testing.T) {
	if os.Getenv("CC_TEST_REAL_CLAUDE") != "1" {
		t.Skip("set CC_TEST_REAL_CLAUDE=1 for local CLI compatibility test")
	}
	t.Run("file", func(t *testing.T) { testNativePermissionPathBase(t, false) })
	t.Run("symlink", func(t *testing.T) { testNativePermissionPathBase(t, true) })
}

func testNativePermissionPathBase(t *testing.T, linked bool) {
	t.Helper()
	bin, err := exec.LookPath("claude")
	if err != nil {
		t.Fatal(err)
	}
	// Use the resolved path for tool inputs, as Claude does for a settings
	// file. In particular, macOS may give t.TempDir a /var alias.
	workDir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	configHome := t.TempDir()
	settingsDir := filepath.Join(workDir, "settings [local] (test)")
	if err := os.Mkdir(settingsDir, 0700); err != nil {
		t.Fatal(err)
	}
	settingsPath := filepath.Join(settingsDir, "permissions.json")
	// The preexisting absolute allow is a positive control: the anchored deny
	// must still take precedence over it after the merge.
	workPattern := filepath.ToSlash(workDir)
	if runtime.GOOS == "windows" && len(filepath.VolumeName(workDir)) == 2 {
		workPattern = "/" + strings.ToLower(workPattern[:1]) + workPattern[2:]
	}
	settings, err := json.Marshal(map[string]any{"permissions": map[string]any{
		"allow": []string{"Edit(/allowed.txt)", "Edit(/" + workPattern + "/**/protected.txt)"},
		"deny":  []string{"Edit(/protected.txt)"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(settingsPath, settings, 0600); err != nil {
		t.Fatal(err)
	}
	settingsArg := filepath.Join("settings [local] (test)", "permissions.json")
	if linked {
		settingsArg = "linked-settings.json"
		if err := os.Symlink(settingsPath, filepath.Join(workDir, settingsArg)); err != nil {
			if runtime.GOOS == "windows" {
				t.Skipf("settings symlink unavailable: %v", err)
			}
			t.Fatal(err)
		}
	}
	allowed := filepath.Join(settingsDir, "allowed.txt")
	protected := filepath.Join(settingsDir, "protected.txt")
	otherProtected := filepath.Join(workDir, "protected.txt")
	const planTool = "mcp__cc_connect_plan__update_plan"
	calls := []*nativeTestToolCall{
		{"toolu_plan", planTool, map[string]any{"plan": []map[string]string{{"step": "Verify fixture permissions", "status": "in_progress"}}}},
		{"toolu_denied", "Write", map[string]any{"file_path": protected, "content": "test"}},
		{"toolu_allowed", "Write", map[string]any{"file_path": allowed, "content": "test"}},
		{"toolu_cwd_allowed", "Write", map[string]any{"file_path": otherProtected, "content": "test"}},
	}
	runNativePlanningCalls(t, bin, workDir, configHome, settingsArg, []string{planTool}, calls)

	if _, err := os.Stat(protected); !os.IsNotExist(err) {
		t.Fatal("CLI did not enforce the settings-relative deny")
	}
	for _, path := range []string{allowed, otherProtected} {
		data, err := os.ReadFile(path)
		if err != nil || string(data) != "test" {
			t.Fatalf("CLI did not preserve permission to write %s: %v", path, err)
		}
	}
}

func runNativePlanningCalls(t *testing.T, bin, workDir, configHome, settingsArg string, allowedTools []string, calls []*nativeTestToolCall) {
	t.Helper()
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()
		if strings.Contains(r.URL.Path, "count_tokens") {
			fmt.Fprint(w, `{"input_tokens":100}`)
			return
		}
		var req struct{ Model string }
		if json.NewDecoder(r.Body).Decode(&req) != nil {
			http.Error(w, "invalid mock request", http.StatusBadRequest)
			return
		}
		index := int(requests.Add(1)) - 1
		var call *nativeTestToolCall
		if index < len(calls) {
			call = calls[index]
		}
		writeNativeTestReply(w, req.Model, call)
	}))
	defer server.Close()
	ctx, cancel := context.WithTimeout(core.WithBusinessPlanning(context.Background()), 30*time.Second)
	defer cancel()
	env := []string{"CLAUDE_CONFIG_DIR=" + configHome, "ANTHROPIC_API_KEY=local-test-placeholder", "ANTHROPIC_AUTH_TOKEN=", "CLAUDE_CODE_OAUTH_TOKEN=", "ANTHROPIC_BASE_URL=" + server.URL, "CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1", "ENABLE_TOOL_SEARCH=false"}
	cs, err := newClaudeSession(ctx, workDir, bin, []string{"--settings", settingsArg}, "", "claude-sonnet-4-5", "", "", "dontAsk", "", "", allowedTools, nil, nil, env, "", false, core.SpawnOptions{}, 0, 0, t.TempDir(), core.LangEnglish)
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()
	if err := cs.Send(core.WithBusinessPlanInstructions("Check the local fixture permissions."), "native-permissions", nil, nil); err != nil {
		t.Fatal(err)
	}
	planConfirmed := false
	for {
		select {
		case event, ok := <-cs.Events():
			if !ok {
				t.Fatal("native CLI exited before completing the test")
			}
			if event.Type == core.EventError {
				t.Fatal(event.Error)
			}
			if event.Type == core.EventTaskProgress {
				planConfirmed = true
			}
			if event.Type == core.EventResult && event.Done {
				if !planConfirmed || requests.Load() <= int32(len(calls)) {
					t.Fatal("fixture did not confirm a plan and exercise all writes")
				}
				return
			}
		case <-ctx.Done():
			t.Fatal("native CLI permission fixture did not finish")
		}
	}
}

func TestClaudeBusinessPlan_NativeCLIPreservesBashSandboxPaths(t *testing.T) {
	if os.Getenv("CC_TEST_REAL_CLAUDE") != "1" {
		t.Skip("set CC_TEST_REAL_CLAUDE=1 for local CLI compatibility test")
	}
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		t.Skip("requires a supported Bash sandbox")
	}
	bin, err := exec.LookPath("claude")
	if err != nil {
		t.Fatal(err)
	}
	workDir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	settingsDir := filepath.Join(workDir, "policy (fixture)")
	for _, subdir := range []string{"permission-protected", "sandbox-protected"} {
		if err := os.MkdirAll(filepath.Join(settingsDir, subdir), 0700); err != nil {
			t.Fatal(err)
		}
	}
	// In addition to Edit rules, Claude's Bash sandbox resolves its own
	// filesystem entries relative to the file defining them.
	policy := []byte(`{"permissions":{"deny":["Edit(/permission-protected/**)"]},"sandbox":{"enabled":true,"failIfUnavailable":true,"allowUnsandboxedCommands":false,"filesystem":{"denyWrite":["sandbox-protected"]}}}`)
	settingsFile := filepath.Join(settingsDir, "settings.json")
	if err := os.WriteFile(settingsFile, policy, 0600); err != nil {
		t.Fatal(err)
	}
	unplanned := filepath.Join(settingsDir, "unplanned.txt")
	fromPermission := filepath.Join(settingsDir, "permission-protected", "fixture.txt")
	fromSandbox := filepath.Join(settingsDir, "sandbox-protected", "fixture.txt")
	allowed := filepath.Join(settingsDir, "allowed.txt")
	const planTool = "mcp__cc_connect_plan__update_plan"
	bash := func(id, path string) *nativeTestToolCall {
		quoted := "'" + strings.ReplaceAll(path, "'", "'\"'\"'") + "'"
		return &nativeTestToolCall{id, "Bash", map[string]any{"command": "touch " + quoted, "description": "Check local fixture policy"}}
	}
	calls := []*nativeTestToolCall{
		bash("toolu_before_plan", unplanned),
		{"toolu_plan", planTool, map[string]any{"plan": []map[string]string{{"step": "Verify fixture policy", "status": "in_progress"}}}},
		bash("toolu_permission", fromPermission),
		bash("toolu_sandbox", fromSandbox),
		bash("toolu_allowed", allowed),
	}
	runNativePlanningCalls(t, bin, workDir, t.TempDir(), settingsFile, []string{planTool, "Bash"}, calls)
	for _, path := range []string{unplanned, fromPermission, fromSandbox} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Errorf("policy did not block write to %s", path)
		}
	}
	if _, err := os.Stat(allowed); err != nil {
		t.Fatalf("allowed fixture write failed: %v", err)
	}
}
