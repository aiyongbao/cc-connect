package claudecode

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/chenhg5/cc-connect/agent/planguard"
	"github.com/chenhg5/cc-connect/core"
)

func TestMain(m *testing.M) {
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "_cc-plan-hook":
			if len(os.Args) != 3 || planguard.RunHook(os.Args[2], os.Stdin, os.Stdout) != nil {
				os.Exit(2)
			}
			return
		case "_cc-plan-mcp":
			if len(os.Args) != 3 || planguard.RunMCP(os.Args[2], os.Stdin, os.Stdout) != nil {
				os.Exit(2)
			}
			return
		case "_cc-plan-test-cli":
			if err := runPlanningCLIFixture(); err != nil {
				fmt.Fprintln(os.Stderr, err)
				os.Exit(1)
			}
			return
		}
	}
	os.Exit(m.Run())
}

// This process stands in for the CLI boundary, not for the guard: it runs the
// exact injected hook command as a subprocess before performing a marker write.
// It reproduces today's ToolSearch → Bash omission, then lets the model recover
// with real TaskCreate results. No provider, IM, or business source is contacted.
func runPlanningCLIFixture() error {
	var settings struct {
		Hooks map[string][]struct {
			Hooks []struct {
				Command string `json:"command"`
			} `json:"hooks"`
		} `json:"hooks"`
	}
	for i, a := range os.Args {
		if a == "--settings" && i+1 < len(os.Args) {
			if err := json.Unmarshal([]byte(os.Args[i+1]), &settings); err != nil {
				return err
			}
		}
	}
	if len(settings.Hooks["PreToolUse"]) != 1 {
		return fmt.Errorf("missing pre-execution guard")
	}
	enc := json.NewEncoder(os.Stdout)
	emit := func(v any) {
		if err := enc.Encode(v); err != nil {
			panic(err)
		}
	}
	hook := func(event, name, id string, input map[string]any, response any) (bool, error) {
		command := settings.Hooks[event][0].Hooks[0].Command
		data, _ := json.Marshal(map[string]any{"hook_event_name": event, "tool_name": name, "tool_use_id": id, "tool_input": input, "tool_response": response})
		cmd := exec.Command("sh", "-c", command)
		cmd.Stdin = bytes.NewReader(data)
		out, err := cmd.Output()
		return strings.Contains(string(out), `"permissionDecision":"deny"`), err
	}
	tool := func(name, id string, input map[string]any, response any) (bool, error) {
		emit(map[string]any{"type": "assistant", "message": map[string]any{"content": []any{map[string]any{"type": "tool_use", "name": name, "id": id, "input": input}}}})
		denied, err := hook("PreToolUse", name, id, input, nil)
		if err != nil {
			return false, err
		}
		if denied {
			emit(map[string]any{"type": "user", "message": map[string]any{"content": []any{map[string]any{"type": "tool_result", "tool_use_id": id, "is_error": true, "content": "Planning barrier blocked this tool"}}}})
			return false, nil
		}
		if name == "Bash" {
			if err := os.WriteFile(os.Getenv("CC_TEST_PLAN_MARKER"), []byte("virtual profit report"), 0600); err != nil {
				return false, err
			}
		}
		if _, err = hook("PostToolUse", name, id, input, response); err != nil {
			return false, err
		}
		emit(map[string]any{"type": "user", "tool_use_result": response, "message": map[string]any{"content": []any{map[string]any{"type": "tool_result", "tool_use_id": id, "content": "success"}}}})
		return true, nil
	}
	emit(map[string]any{"type": "system", "subtype": "init", "session_id": "plan-fixture", "model": "test-model"})
	scanner := bufio.NewScanner(os.Stdin)
	scanner.Buffer(make([]byte, 4096), 1<<20)
	turn := 0
	for scanner.Scan() {
		var request struct {
			Type    string `json:"type"`
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		}
		if json.Unmarshal(scanner.Bytes(), &request) != nil || request.Type != "user" {
			continue
		}
		turn++
		if strings.HasSuffix(request.Message.Content, "hello") {
			emit(map[string]any{"type": "result", "subtype": "success", "session_id": "plan-fixture", "result": "hello"})
			continue
		}
		if ok, err := tool("Bash", fmt.Sprintf("early-%d", turn), map[string]any{"command": "generate virtual report"}, nil); err != nil || ok {
			return fmt.Errorf("unplanned work executed: %v", err)
		}
		if strings.HasSuffix(request.Message.Content, "skip-only") {
			emit(map[string]any{"type": "result", "subtype": "success", "session_id": "plan-fixture", "result": "false completion claim"})
			continue
		}
		_, err := tool("ToolSearch", fmt.Sprintf("search-%d", turn), map[string]any{"query": "select:TaskCreate,TaskUpdate"}, map[string]any{"tools": []string{"TaskCreate", "TaskUpdate"}})
		if err != nil {
			return err
		}
		if ok, err := tool("Bash", fmt.Sprintf("after-search-%d", turn), nil, nil); err != nil || ok {
			return fmt.Errorf("discovery authorized work: %v", err)
		}
		for i, title := range []string{"核对虚拟利润数据", "编写经营日报"} {
			id := fmt.Sprint(i + 1)
			if ok, err := tool("TaskCreate", fmt.Sprintf("create-%d-%d", turn, i), map[string]any{"subject": title}, map[string]any{"task": map[string]any{"id": id, "subject": title, "status": "pending"}}); err != nil || !ok {
				return fmt.Errorf("plan creation failed: %v", err)
			}
		}
		if ok, err := tool("Bash", fmt.Sprintf("business-%d", turn), nil, map[string]any{"success": true}); err != nil || !ok {
			return fmt.Errorf("planned work blocked: %v", err)
		}
		for i := 0; i < 2; i++ {
			id := fmt.Sprint(i + 1)
			if ok, err := tool("TaskUpdate", fmt.Sprintf("finish-%d-%d", turn, i), map[string]any{"taskId": id, "status": "completed"}, map[string]any{"success": true, "taskId": id}); err != nil || !ok {
				return fmt.Errorf("plan completion failed: %v", err)
			}
		}
		emit(map[string]any{"type": "result", "subtype": "success", "session_id": "plan-fixture", "result": "virtual report ready"})
	}
	return scanner.Err()
}

