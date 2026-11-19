package planguard

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/chenhg5/cc-connect/core"
)

func TestMain(m *testing.M) {
	if len(os.Args) == 3 && os.Args[1] == "_cc-plan-hook" {
		if RunHook(os.Args[2], os.Stdin, os.Stdout) != nil {
			os.Exit(2)
		}
		return
	}
	if len(os.Args) == 3 && os.Args[1] == "_cc-plan-mcp" {
		if RunMCP(os.Args[2], os.Stdin, os.Stdout) != nil {
			os.Exit(2)
		}
		return
	}
	os.Exit(m.Run())
}

func newGuard(t *testing.T) *Guard {
	t.Helper()
	g, err := New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(g.Close)
	if err = g.Begin(core.WithBusinessPlanInstructions("生成虚拟日利润表和日报")); err != nil {
		t.Fatal(err)
	}
	return g
}

var hookSequence atomic.Uint64

func runHook(t *testing.T, g *Guard, event, tool string, input map[string]any, response any) string {
	t.Helper()
	id := fmt.Sprintf("call-%d", hookSequence.Add(1))
	if event == "PostToolUse" {
		data, _ := json.Marshal(map[string]any{"hook_event_name": "PreToolUse", "tool_name": tool, "tool_input": input, "tool_use_id": id})
		cmd := exec.Command(g.Executable, "_cc-plan-hook", g.Path)
		cmd.Stdin = bytes.NewReader(data)
		if _, err := cmd.Output(); err != nil {
			t.Fatal(err)
		}
	}
	data, _ := json.Marshal(map[string]any{"hook_event_name": event, "tool_name": tool, "tool_input": input, "tool_response": response, "tool_use_id": id})
	cmd := exec.Command(g.Executable, "_cc-plan-hook", g.Path)
	cmd.Stdin = bytes.NewReader(data)
	out, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	return string(out)
}

func publish(t *testing.T, g *Guard, plan string) {
	t.Helper()
	result := callPlanTool(g.Path, "update_plan", json.RawMessage(plan))
	if result["isError"] != false {
		t.Fatal(result)
	}
}

func TestPlanningBarrier_DiscoveryDoesNotAuthorizeBusinessTools(t *testing.T) {
	g := newGuard(t)
	if got := runHook(t, g, "PreToolUse", "ToolSearch", map[string]any{"query": "select:TaskCreate,TaskUpdate"}, nil); strings.Contains(got, "deny") {
		t.Fatal(got)
	}
	for _, tool := range []string{"Bash", "Read", "Skill", "Agent", "exec_command", "MCP"} {
		if got := runHook(t, g, "PreToolUse", tool, nil, nil); !strings.Contains(got, `"permissionDecision":"deny"`) {
			t.Fatalf("%s started before plan: %s", tool, got)
		}
	}
	if !g.Blocked() {
		t.Fatal("blocked work was not reported")
	}
	if g.Progress() != nil {
		t.Fatal("discovery fabricated a plan")
	}
	// A failed task must not open the gate.
	runHook(t, g, "PostToolUse", "TaskCreate", map[string]any{"subject": "检查虚拟数据"}, map[string]any{"success": false})
	if got := runHook(t, g, "PreToolUse", "Bash", nil, nil); !strings.Contains(got, "deny") {
		t.Fatal(got)
	}
	for i, title := range []string{"核对虚拟利润数据", "编写日报"} {
		runHook(t, g, "PostToolUse", "TaskCreate", nil, map[string]any{"task": map[string]any{"id": string(rune('1' + i)), "subject": title, "status": "pending"}})
	}
	if got := runHook(t, g, "PreToolUse", "Bash", nil, nil); strings.Contains(got, "deny") || strings.Contains(got, `"allow"`) {
		t.Fatalf("plan barrier must only abstain, never bypass normal permissions: %s", got)
	}
	p := g.Progress()
	if p == nil || len(p.Steps) != 2 || p.Steps[0].Title != "核对虚拟利润数据" {
		t.Fatalf("wrong plan: %+v", p)
	}
	if g.Blocked() {
		t.Fatal("successful recovery stayed blocked")
	}
}

