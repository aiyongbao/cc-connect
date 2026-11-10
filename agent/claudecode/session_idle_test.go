package claudecode

import (
	"context"
	"testing"
	"time"

	"github.com/chenhg5/cc-connect/core"
)

func newIdleTestSession(t *testing.T) *claudeSession {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	return &claudeSession{events: make(chan core.Event, 16), ctx: ctx}
}

func nextEvent(t *testing.T, cs *claudeSession) core.Event {
	t.Helper()
	select {
	case evt := <-cs.events:
		return evt
	case <-time.After(time.Second):
		t.Fatal("expected an event")
		return core.Event{}
	}
}

func assertNoEvent(t *testing.T, cs *claudeSession) {
	t.Helper()
	select {
	case evt := <-cs.events:
		t.Fatalf("unexpected event %#v", evt)
	default:
	}
}

// Recorded from Claude Code 2.1.312: a plain turn reports idle right after its
// result. Nothing is held, so idle must not produce another result.
func TestClaudeSessionIdleAfterNormalTurnIsNoop(t *testing.T) {
	cs := newIdleTestSession(t)
	cs.handleReadLoopLine(`{"type":"system","subtype":"session_state_changed","state":"running"}`)
	cs.handleReadLoopLine(`{"type":"result","subtype":"success","num_turns":1,"result":"hi"}`)
	if evt := nextEvent(t, cs); evt.Type != core.EventResult || !evt.Done {
		t.Fatalf("result = %#v, want Done=true", evt)
	}
	cs.handleReadLoopLine(`{"type":"system","subtype":"session_state_changed","state":"idle"}`)
	assertNoEvent(t, cs)
}

// Recorded from Claude Code 2.1.312: Claude reports idle while a background
// shell is still running. The turn must keep waiting for the shell, then close
// on its task-notification result; neither idle may emit a backstop.
func TestClaudeSessionIdleWithRunningBackgroundShellKeepsWaiting(t *testing.T) {
	cs := newIdleTestSession(t)
	cs.handleReadLoopLine(`{"type":"system","subtype":"session_state_changed","state":"running"}`)
	cs.handleReadLoopLine(`{"type":"system","subtype":"background_tasks_changed","tasks":[{"task_id":"beoap4fjl","run_id":"run-bash","task_type":"local_bash"}]}`)
	cs.handleReadLoopLine(`{"type":"system","subtype":"task_started","task_id":"beoap4fjl","run_id":"run-bash","is_backgrounded":true,"task_type":"local_bash"}`)
	cs.handleReadLoopLine(`{"type":"result","subtype":"success","num_turns":2,"result":"started"}`)
	if evt := nextEvent(t, cs); evt.Type != core.EventResult || !evt.Done {
		t.Fatalf("main result = %#v, want Done=true", evt)
	}
	if !cs.HasPendingBackgroundTasks() {
		t.Fatal("running background shell should hold the turn")
	}
	cs.handleReadLoopLine(`{"type":"system","subtype":"session_state_changed","state":"idle"}`)
	assertNoEvent(t, cs)
	if !cs.HasPendingBackgroundTasks() {
		t.Fatal("idle must not release a turn while a background task is active")
	}

	cs.handleReadLoopLine(`{"type":"system","subtype":"task_notification","task_id":"beoap4fjl","run_id":"run-bash","status":"completed"}`)
	cs.handleReadLoopLine(`{"type":"system","subtype":"background_tasks_changed","tasks":[]}`)
	cs.handleReadLoopLine(`{"type":"system","subtype":"session_state_changed","state":"running"}`)
	cs.handleReadLoopLine(`{"type":"result","subtype":"success","origin":{"kind":"task-notification","runId":"run-bash"},"num_turns":1,"result":"bg-done"}`)
	if evt := nextEvent(t, cs); evt.Type != core.EventResult || !evt.Done || evt.Content != "bg-done" {
		t.Fatalf("task-notification result = %#v, want Done=true", evt)
	}
	cs.handleReadLoopLine(`{"type":"system","subtype":"session_state_changed","state":"idle"}`)
	assertNoEvent(t, cs)
}

