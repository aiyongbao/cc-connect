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
	bin, err := exec.LookPath("claude")
	if err != nil {
		t.Fatal(err)
	}
	dir, home := t.TempDir(), t.TempDir()
	policy, marker := filepath.Join(home, "settings.json"), filepath.Join(dir, "unplanned-business")
	if err := os.WriteFile(policy, []byte(`{}`), 0600); err != nil {
		t.Fatal(err)
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
		w.Header().Set("Content-Type", "text/event-stream")
		emit := func(name string, v any) { b, _ := json.Marshal(v); fmt.Fprintf(w, "event: %s\ndata: %s\n\n", name, b) }
		emit("message_start", map[string]any{"type": "message_start", "message": map[string]any{"id": "msg_review", "type": "message", "role": "assistant", "model": req.Model, "content": []any{}, "stop_reason": nil, "stop_sequence": nil, "usage": map[string]int{"input_tokens": 100, "output_tokens": 0}}})
		stop := "end_turn"
		if tool {
			emit("content_block_start", map[string]any{"type": "content_block_start", "index": 0, "content_block": map[string]any{"type": "tool_use", "id": "toolu_review", "name": "Bash", "input": map[string]any{}}})
			args, _ := json.Marshal(map[string]any{"command": "printf review > " + marker, "description": "Write a temporary review marker"})
			emit("content_block_delta", map[string]any{"type": "content_block_delta", "index": 0, "delta": map[string]any{"type": "input_json_delta", "partial_json": string(args)}})
			stop = "tool_use"
		} else {
			emit("content_block_start", map[string]any{"type": "content_block_start", "index": 0, "content_block": map[string]any{"type": "text", "text": ""}})
			emit("content_block_delta", map[string]any{"type": "content_block_delta", "index": 0, "delta": map[string]any{"type": "text_delta", "text": "fixture final reply"}})
		}
		emit("content_block_stop", map[string]any{"type": "content_block_stop", "index": 0})
		emit("message_delta", map[string]any{"type": "message_delta", "delta": map[string]any{"stop_reason": stop, "stop_sequence": nil}, "usage": map[string]int{"output_tokens": 20}})
		emit("message_stop", map[string]any{"type": "message_stop"})
	}))
	defer server.Close()
	ctx, cancel := context.WithTimeout(core.WithBusinessPlanning(context.Background()), 35*time.Second)
	defer cancel()
	env := []string{"CLAUDE_CONFIG_DIR=" + home, "ANTHROPIC_API_KEY=local-test-placeholder", "ANTHROPIC_AUTH_TOKEN=", "CLAUDE_CODE_OAUTH_TOKEN=", "ANTHROPIC_BASE_URL=" + server.URL, "CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1"}
	cs, err := newClaudeSession(ctx, dir, bin, nil, "", "claude-sonnet-4-5", "", "", "bypassPermissions", "", "", nil, nil, nil, env, "", false, core.SpawnOptions{}, 0, 0, t.TempDir(), core.LangEnglish)
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
