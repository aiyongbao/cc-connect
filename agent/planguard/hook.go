package planguard

import (
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"strings"

	"github.com/chenhg5/cc-connect/core"
)

const blockedReason = "Business work is paused until you publish a plan. Call TaskCreate/TaskUpdate, TodoWrite, update_plan, or the cc_connect_plan MCP update_plan tool successfully first. ToolSearch only discovers tools; it does not create a plan. Do not bypass this barrier through another tool or a subagent."

type hookInput struct {
	Event    string         `json:"hook_event_name"`
	Tool     string         `json:"tool_name"`
	Input    map[string]any `json:"tool_input"`
	Response any            `json:"tool_response"`
	AgentID  string         `json:"agent_id"`
	CallID   string         `json:"tool_use_id"`
}

// RunHook never approves a tool. Success only means no planning objection;
// existing allow/deny rules and all other hooks still decide normal permissions.
func RunHook(path string, input io.Reader, output io.Writer) error {
	var in hookInput
	if err := json.NewDecoder(io.LimitReader(input, 4<<20)).Decode(&in); err != nil {
		return deny(output, "Planning barrier could not read the tool request; work has not started.")
	}
	var reason string
	err := withState(path, func(s *state) error {
		if !s.Enabled {
			return nil
		}
		name := planningTool(in.Tool)
		if in.Event == "PreToolUse" && in.AgentID != "" && ownPlanningTool(in.Tool) {
			reason = "Only the main session may publish the user's business plan. Report your progress to the main session."
			return nil
		}
		if in.Event == "PostToolUse" {
			if in.AgentID == "" {
				if call, ok := s.Pending[in.CallID]; ok && call.Name == name {
					delete(s.Pending, in.CallID)
					if !ownPlanningTool(in.Tool) {
						// PostToolUse carries the final input after any existing
						// user hook rewrites; do not publish the pre-hook draft.
						recordSuccessfulTool(s, name, in.Input, in.Response)
					}
				}
			}
			return nil
		}
		if in.Event != "PreToolUse" {
			return fmt.Errorf("unknown planning hook event")
		}
		s.Attempted = true
		switch name {
		case "ToolSearch", "TaskCreate", "TaskUpdate", "TaskList", "TaskGet", "TodoWrite", "update_plan":
			if in.AgentID == "" {
				if in.CallID != "" {
					if s.Pending == nil {
						s.Pending = make(map[string]pendingCall)
					}
					s.Pending[in.CallID] = pendingCall{Name: name}
				}
				return nil
			}
		}
		if s.Ready {
			return nil
		}
		s.Blocked = true
		reason = blockedReason
		return nil
	})
	if err != nil {
		reason = "Planning barrier is unavailable; business work is paused."
	}
	if reason != "" {
		return deny(output, reason)
	}
	_, err = io.WriteString(output, "{}\n")
	return err
}

func deny(out io.Writer, reason string) error {
	return json.NewEncoder(out).Encode(map[string]any{"hookSpecificOutput": map[string]any{
		"hookEventName": "PreToolUse", "permissionDecision": "deny", "permissionDecisionReason": reason,
	}})
}

var createdTask = regexp.MustCompile(`^Task #([^\s:]+) created successfully:`)
var updatedTask = regexp.MustCompile(`^Updated task #([^\s:]+)(?:\s|$)`)

func recordSuccessfulTool(s *state, name string, input map[string]any, response any) {
	row, _ := response.(map[string]any)
	if row != nil {
		if failed, _ := row["is_error"].(bool); failed {
			return
		}
		if failed, _ := row["isError"].(bool); failed {
			return
		}
		if success, ok := row["success"].(bool); ok && !success {
			return
		}
		if text, _ := row["error"].(string); text != "" {
			return
		}
	}
	if name == "update_plan" || name == "TodoWrite" {
		acceptPlan(s, core.ParseTaskProgress("native_plan", input))
		return
	}
	if s.Tasks == nil {
		s.Tasks = make(map[string]core.TaskProgressStep)
	}
	changed := false
	put := func(task map[string]any) {
		id, _ := task["id"].(string)
		title, _ := task["subject"].(string)
		status, _ := task["status"].(string)
		if status == "" {
			status = "pending"
		}
		if id == "" || strings.TrimSpace(title) == "" || (status != "pending" && status != "in_progress" && status != "completed") {
			return
		}
		title = strings.TrimSpace(title)
		if runes := []rune(title); len(runes) > 240 {
			title = string(runes[:240])
		}
		if _, ok := s.Tasks[id]; !ok {
			s.Order = append(s.Order, id)
		}
		s.Tasks[id] = core.TaskProgressStep{Title: title, Status: status}
		changed = true
	}
	switch name {
	case "TaskCreate":
		task, _ := row["task"].(map[string]any)
		if task != nil {
			put(task)
		} else if text, ok := response.(string); ok {
			if match := createdTask.FindStringSubmatch(strings.TrimSpace(text)); match != nil {
				put(map[string]any{"id": match[1], "subject": input["subject"]})
			}
		}
	case "TaskUpdate":
		id, _ := input["taskId"].(string)
		if row != nil {
			if success, _ := row["success"].(bool); !success {
				return
			}
			if resultID, _ := row["taskId"].(string); resultID != "" && resultID != id {
				return
			}
		} else {
			text, _ := response.(string)
			match := updatedTask.FindStringSubmatch(strings.TrimSpace(text))
			if match == nil || match[1] != id {
				return
			}
		}
		step, ok := s.Tasks[id]
		if !ok {
			return
		}
		if title, ok := input["subject"].(string); ok {
			step.Title = title
		}
		if status, ok := input["status"].(string); ok {
			step.Status = status
		}
		if step.Status == "deleted" {
			delete(s.Tasks, id)
			for i, known := range s.Order {
				if known == id {
					s.Order = append(s.Order[:i], s.Order[i+1:]...)
					break
				}
			}
			changed = true
		} else {
			put(map[string]any{"id": id, "subject": step.Title, "status": step.Status})
		}
	case "TaskList":
		rows, ok := row["tasks"].([]any)
		if !ok {
			return
		}
		if len(rows) > 12 {
			return
		}
		seen := make(map[string]bool)
		for _, value := range rows {
			task, ok := value.(map[string]any)
			if !ok {
				return
			}
			id, _ := task["id"].(string)
			title, _ := task["subject"].(string)
			status, _ := task["status"].(string)
			if id == "" || strings.TrimSpace(title) == "" || seen[id] || (status != "pending" && status != "in_progress" && status != "completed") {
				return
			}
			seen[id] = true
		}
		s.Tasks, s.Order = make(map[string]core.TaskProgressStep), nil
		changed = true
		for _, value := range rows {
			if task, ok := value.(map[string]any); ok {
				put(task)
			}
		}
	case "TaskGet":
		if task, ok := row["task"].(map[string]any); ok {
			id, _ := task["id"].(string)
			wanted, _ := input["taskId"].(string)
			if id == "" || id != wanted {
				return
			}
			put(task)
		}
	default:
		return
	}
	if !changed {
		return
	}
	steps := make([]core.TaskProgressStep, 0, len(s.Tasks))
	for _, id := range s.Order {
		if step, ok := s.Tasks[id]; ok {
			steps = append(steps, step)
		}
	}
	acceptPlan(s, &core.TaskProgress{Source: "claude_tasks", Steps: steps})
}