func TestPlanningBarrier_MCPWorksWithoutNativeTaskTools(t *testing.T) {
	g := newGuard(t)
	input := strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize"}` + "\n" + `{"jsonrpc":"2.0","id":2,"method":"tools/list"}` + "\n" + `{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"update_plan","arguments":{"plan":[{"step":"核对虚拟利润","status":"in_progress"},{"step":"生成日报","status":"pending"}]}}}` + "\n")
	cmd := exec.Command(g.Executable, "_cc-plan-mcp", g.Path)
	cmd.Stdin = input
	out, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(out), `"isError":true`) || !strings.Contains(string(out), `"name":"update_plan"`) {
		t.Fatal(string(out))
	}
	if got := runHook(t, g, "PreToolUse", "exec_command", nil, nil); strings.Contains(got, "deny") {
		t.Fatal(got)
	}
	progress := g.Progress()
	if progress == nil || progress.Source != "cc_connect_plan" || len(progress.Steps) != 2 {
		t.Fatal(progress)
	}
	// Completing the model-authored plan updates the card and cannot authorize
	// an extra unplanned business operation.
	publish(t, g, `{"plan":[{"step":"核对虚拟利润","status":"completed"},{"step":"生成日报","status":"completed"}]}`)
	if p := g.Progress(); p == nil || p.Steps[1].Status != "completed" {
		t.Fatal(p)
	}
	if got := runHook(t, g, "PreToolUse", "Bash", nil, nil); !strings.Contains(got, "deny") {
		t.Fatal(got)
	}
}

func TestPlanningBarrier_ResumedQueuedAndUnplannedTurnsReset(t *testing.T) {
	g := newGuard(t)
	publish(t, g, `{"plan":[{"step":"上一轮任务","status":"in_progress"}]}`)
	for _, request := range []string{"继续生成日报", "生成下一天利润表"} {
		if err := g.Begin(core.WithBusinessPlanInstructions(request)); err != nil {
			t.Fatal(err)
		}
		if g.Progress() != nil {
			t.Fatal("old plan leaked into new turn")
		}
		if got := runHook(t, g, "PreToolUse", "Bash", nil, nil); !strings.Contains(got, "deny") {
			t.Fatal("old plan authorized new work")
		}
		publish(t, g, `{"plan":[{"step":"本轮业务任务","status":"pending"}]}`)
	}
	if err := g.Begin(core.WithBusinessPlanInstructions("hello")); err != nil {
		t.Fatal(err)
	}
	if g.Progress() != nil || g.Blocked() {
		t.Fatal("plain turn retained planning state")
	}
	if err := g.Begin("/ps continue without a plan"); err != nil {
		t.Fatal(err)
	}
	if got := runHook(t, g, "PreToolUse", "Read", nil, nil); !strings.Contains(got, "deny") {
		t.Fatal("an unmarked supplement disabled the active planning barrier")
	}
}

func TestPlanningBarrier_ConcurrentTaskCreatesAreNotLost(t *testing.T) {
	g := newGuard(t)
	var wg sync.WaitGroup
	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			var out bytes.Buffer
			id := fmt.Sprintf("parallel-%d", i)
			pre, _ := json.Marshal(map[string]any{"hook_event_name": "PreToolUse", "tool_name": "TaskCreate", "tool_use_id": id})
			if err := RunHook(g.Path, bytes.NewReader(pre), &out); err != nil {
				t.Error(err)
			}
			data, _ := json.Marshal(map[string]any{"hook_event_name": "PostToolUse", "tool_name": "TaskCreate", "tool_use_id": id, "tool_response": map[string]any{"task": map[string]any{"id": string(rune('1' + i)), "subject": "业务步骤", "status": "pending"}}})
			if err := RunHook(g.Path, bytes.NewReader(data), &out); err != nil {
				t.Error(err)
			}
		}(i)
	}
	wg.Wait()
	p := g.Progress()
	if p == nil || len(p.Steps) != 6 {
		t.Fatalf("lost concurrent task results: %+v", p)
	}
}

func TestPlanningBarrier_MalformedPlanAndUnavailableStateStayClosed(t *testing.T) {
	g := newGuard(t)
	for _, args := range []string{`{"plan":[]}`, `{"plan":[{"step":"","status":"pending"}]}`, `{"plan":[{"step":"检查","status":"done"}]}`} {
		if result := callPlanTool(g.Path, "update_plan", json.RawMessage(args)); result["isError"] != true {
			t.Fatal(result)
		}
	}
	if result := callPlanTool(g.Path, "update_plan", json.RawMessage(`{"plan":[{"step":"还未执行的业务任务","status":"completed"}]}`)); result["isError"] != true {
		t.Fatal("a fresh turn accepted an already-completed plan")
	}
	if err := os.WriteFile(g.Path, []byte("broken state"), 0600); err != nil {
		t.Fatal(err)
	}
	if got := runHook(t, g, "PreToolUse", "Bash", nil, nil); !strings.Contains(got, "deny") {
		t.Fatal("corrupt guard failed open")
	}
}