// A permission prompt moves Claude to requires_action, not idle; the turn is
// untouched until its result arrives.
func TestClaudeSessionRequiresActionIsNotIdle(t *testing.T) {
	cs := newIdleTestSession(t)
	cs.handleReadLoopLine(`{"type":"system","subtype":"background_tasks_changed","tasks":[{"task_id":"t1","run_id":"r1"}]}`)
	cs.handleReadLoopLine(`{"type":"result","subtype":"success","num_turns":1,"result":"waiting"}`)
	nextEvent(t, cs)
	cs.handleReadLoopLine(`{"type":"system","subtype":"background_tasks_changed","tasks":[]}`)
	cs.handleReadLoopLine(`{"type":"system","subtype":"session_state_changed","state":"requires_action"}`)
	assertNoEvent(t, cs)
}

// The empty task-notification result is dropped without an event. If it was
// the last thing the held turn waited for, Claude's idle must close the turn
// with the held content instead of leaving it to the event idle timeout.
func TestClaudeSessionIdleBackstopClosesTurnAfterDroppedResult(t *testing.T) {
	cs := newIdleTestSession(t)
	cs.handleReadLoopLine(`{"type":"system","subtype":"background_tasks_changed","tasks":[{"task_id":"t1","run_id":"run-1"}]}`)
	cs.handleReadLoopLine(`{"type":"result","subtype":"success","num_turns":3,"result":"报告已生成"}`)
	if evt := nextEvent(t, cs); !evt.Done || !cs.HasPendingBackgroundTasks() {
		t.Fatalf("main result = %#v pending=%v, want held turn", evt, cs.HasPendingBackgroundTasks())
	}
	cs.handleReadLoopLine(`{"type":"system","subtype":"task_notification","task_id":"t1","run_id":"run-1","status":"stopped"}`)
	cs.handleReadLoopLine(`{"type":"system","subtype":"background_tasks_changed","tasks":[]}`)
	cs.handleReadLoopLine(`{"type":"result","subtype":"success","origin":{"kind":"task-notification","runId":"run-1"},"num_turns":0,"result":""}`)
	assertNoEvent(t, cs)

	cs.handleReadLoopLine(`{"type":"system","subtype":"session_state_changed","state":"idle"}`)
	evt := nextEvent(t, cs)
	if evt.Type != core.EventResult || !evt.Done || evt.Content != "报告已生成" {
		t.Fatalf("backstop = %#v, want Done=true with the held content", evt)
	}
	if backstop, _ := evt.Metadata[core.EventMetaTurnBackstop].(bool); !backstop {
		t.Fatalf("backstop metadata missing: %#v", evt.Metadata)
	}
	if cs.HasPendingBackgroundTasks() {
		t.Fatal("backstop must leave no pending background state")
	}
	// A second idle has nothing left to close.
	cs.handleReadLoopLine(`{"type":"system","subtype":"session_state_changed","state":"idle"}`)
	assertNoEvent(t, cs)
}

// A Done=false result held for still-running tasks is released by the next
// closing result, so the following idle stays silent.
func TestClaudeSessionClosingResultReleasesHeldTurn(t *testing.T) {
	cs := newIdleTestSession(t)
	cs.handleReadLoopLine(`{"type":"system","subtype":"background_tasks_changed","tasks":[{"task_id":"a","run_id":"ra"},{"task_id":"b","run_id":"rb"}]}`)
	cs.handleReadLoopLine(`{"type":"system","subtype":"task_notification","task_id":"a","run_id":"ra","status":"completed"}`)
	cs.handleReadLoopLine(`{"type":"system","subtype":"background_tasks_changed","tasks":[{"task_id":"b","run_id":"rb"}]}`)
	cs.handleReadLoopLine(`{"type":"result","subtype":"success","origin":{"kind":"task-notification","runId":"ra"},"num_turns":1,"result":"A done"}`)
	if evt := nextEvent(t, cs); evt.Done {
		t.Fatalf("first result = %#v, want Done=false while b runs", evt)
	}
	cs.handleReadLoopLine(`{"type":"system","subtype":"task_notification","task_id":"b","run_id":"rb","status":"completed"}`)
	cs.handleReadLoopLine(`{"type":"system","subtype":"background_tasks_changed","tasks":[]}`)
	cs.handleReadLoopLine(`{"type":"result","subtype":"success","origin":{"kind":"task-notification","runId":"rb"},"num_turns":1,"result":"all done"}`)
	if evt := nextEvent(t, cs); !evt.Done {
		t.Fatalf("last result = %#v, want Done=true", evt)
	}
	cs.handleReadLoopLine(`{"type":"system","subtype":"session_state_changed","state":"idle"}`)
	assertNoEvent(t, cs)
}

