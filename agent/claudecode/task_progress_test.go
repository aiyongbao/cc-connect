package claudecode

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/chenhg5/cc-connect/core"
)

func taskTestSession(t *testing.T) *claudeSession {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	return &claudeSession{ctx: ctx, events: make(chan core.Event, 64)}
}

func sendTaskUse(t *testing.T, cs *claudeSession, callID, name, input string) {
	t.Helper()
	cs.handleReadLoopLine(`{"type":"assistant","message":{"content":[{"type":"tool_use","id":"` + callID + `","name":"` + name + `","input":` + input + `}]}}`)
}

func sendTaskResult(t *testing.T, cs *claudeSession, callID, output string, failed bool) {
	t.Helper()
	var data any
	if err := json.Unmarshal([]byte(output), &data); err != nil {
		t.Fatal(err)
	}
	raw := map[string]any{
		"type": "user", "tool_use_result": data,
		"message": map[string]any{"content": []any{map[string]any{
			"type": "tool_result", "tool_use_id": callID, "is_error": failed, "content": "tool response",
		}}},
	}
	cs.handleUser(raw)
}

func drainTaskProgress(cs *claudeSession) []*core.TaskProgress {
	var updates []*core.TaskProgress
	for len(cs.events) > 0 {
		event := <-cs.events
		if event.Type == core.EventTaskProgress {
			updates = append(updates, event.TaskProgress)
		}
	}
	return updates
}

func requireTaskSnapshot(t *testing.T, cs *claudeSession, want ...core.TaskProgressStep) {
	t.Helper()
	updates := drainTaskProgress(cs)
	if len(updates) != 1 {
		t.Fatalf("progress updates = %+v, want one", updates)
	}
	got := updates[0]
	if got.Source != "claude_tasks" || len(got.Steps) != len(want) {
		t.Fatalf("snapshot = %+v, want %+v", got, want)
	}
	for i := range want {
		if got.Steps[i] != want[i] {
			t.Fatalf("step %d = %+v, want %+v", i, got.Steps[i], want[i])
		}
	}
}

func TestClaudeTaskProgress_CreateUpdateDeleteAcrossTurns(t *testing.T) {
	cs := taskTestSession(t)
	sendTaskUse(t, cs, "create", "TaskCreate", `{"subject":"Inspect"}`)
	if got := drainTaskProgress(cs); len(got) != 0 {
		t.Fatal("task was published before successful creation")
	}
	sendTaskResult(t, cs, "create", `{"task":{"id":"1","subject":"Inspect"}}`, false)
	requireTaskSnapshot(t, cs, core.TaskProgressStep{Title: "Inspect", Status: "pending"})

	sendTaskUse(t, cs, "start", "TaskUpdate", `{"taskId":"1","status":"in_progress"}`)
	sendTaskResult(t, cs, "start", `{"success":true,"taskId":"1"}`, false)
	requireTaskSnapshot(t, cs, core.TaskProgressStep{Title: "Inspect", Status: "in_progress"})
	// A foreground turn ending must not complete or discard the task registry.
	cs.handleReadLoopLine(`{"type":"result","subtype":"success","result":"Continuing next turn","session_id":"session","num_turns":1}`)
	if got := drainTaskProgress(cs); len(got) != 0 {
		t.Fatal("turn completion fabricated task progress")
	}
	sendTaskUse(t, cs, "finish", "TaskUpdate", `{"taskId":"1","subject":"Inspect source","status":"completed"}`)
	sendTaskResult(t, cs, "finish", `{"success":true}`, false)
	requireTaskSnapshot(t, cs, core.TaskProgressStep{Title: "Inspect source", Status: "completed"})
	sendTaskUse(t, cs, "delete", "TaskUpdate", `{"taskId":"1","status":"deleted"}`)
	sendTaskResult(t, cs, "delete", `{"success":true}`, false)
	requireTaskSnapshot(t, cs)
}