func TestPlanningBarrier_UnsupportedCodexNeverStartsBusinessTurn(t *testing.T) {
	g := newGuard(t)
	bin := t.TempDir() + "/codex"
	if err := os.WriteFile(bin, []byte("#!/bin/sh\nexit 0\n"), 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := g.PrepareCodex(context.Background(), bin, nil, t.TempDir(), os.Environ()); err == nil {
		t.Fatal("unsupported CLI accepted an unenforced plan barrier")
	}
}

func TestPlanningBarrier_PreviousTurnResultsAndSubagentsCannotAuthorizeMainWork(t *testing.T) {
	g := newGuard(t)
	raw := func(event, tool, id, agent string, input, response any) string {
		data, _ := json.Marshal(map[string]any{"hook_event_name": event, "tool_name": tool, "tool_use_id": id, "agent_id": agent, "tool_input": input, "tool_response": response})
		var out bytes.Buffer
		if err := RunHook(g.Path, bytes.NewReader(data), &out); err != nil {
			t.Fatal(err)
		}
		return out.String()
	}
	raw("PreToolUse", "TaskCreate", "old-call", "", map[string]any{"subject": "上一轮任务"}, nil)
	if err := g.Begin(core.WithBusinessPlanInstructions("new request")); err != nil {
		t.Fatal(err)
	}
	raw("PostToolUse", "TaskCreate", "old-call", "", nil, map[string]any{"task": map[string]any{"id": "1", "subject": "上一轮任务", "status": "pending"}})
	if g.Progress() != nil {
		t.Fatal("late result published the previous plan")
	}
	if got := raw("PreToolUse", "TaskCreate", "child-call", "child", nil, nil); !strings.Contains(got, "deny") {
		t.Fatal("child opened the main gate")
	}
	publish(t, g, `{"plan":[{"step":"主会话业务任务","status":"pending"}]}`)
	if got := raw("PreToolUse", "mcp__cc_connect_plan__update_plan", "child-plan", "child", nil, nil); !strings.Contains(got, "deny") {
		t.Fatal("child may overwrite the main plan")
	}
}

func TestPlanningBarrier_DiscoveryOnlyAndCorruptStateReportPlanningStop(t *testing.T) {
	g := newGuard(t)
	runHook(t, g, "PreToolUse", "ToolSearch", nil, nil)
	if !g.Blocked() {
		t.Fatal("tool discovery without plan looked completed")
	}
	if err := os.WriteFile(g.Path, []byte("corrupt"), 0600); err != nil {
		t.Fatal(err)
	}
	if !g.Blocked() {
		t.Fatal("unavailable state looked completed")
	}
	if err := os.WriteFile(g.Path, []byte("null"), 0600); err != nil {
		t.Fatal(err)
	}
	if got := runHook(t, g, "PreToolUse", "Bash", nil, nil); !strings.Contains(got, "deny") {
		t.Fatal("null state disabled the barrier")
	}
}

func TestPlanningBarrier_MalformedNativeTaskGetCannotPublishOrCrash(t *testing.T) {
	g := newGuard(t)
	runHook(t, g, "PostToolUse", "TaskGet", map[string]any{"taskId": "1"}, map[string]any{"task": map[string]any{"id": map[string]any{"unexpected": "shape"}, "subject": "not a real task", "status": "pending"}})
	if g.Progress() != nil {
		t.Fatal("malformed task result published a plan")
	}
	if got := runHook(t, g, "PreToolUse", "Bash", nil, nil); !strings.Contains(got, "deny") {
		t.Fatal(got)
	}
}

func TestPlanningBarrier_ExplicitCLIHookPolicyIsNotOverridden(t *testing.T) {
	g := newGuard(t)
	for _, args := range [][]string{{"-c", "hooks.PreToolUse=[]"}, {"--config=hooks.PostToolUse=[]"}, {"--disable", "hooks"}, {"-c", "features.hooks=false"}} {
		if _, err := g.PrepareCodex(context.Background(), "nonexistent", args, t.TempDir(), nil); err == nil || !strings.Contains(err.Error(), "business planning") {
			t.Fatalf("explicit policy ignored: %v", err)
		}
	}
}

func TestPlanningBarrier_InstalledCodexHooksAreVerified(t *testing.T) {
	bin := os.Getenv("CC_CONNECT_TEST_CODEX_BINARY")
	if bin == "" {
		t.Skip("set CC_CONNECT_TEST_CODEX_BINARY for local no-model CLI compatibility check")
	}
	g := newGuard(t)
	env := core.MergeEnv(os.Environ(), []string{"CODEX_HOME=" + t.TempDir()})
	if _, err := g.PrepareCodex(context.Background(), bin, nil, t.TempDir(), env); err != nil {
		t.Fatal(err)
	}
}
