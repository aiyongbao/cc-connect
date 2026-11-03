package claudecode

import (
	"encoding/json"
	"log/slog"
	"regexp"
	"strings"

	"github.com/chenhg5/cc-connect/core"
)

// Claude's task tools patch individual tasks, whereas the shared progress
// contract publishes a complete ordered snapshot. Only the stream reader
// accesses this tracker; neither Send nor card writers mutate it.
type claudeTaskTracker struct {
	pending map[string]claudeTaskCall
	tasks   map[string]core.TaskProgressStep
	order   []string
}

type claudeTaskCall struct {
	name  string
	input map[string]any
}

func (t *claudeTaskTracker) track(block map[string]any) {
	name, _ := block["name"].(string)
	switch name {
	case "TaskCreate", "TaskUpdate", "TaskList", "TaskGet":
	default:
		return
	}
	id, _ := block["id"].(string)
	input, ok := block["input"].(map[string]any)
	if id == "" || !ok {
		return
	}
	if t.pending == nil {
		t.pending = make(map[string]claudeTaskCall)
	}
	t.pending[id] = claudeTaskCall{name: name, input: input}
}

// TaskCreate's ID comes from tool_use_result.task.id, not from the input or
// the tool call ID. Pair by tool_use_id even when results arrive out of order.
func (t *claudeTaskTracker) result(block map[string]any, output any, text string, failed bool) (string, *core.TaskProgress) {
	id, _ := block["tool_use_id"].(string)
	call, ok := t.pending[id]
	if !ok {
		return "", nil
	}
	delete(t.pending, id)
	data, _ := output.(map[string]any)
	if data == nil {
		// Some transports carry structured JSON in the content instead.
		_ = json.Unmarshal([]byte(text), &data)
	}
	success, hasSuccess := data["success"].(bool)
	if failed || (hasSuccess && !success) || taskString(data, "error") != "" {
		return call.name, nil
	}
	if !t.apply(call, data, text) {
		slog.Debug("claudeSession: task progress unchanged or unavailable", "tool", call.name)
		return call.name, nil
	}
	steps := make([]core.TaskProgressStep, 0, len(t.order))
	for _, taskID := range t.order {
		steps = append(steps, t.tasks[taskID])
	}
	return call.name, &core.TaskProgress{Source: "claude_tasks", Steps: steps}
}

var createdTaskText = regexp.MustCompile(`^Task #([^\s:]+) created successfully:`)
var updatedTaskText = regexp.MustCompile(`^Updated task #([^\s:]+)(?:\s|$)`)

func (t *claudeTaskTracker) apply(call claudeTaskCall, data map[string]any, text string) bool {
	switch call.name {
	case "TaskCreate":
		row, _ := data["task"].(map[string]any)
		id := taskString(row, "id")
		if id == "" {
			// Older stream-json versions only expose this exact confirmation.
			if match := createdTaskText.FindStringSubmatch(strings.TrimSpace(text)); match != nil {
				id = match[1]
			}
		}
		title := taskString(row, "subject")
		if title == "" {
			title = taskString(call.input, "subject")
		}
		if id == "" || title == "" {
			return false
		}
		status := taskString(row, "status")
		if status == "" {
			status = "pending"
		}
		return t.put(id, title, status)
	case "TaskUpdate":
		if data != nil {
			if success, _ := data["success"].(bool); !success {
				return false
			}
		} else {
			match := updatedTaskText.FindStringSubmatch(strings.TrimSpace(text))
			if match == nil || match[1] != taskString(call.input, "taskId") {
				return false
			}
		}
		return t.update(call.input, data)
	case "TaskGet":
		row, _ := data["task"].(map[string]any)
		id := taskString(row, "id")
		if id != taskString(call.input, "taskId") {
			return false
		}
		return t.put(id, taskString(row, "subject"), taskString(row, "status"))
	case "TaskList":
		rows, ok := data["tasks"].([]any)
		if !ok {
			return false
		}
		// Validate the whole snapshot before replacing existing state.
		next := claudeTaskTracker{}
		for _, raw := range rows {
			row, _ := raw.(map[string]any)
			id := taskString(row, "id")
			if _, duplicate := next.tasks[id]; duplicate || !next.put(id, taskString(row, "subject"), taskString(row, "status")) {
				return false
			}
		}
		t.tasks, t.order = next.tasks, next.order
		return true
	}
	return false
}

func (t *claudeTaskTracker) update(input, data map[string]any) bool {
	id := taskString(input, "taskId")
	if resultID := taskString(data, "taskId"); resultID != "" && resultID != id {
		return false
	}
	step, exists := t.tasks[id]
	if !exists {
		// Never invent the subject or initial status of an unseen task.
		return false
	}
	status := taskString(input, "status")
	if status == "deleted" {
		delete(t.tasks, id)
		for i, taskID := range t.order {
			if taskID == id {
				t.order = append(t.order[:i], t.order[i+1:]...)
				break
			}
		}
		return true
	}
	if title := taskString(input, "subject"); title != "" {
		step.Title = title
	}
	if status != "" {
		step.Status = status
	}
	return t.put(id, step.Title, step.Status)
}

func (t *claudeTaskTracker) put(id, title, status string) bool {
	if id == "" || title == "" || (status != "pending" && status != "in_progress" && status != "completed") {
		return false
	}
	if t.tasks == nil {
		t.tasks = make(map[string]core.TaskProgressStep)
	}
	if _, exists := t.tasks[id]; !exists {
		t.order = append(t.order, id)
	}
	t.tasks[id] = core.TaskProgressStep{Title: title, Status: status}
	return true
}

func taskString(row map[string]any, key string) string {
	value, _ := row[key].(string)
	return strings.TrimSpace(value)
}

// Keep nesting and correlation on ordinary tool events too: legacy TodoWrite
// progress is parsed by core, after it has left this adapter.
func claudeToolMetadata(message, block map[string]any, idKey string) map[string]any {
	return map[string]any{
		"tool_use_id":        taskString(block, idKey),
		"parent_tool_use_id": taskString(message, "parent_tool_use_id"),
	}
}