func TestClaudeTaskProgress_ParallelResultsArePairedByCallID(t *testing.T) {
	cs := taskTestSession(t)
	sendTaskUse(t, cs, "a", "TaskCreate", `{"subject":"First"}`)
	sendTaskUse(t, cs, "b", "TaskCreate", `{"subject":"Second"}`)
	drainTaskProgress(cs)
	sendTaskResult(t, cs, "b", `{"task":{"id":"22"}}`, false)
	requireTaskSnapshot(t, cs, core.TaskProgressStep{Title: "Second", Status: "pending"})
	sendTaskResult(t, cs, "a", `{"task":{"id":"11"}}`, false)
	requireTaskSnapshot(t, cs,
		core.TaskProgressStep{Title: "Second", Status: "pending"},
		core.TaskProgressStep{Title: "First", Status: "pending"})
	sendTaskUse(t, cs, "ua", "TaskUpdate", `{"taskId":"11","status":"completed"}`)
	sendTaskUse(t, cs, "ub", "TaskUpdate", `{"taskId":"22","status":"in_progress"}`)
	sendTaskResult(t, cs, "ub", `{"success":true}`, false)
	sendTaskResult(t, cs, "ua", `{"success":true}`, false)
	updates := drainTaskProgress(cs)
	if len(updates) != 2 || updates[1].Steps[0].Status != "in_progress" || updates[1].Steps[1].Status != "completed" {
		t.Fatalf("parallel updates = %+v", updates)
	}
}

func TestClaudeTaskProgress_FailedUnknownAndChildTasksCannotAdvancePlan(t *testing.T) {
	cs := taskTestSession(t)
	sendTaskUse(t, cs, "restore", "TaskList", `{}`)
	sendTaskResult(t, cs, "restore", `{"tasks":[{"id":"1","subject":"Keep pending","status":"pending"}]}`, false)
	drainTaskProgress(cs)
	for i, tc := range []struct {
		input, output string
		failed        bool
	}{
		{`{"taskId":"1","status":"completed"}`, `{"success":false}`, false},
		{`{"taskId":"1","status":"completed"}`, `{"success":true}`, true},
		{`{"taskId":"missing","status":"completed"}`, `{"success":true}`, false},
		{`{"taskId":"1","status":"invented"}`, `{"success":true}`, false},
		{`{"taskId":"1","status":"completed"}`, `{"success":true,"taskId":"wrong"}`, false},
		{`{"taskId":"1","status":"completed"}`, `{}`, false},
	} {
		id := string(rune('a' + i))
		sendTaskUse(t, cs, id, "TaskUpdate", tc.input)
		sendTaskResult(t, cs, id, tc.output, tc.failed)
		if got := drainTaskProgress(cs); len(got) != 0 {
			t.Fatalf("invalid/failed update published: %+v", got)
		}
	}
	cs.handleReadLoopLine(`{"type":"assistant","parent_tool_use_id":"subagent","message":{"content":[{"type":"tool_use","id":"child","name":"TaskCreate","input":{"subject":"Private child step"}}]}}`)
	sendTaskResult(t, cs, "child", `{"task":{"id":"1","subject":"Private child step"}}`, false)
	if got := drainTaskProgress(cs); len(got) != 0 {
		t.Fatal("child task polluted parent progress")
	}
	sendTaskUse(t, cs, "check", "TaskUpdate", `{"taskId":"1","owner":"worker"}`)
	sendTaskResult(t, cs, "check", `{"success":true}`, false)
	requireTaskSnapshot(t, cs, core.TaskProgressStep{Title: "Keep pending", Status: "pending"})
}

func TestClaudeTaskProgress_RestoreAndReplaceFromTaskReads(t *testing.T) {
	cs := taskTestSession(t)
	sendTaskUse(t, cs, "get", "TaskGet", `{"taskId":"9"}`)
	sendTaskResult(t, cs, "get", `{"task":{"id":"9","subject":"Restored","status":"in_progress"}}`, false)
	requireTaskSnapshot(t, cs, core.TaskProgressStep{Title: "Restored", Status: "in_progress"})
	sendTaskUse(t, cs, "list", "TaskList", `{}`)
	sendTaskResult(t, cs, "list", `{"tasks":[{"id":"2","subject":"Full snapshot","status":"completed"}]}`, false)
	requireTaskSnapshot(t, cs, core.TaskProgressStep{Title: "Full snapshot", Status: "completed"})
	sendTaskUse(t, cs, "invalid", "TaskList", `{}`)
	sendTaskResult(t, cs, "invalid", `{"tasks":[{"id":"3","subject":"Invalid","status":"unknown"}]}`, false)
	if got := drainTaskProgress(cs); len(got) != 0 {
		t.Fatal("invalid list replaced tasks")
	}
	sendTaskUse(t, cs, "clear", "TaskList", `{}`)
	sendTaskResult(t, cs, "clear", `{"tasks":[]}`, false)
	requireTaskSnapshot(t, cs)
}