// Recorded from Claude Code 2.1.312 without --replay-user-messages: two async
// agents finish, their notifications and a user prompt sent meanwhile are
// merged into one turn, and a single task-notification result closes it. This
// is the production stall of session 86f49583; the result itself must close
// the turn and the trailing idle must stay silent.
func TestClaudeSessionMergedAgentsAndPromptRecordedSequence(t *testing.T) {
	cs := newIdleTestSession(t)
	for _, line := range []string{
		`{"type":"system","subtype":"session_state_changed","state":"running"}`,
		`{"type":"system","subtype":"background_tasks_changed","tasks":[{"task_id":"aa3cf7cb5d3616860","run_id":"0mwabd2a1","task_type":"local_agent"}]}`,
		`{"type":"system","subtype":"task_started","task_id":"aa3cf7cb5d3616860","run_id":"0mwabd2a1","is_backgrounded":true,"task_type":"local_agent"}`,
		`{"type":"system","subtype":"background_tasks_changed","tasks":[{"task_id":"aa3cf7cb5d3616860","run_id":"0mwabd2a1","task_type":"local_agent"},{"task_id":"a39132ab6d6cc2ecc","run_id":"0mwabd2a2","task_type":"local_agent"}]}`,
		`{"type":"system","subtype":"task_started","task_id":"a39132ab6d6cc2ecc","run_id":"0mwabd2a2","is_backgrounded":true,"task_type":"local_agent"}`,
		`{"type":"result","subtype":"success","num_turns":3,"result":"launched"}`,
	} {
		cs.handleReadLoopLine(line)
	}
	if evt := nextEvent(t, cs); !evt.Done || !cs.HasPendingBackgroundTasks() {
		t.Fatalf("launch result = %#v, want Done=true held by running agents", evt)
	}
	for _, line := range []string{
		`{"type":"system","subtype":"task_started","task_id":"b4h970y5q","run_id":"0mwabd2b1","owned_by_subagent":true,"is_backgrounded":false,"parent_task_id":"aa3cf7cb5d3616860","task_type":"local_bash"}`,
		`{"type":"system","subtype":"task_started","task_id":"bqtx8a0bz","run_id":"0mwabd2b2","owned_by_subagent":true,"is_backgrounded":false,"parent_task_id":"a39132ab6d6cc2ecc","task_type":"local_bash"}`,
		`{"type":"system","subtype":"task_notification","task_id":"b4h970y5q","run_id":"0mwabd2b1","status":"completed"}`,
		`{"type":"system","subtype":"task_notification","task_id":"bqtx8a0bz","run_id":"0mwabd2b2","status":"completed"}`,
		`{"type":"system","subtype":"task_notification","task_id":"aa3cf7cb5d3616860","run_id":"0mwabd2a1","status":"completed"}`,
		`{"type":"system","subtype":"background_tasks_changed","tasks":[{"task_id":"a39132ab6d6cc2ecc","run_id":"0mwabd2a2","task_type":"local_agent"}]}`,
		`{"type":"system","subtype":"task_notification","task_id":"a39132ab6d6cc2ecc","run_id":"0mwabd2a2","status":"completed"}`,
		`{"type":"system","subtype":"background_tasks_changed","tasks":[]}`,
		// Foreground bash in the merged turn (not backgrounded, not subagent-owned).
		`{"type":"system","subtype":"task_started","task_id":"b79wbwu9v","run_id":"0mwabd2c1","is_backgrounded":false,"task_type":"local_bash"}`,
		`{"type":"system","subtype":"task_notification","task_id":"b79wbwu9v","run_id":"0mwabd2c1","status":"completed"}`,
		`{"type":"result","subtype":"success","origin":{"kind":"task-notification","runId":"0mwabd2a1"},"num_turns":2,"result":"2+2 is 4. Both agents finished."}`,
	} {
		cs.handleReadLoopLine(line)
	}
	// Agent B's run never gets its own result, but that is only known once
	// Claude reports idle; until then the turn stays open.
	if evt := nextEvent(t, cs); evt.Type != core.EventResult || evt.Done {
		t.Fatalf("merged result = %#v, want Done=false until Claude is idle", evt)
	}
	cs.handleReadLoopLine(`{"type":"system","subtype":"session_state_changed","state":"idle"}`)
	evt := nextEvent(t, cs)
	if evt.Type != core.EventResult || !evt.Done || evt.Content != "2+2 is 4. Both agents finished." {
		t.Fatalf("idle backstop = %#v, want Done=true with the merged reply", evt)
	}
	if cs.HasPendingBackgroundTasks() {
		t.Fatal("merged turn must leave no pending background state")
	}
}