func TestClaudeBusinessPlan_GatesActualExecutionBeforeNativeTasksAndAcrossTurns(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX CLI fixture")
	}
	ctx, cancel := context.WithTimeout(core.WithBusinessPlanning(context.Background()), 60*time.Second)
	defer cancel()
	dir := t.TempDir()
	marker := filepath.Join(dir, "business-started")
	cs, err := newClaudeSession(ctx, dir, os.Args[0], []string{"_cc-plan-test-cli"}, "", "test-model", "", "", "bypassPermissions", "", "", nil, nil, nil, []string{"CC_TEST_PLAN_MARKER=" + marker}, "", false, core.SpawnOptions{}, 0, 0, t.TempDir(), core.LangChinese)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cs.Close() })
	for _, request := range []string{"generate virtual report", "skip-only", "hello"} {
		_ = os.Remove(marker)
		if err := cs.Send(core.WithBusinessPlanInstructions(request), "test", nil, nil); err != nil {
			t.Fatal(err)
		}
		plans := 0
		finished := false
		for !finished {
			select {
			case event := <-cs.Events():
				if event.Type == core.EventTaskProgress && event.TaskProgress != nil {
					plans++
					if len(event.TaskProgress.Steps) > 0 && strings.Contains(event.TaskProgress.Steps[0].Title, "Bash") {
						t.Fatal("invented technical plan")
					}
				}
				if event.Type == core.EventResult && event.Done {
					finished = true
				}
				if event.Type == core.EventError {
					t.Fatal(event.Error)
				}
			case <-ctx.Done():
				t.Fatal("fixture did not finish")
			}
		}
		_, statErr := os.Stat(marker)
		switch request {
		case "generate virtual report":
			if statErr != nil || plans == 0 || cs.BusinessPlanBlocked() {
				t.Fatalf("report did not recover with a real plan: marker=%v plans=%d blocked=%v", statErr, plans, cs.BusinessPlanBlocked())
			}
		case "skip-only":
			if !os.IsNotExist(statErr) || plans != 0 || !cs.BusinessPlanBlocked() {
				t.Fatalf("resumed turn reused previous plan: marker=%v plans=%d", statErr, plans)
			}
		case "hello":
			if !os.IsNotExist(statErr) || plans != 0 || cs.BusinessPlanBlocked() {
				t.Fatal("greeting performed tools or created tasks")
			}
		}
	}
}

func TestClaudeBusinessPlan_ExplicitlyDisabledHooksAreNotOverridden(t *testing.T) {
	for _, policy := range []string{`{"disableAllHooks":true}`, `{"allowManagedHooksOnly":true}`} {
		home := t.TempDir()
		if err := os.WriteFile(filepath.Join(home, "settings.json"), []byte(policy), 0600); err != nil {
			t.Fatal(err)
		}
		if err := checkBusinessPlanningHooks(t.TempDir(), []string{"CLAUDE_CONFIG_DIR=" + home}, nil); err == nil {
			t.Fatal("disabled hook policy was ignored")
		}
	}
}

func TestClaudeBusinessPlan_IsolatedUserCannotStartWithoutAccessibleGuard(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("run_as_user is rejected by configuration validation on Windows")
	}
	ctx := core.WithBusinessPlanning(context.Background())
	cs, err := newClaudeSession(ctx, t.TempDir(), "nonexistent", nil, "", "", "", "", "", "", "", nil, nil, nil, nil, "", false, core.SpawnOptions{RunAsUser: "other-user"}, 0, 0, t.TempDir(), core.LangEnglish)
	if err == nil || cs != nil || !strings.Contains(err.Error(), "run_as_user") {
		t.Fatal("an isolated user started without a usable planning guard")
	}
}