func TestClaudeTaskProgress_TextCreateFallbackAndUntruncatedOutput(t *testing.T) {
	cs := taskTestSession(t)
	sendTaskUse(t, cs, "text", "TaskCreate", `{"subject":"Old CLI"}`)
	cs.handleReadLoopLine(`{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"text","content":"Task #7 created successfully: Old CLI"}]}}`)
	requireTaskSnapshot(t, cs, core.TaskProgressStep{Title: "Old CLI", Status: "pending"})
	sendTaskUse(t, cs, "missing", "TaskUpdate", `{"taskId":"7","status":"completed"}`)
	cs.handleReadLoopLine(`{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"missing","content":"Task not found"}]}}`)
	if got := drainTaskProgress(cs); len(got) != 0 {
		t.Fatal("unacknowledged update advanced a task")
	}
	sendTaskUse(t, cs, "text-update", "TaskUpdate", `{"taskId":"7","status":"completed"}`)
	cs.handleReadLoopLine(`{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"text-update","content":"Updated task #7 status"}]}}`)
	requireTaskSnapshot(t, cs, core.TaskProgressStep{Title: "Old CLI", Status: "completed"})
	sendTaskUse(t, cs, "json", "TaskList", `{}`)
	content := `{"padding":"` + strings.Repeat("x", 600) + `","tasks":[{"id":"8","subject":"Beyond preview limit","status":"completed"}]}`
	cs.handleUser(map[string]any{"message": map[string]any{"content": []any{map[string]any{
		"type": "tool_result", "tool_use_id": "json", "content": content,
	}}}})
	requireTaskSnapshot(t, cs, core.TaskProgressStep{Title: "Beyond preview limit", Status: "completed"})
}

func TestClaudeTaskProgress_BatchedResultsDoNotReuseMessageOutput(t *testing.T) {
	cs := taskTestSession(t)
	sendTaskUse(t, cs, "a", "TaskCreate", `{"subject":"A"}`)
	sendTaskUse(t, cs, "b", "TaskCreate", `{"subject":"B"}`)
	cs.handleReadLoopLine(`{"type":"user","tool_use_result":{"task":{"id":"wrong"}},"message":{"content":[{"type":"tool_result","tool_use_id":"b","content":"Task #2 created successfully: B"},{"type":"tool_result","tool_use_id":"a","content":"Task #1 created successfully: A"}]}}`)
	updates := drainTaskProgress(cs)
	if len(updates) != 2 || len(updates[1].Steps) != 2 || updates[1].Steps[0].Title != "B" || updates[1].Steps[1].Title != "A" {
		t.Fatalf("batch results mixed up task identities: %+v", updates)
	}
}

func TestClaudeTaskProgress_FailedCreateAndNewSessionsAreIsolated(t *testing.T) {
	cs := taskTestSession(t)
	sendTaskUse(t, cs, "failed", "TaskCreate", `{"subject":"Never created"}`)
	sendTaskResult(t, cs, "failed", `{"task":{"id":"1"}}`, true)
	if got := drainTaskProgress(cs); len(got) != 0 {
		t.Fatal("failed create published a task")
	}
	sendTaskUse(t, cs, "ok", "TaskCreate", `{"subject":"Original session"}`)
	sendTaskResult(t, cs, "ok", `{"task":{"id":"1"}}`, false)
	requireTaskSnapshot(t, cs, core.TaskProgressStep{Title: "Original session", Status: "pending"})
	other := taskTestSession(t)
	sendTaskUse(t, other, "update", "TaskUpdate", `{"taskId":"1","status":"completed"}`)
	sendTaskResult(t, other, "update", `{"success":true}`, false)
	if got := drainTaskProgress(other); len(got) != 0 {
		t.Fatal("another session inherited the task registry")
	}
}

func TestClaudeTaskProgress_ChildTodoEventsKeepNestingAndCallIDs(t *testing.T) {
	cs := taskTestSession(t)
	cs.handleReadLoopLine(`{"type":"assistant","parent_tool_use_id":"agent","message":{"content":[{"type":"tool_use","id":"child-todo","name":"TodoWrite","input":{"todos":[]}}]}}`)
	cs.handleReadLoopLine(`{"type":"user","parent_tool_use_id":"agent","message":{"content":[{"type":"tool_result","tool_use_id":"child-todo","content":"success"}]}}`)
	for _, wantType := range []core.EventType{core.EventToolUse, core.EventToolResult} {
		event := <-cs.events
		if event.Type != wantType || event.Metadata["parent_tool_use_id"] != "agent" || event.Metadata["tool_use_id"] != "child-todo" {
			t.Fatalf("child tool lost correlation or nesting: %+v", event)
		}
	}
	if len(cs.events) != 0 {
		t.Fatal("child todo published parent task progress")
	}
}