// Recorded from the local cc-connect test (Claude Code 2.1.312): agents A and B
// finish together, but B's notification is queued behind A's turn instead of
// merged. A's result must not close the turn; B's own result does. Claude
// does not report idle between queued turns.
func TestClaudeSessionQueuedNotificationKeepsTurnOpenUntilItsResult(t *testing.T) {
	cs := newIdleTestSession(t)
	for _, line := range []string{
		`{"type":"system","subtype":"session_state_changed","state":"running"}`,
		`{"type":"system","subtype":"background_tasks_changed","tasks":[{"task_id":"a663","run_id":"b-run1","task_type":"local_agent"},{"task_id":"a2139","run_id":"a-run1","task_type":"local_agent"}]}`,
		`{"type":"result","subtype":"success","num_turns":2,"result":"已启动"}`,
	} {
		cs.handleReadLoopLine(line)
	}
	if evt := nextEvent(t, cs); !evt.Done || !cs.HasPendingBackgroundTasks() {
		t.Fatalf("launch result = %#v, want Done=true held by running agents", evt)
	}
	for _, line := range []string{
		// B's first run hands back but leaves its own background shell running.
		`{"type":"system","subtype":"task_started","task_id":"bzq7w3t7y","run_id":"b-shell","owned_by_subagent":true,"is_backgrounded":true,"parent_task_id":"a663","task_type":"local_bash"}`,
		`{"type":"system","subtype":"task_notification","task_id":"a663","run_id":"b-run1","status":"completed"}`,
		`{"type":"system","subtype":"background_tasks_changed","tasks":[{"task_id":"a2139","run_id":"a-run1"},{"task_id":"bzq7w3t7y","run_id":"b-shell","parent_task_id":"a663"}]}`,
		`{"type":"result","subtype":"success","origin":{"kind":"task-notification","runId":"b-run1"},"num_turns":1,"result":"B 还在后台运行"}`,
	} {
		cs.handleReadLoopLine(line)
	}
	if evt := nextEvent(t, cs); evt.Done {
		t.Fatalf("B hand-back result = %#v, want Done=false", evt)
	}
	for _, line := range []string{
		// The shell finishes; Claude wakes B again under a new run ID.
		`{"type":"system","subtype":"task_notification","task_id":"bzq7w3t7y","run_id":"b-shell","status":"completed"}`,
		`{"type":"system","subtype":"background_tasks_changed","tasks":[{"task_id":"a2139","run_id":"a-run1"}]}`,
		`{"type":"system","subtype":"background_tasks_changed","tasks":[{"task_id":"a2139","run_id":"a-run1"},{"task_id":"a663","run_id":"b-run2"}]}`,
		`{"type":"system","subtype":"task_started","task_id":"a663","run_id":"b-run2","is_backgrounded":true,"task_type":"local_agent"}`,
		`{"type":"system","subtype":"task_notification","task_id":"a2139","run_id":"a-run1","status":"completed"}`,
		`{"type":"system","subtype":"task_notification","task_id":"a663","run_id":"b-run2","status":"completed"}`,
		`{"type":"system","subtype":"background_tasks_changed","tasks":[]}`,
		`{"type":"result","subtype":"success","origin":{"kind":"task-notification","runId":"a-run1"},"num_turns":1,"result":"A 已完成，B 仍未确认"}`,
	} {
		cs.handleReadLoopLine(line)
	}
	if evt := nextEvent(t, cs); evt.Done {
		t.Fatalf("A result = %#v, want Done=false while B's notification is queued", evt)
	}
	cs.handleReadLoopLine(`{"type":"result","subtype":"success","origin":{"kind":"task-notification","runId":"b-run2"},"num_turns":1,"result":"两个子代理都已完成"}`)
	if evt := nextEvent(t, cs); !evt.Done || evt.Content != "两个子代理都已完成" {
		t.Fatalf("B result = %#v, want Done=true with the final summary", evt)
	}
	cs.handleReadLoopLine(`{"type":"system","subtype":"session_state_changed","state":"idle"}`)
	assertNoEvent(t, cs)
}

