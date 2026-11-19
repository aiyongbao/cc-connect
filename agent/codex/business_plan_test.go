package codex

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/chenhg5/cc-connect/agent/planguard"
	"github.com/chenhg5/cc-connect/core"
)

func publishBusinessPlan(t *testing.T, g *planguard.Guard) {
	t.Helper()
	request := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"update_plan","arguments":{"plan":[{"step":"核对虚拟利润","status":"in_progress"},{"step":"生成日报","status":"pending"}]}}}` + "\n"
	var out bytes.Buffer
	if err := planguard.RunMCP(g.Path, strings.NewReader(request), &out); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), `"isError":true`) {
		t.Fatal(out.String())
	}
}

func TestCodexBusinessPlan_CodeModePublishesConfirmedPlanBeforeBusinessEvent(t *testing.T) {
	g, err := planguard.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(g.Close)
	if err = g.Begin(core.WithBusinessPlanInstructions("生成虚拟日报")); err != nil {
		t.Fatal(err)
	}
	cs, err := newCodexSession(context.Background(), "", nil, t.TempDir(), "", "", "", "", "", nil, "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	cs.planGuard = g
	t.Cleanup(func() { _ = cs.Close() })
	publishBusinessPlan(t, g)
	// Code mode may not emit native todo_list events for an MCP call. Its
	// confirmed plan must still reach the engine before the next tool event.
	cs.handleEvent(map[string]any{"type": "item.started", "item": map[string]any{"type": "command_execution", "command": "virtual-business-command"}})
	first := <-cs.Events()
	if first.Type != core.EventTaskProgress || first.TaskProgress.Source != "cc_connect_plan" || len(first.TaskProgress.Steps) != 2 || first.TaskProgress.Steps[0].Status != "in_progress" {
		t.Fatalf("lost confirmed code-mode plan: %+v", first)
	}
	second := <-cs.Events()
	if second.Type != core.EventToolUse {
		t.Fatalf("wrong event order: %+v", second)
	}
	cs.handleEvent(map[string]any{"type": "item.updated", "item": map[string]any{"type": "todo_list", "items": []any{map[string]any{"text": "核对虚拟利润", "completed": false}}}})
	if len(cs.events) != 0 {
		t.Fatal("lossy exec todo snapshot replaced the confirmed plan")
	}
}

func TestCodexBusinessPlan_ActualSendClearsPreviousTurnAuthorization(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX CLI fixture")
	}
	dir := t.TempDir()
	cli := filepath.Join(dir, "codex")
	script := "#!/bin/sh\ncat >/dev/null\nprintf '%s\\n' '{\"type\":\"turn.completed\"}'\n"
	if err := os.WriteFile(cli, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	g, err := planguard.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(g.Close)
	if err = g.Begin(core.WithBusinessPlanInstructions("previous task")); err != nil {
		t.Fatal(err)
	}
	publishBusinessPlan(t, g)
	cs, err := newCodexSession(context.Background(), cli, nil, dir, "", "", "", "", "", []string{"CODEX_HOME=" + t.TempDir()}, "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	cs.planGuard = g
	t.Cleanup(func() { _ = cs.Close() })
	if err = cs.Send(core.WithBusinessPlanInstructions("new daily report"), "new-turn", nil, nil); err != nil {
		t.Fatal(err)
	}
	select {
	case event := <-cs.Events():
		if event.Type != core.EventResult {
			t.Fatalf("previous plan leaked into new send: %+v", event)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("send did not finish")
	}
	if g.Progress() != nil {
		t.Fatal("previous turn remained authorized")
	}
}

func TestCodexBusinessPlan_UnsupportedCLIRejectedBeforeAnyExec(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX CLI fixture")
	}
	dir := t.TempDir()
	cli := filepath.Join(dir, "codex")
	if err := os.WriteFile(cli, []byte("#!/bin/sh\nexit 0\n"), 0700); err != nil {
		t.Fatal(err)
	}
	cs, err := newCodexSession(core.WithBusinessPlanning(context.Background()), cli, nil, dir, "", "", "", "", "", nil, "", "", "")
	if cs != nil || err == nil {
		t.Fatal("unsupported CLI accepted an unenforced business session")
	}
}

func TestCodexBusinessPlan_FullEventQueueDoesNotDropConfirmedPlan(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s := &appServerSession{ctx: ctx, events: make(chan core.Event, 1)}
	s.events <- core.Event{Type: core.EventThinking, Content: "previous event"}
	done := make(chan struct{})
	go func() {
		s.emit(core.Event{Type: core.EventTaskProgress, TaskProgress: &core.TaskProgress{Source: "test", Steps: []core.TaskProgressStep{{Title: "核对利润数据", Status: "pending"}}}})
		close(done)
	}()
	select {
	case <-done:
		t.Fatal("confirmed plan was dropped when the event queue was full")
	case <-time.After(50 * time.Millisecond):
	}
	<-s.events
	select {
	case event := <-s.events:
		if event.Type != core.EventTaskProgress || event.TaskProgress.Steps[0].Title != "核对利润数据" {
			t.Fatal(event)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("confirmed plan did not arrive after the queue drained")
	}
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("plan publisher did not finish")
	}
}