// A background task a subagent leaves running keeps the turn open (Claude wakes
// the subagent again when it ends), while its own run ID never blocks a result.
func TestClaudeSessionSubagentBackgroundTaskStaysActive(t *testing.T) {
	cs := newIdleTestSession(t)
	cs.handleReadLoopLine(`{"type":"system","subtype":"session_state_changed","state":"running"}`)
	cs.handleReadLoopLine(`{"type":"system","subtype":"task_started","task_id":"agent-1","run_id":"run-a","is_backgrounded":true,"task_type":"local_agent"}`)
	cs.handleReadLoopLine(`{"type":"system","subtype":"task_started","task_id":"shell-1","run_id":"run-s","owned_by_subagent":true,"is_backgrounded":true,"parent_task_id":"agent-1","task_type":"local_bash"}`)
	cs.handleReadLoopLine(`{"type":"system","subtype":"task_notification","task_id":"agent-1","run_id":"run-a","status":"completed"}`)
	cs.handleReadLoopLine(`{"type":"result","subtype":"success","origin":{"kind":"task-notification","runId":"run-a"},"num_turns":1,"result":"agent handed back"}`)
	if evt := nextEvent(t, cs); evt.Done {
		t.Fatalf("result = %#v, want Done=false while the subagent's shell runs", evt)
	}
	cs.handleReadLoopLine(`{"type":"system","subtype":"session_state_changed","state":"idle"}`)
	assertNoEvent(t, cs)
	cs.handleReadLoopLine(`{"type":"system","subtype":"task_notification","task_id":"shell-1","run_id":"run-s","status":"completed"}`)
	cs.handleReadLoopLine(`{"type":"system","subtype":"task_started","task_id":"agent-1","run_id":"run-a2","is_backgrounded":true,"task_type":"local_agent"}`)
	cs.handleReadLoopLine(`{"type":"system","subtype":"task_notification","task_id":"agent-1","run_id":"run-a2","status":"completed"}`)
	cs.handleReadLoopLine(`{"type":"result","subtype":"success","origin":{"kind":"task-notification","runId":"run-a2"},"num_turns":1,"result":"agent done"}`)
	if evt := nextEvent(t, cs); !evt.Done {
		t.Fatalf("final result = %#v, want Done=true", evt)
	}
	if cs.HasPendingBackgroundTasks() {
		t.Fatal("no background state may remain")
	}
}

// A foreground task's notification never gets a result of its own, so it must
// not hold a normal turn even when session state events are enabled.
func TestClaudeSessionForegroundTaskNotificationDoesNotHoldTurn(t *testing.T) {
	cs := newIdleTestSession(t)
	cs.handleReadLoopLine(`{"type":"system","subtype":"session_state_changed","state":"running"}`)
	cs.handleReadLoopLine(`{"type":"system","subtype":"task_started","task_id":"fg-1","run_id":"run-fg","is_backgrounded":false,"task_type":"local_bash"}`)
	cs.handleReadLoopLine(`{"type":"system","subtype":"task_notification","task_id":"fg-1","run_id":"run-fg","status":"completed"}`)
	cs.handleReadLoopLine(`{"type":"result","subtype":"success","num_turns":2,"result":"ran it"}`)
	if evt := nextEvent(t, cs); !evt.Done || cs.HasPendingBackgroundTasks() {
		t.Fatalf("result = %#v pending=%v, want closed turn", evt, cs.HasPendingBackgroundTasks())
	}
	cs.handleReadLoopLine(`{"type":"system","subtype":"session_state_changed","state":"idle"}`)
	assertNoEvent(t, cs)
}

// Session state changes follow every turn. They must not reach the engine as
// events, or the unsolicited reader would log background activity after each
// turn; the session ID they carry is still recorded.
func TestClaudeSessionStateEventsEmitNoEngineEvent(t *testing.T) {
	cs := newIdleTestSession(t)
	cs.handleReadLoopLine(`{"type":"system","subtype":"session_state_changed","state":"running","session_id":"sid-1"}`)
	cs.handleReadLoopLine(`{"type":"system","subtype":"session_state_changed","state":"requires_action","session_id":"sid-1"}`)
	cs.handleReadLoopLine(`{"type":"system","subtype":"session_state_changed","state":"idle","session_id":"sid-1"}`)
	assertNoEvent(t, cs)
	if got := cs.CurrentSessionID(); got != "sid-1" {
		t.Fatalf("session id = %q, want sid-1", got)
	}
}
