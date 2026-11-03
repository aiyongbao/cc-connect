package core

import (
	"encoding/json"
	"strings"
)

const maxTaskProgressSteps = 12

// ParseTaskProgress normalizes native plan/todo payloads without deriving
// progress from tool output, elapsed time, or private reasoning.
func ParseTaskProgress(source string, value any) *TaskProgress {
	value = decodeTaskProgressValue(value)
	root, _ := value.(map[string]any)
	rows := taskProgressRows(value)
	// An explicit empty array clears a previous plan. Missing/null/malformed
	// arrays must not erase the last accepted snapshot.
	if rows == nil || len(rows) > maxTaskProgressSteps {
		return nil
	}

	steps := make([]TaskProgressStep, 0, len(rows))
	for _, raw := range rows {
		row, ok := raw.(map[string]any)
		if !ok {
			return nil
		}
		title := firstTaskProgressString(row, "title", "step", "content")
		status := normalizeTaskProgressStatus(firstTaskProgressString(row, "status"))
		if title == "" || status == "" {
			return nil
		}
		steps = append(steps, TaskProgressStep{Title: truncateIf(title, 240), Status: status})
	}

	progress := &TaskProgress{Source: strings.TrimSpace(source), Steps: steps}
	if root != nil {
		progress.Explanation = truncateIf(strings.TrimSpace(firstTaskProgressString(root, "explanation", "note")), 500)
	}
	return progress
}

func decodeTaskProgressValue(value any) any {
	switch typed := value.(type) {
	case json.RawMessage:
		var decoded any
		if json.Unmarshal(typed, &decoded) == nil {
			return decoded
		}
	case []byte:
		var decoded any
		if json.Unmarshal(typed, &decoded) == nil {
			return decoded
		}
	case string:
		var decoded any
		if json.Unmarshal([]byte(typed), &decoded) == nil {
			return decoded
		}
	}
	return value
}

func taskProgressRows(value any) []any {
	if rows, ok := value.([]any); ok {
		return rows
	}
	root, ok := value.(map[string]any)
	if !ok {
		return nil
	}
	for _, key := range []string{"steps", "plan", "todos", "entries"} {
		if rows, ok := root[key].([]any); ok {
			return rows
		}
	}
	for _, key := range []string{"update", "data", "params"} {
		if nested := taskProgressRows(root[key]); nested != nil {
			return nested
		}
	}
	return nil
}

func firstTaskProgressString(row map[string]any, keys ...string) string {
	for _, key := range keys {
		if value, ok := row[key].(string); ok && strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func normalizeTaskProgressStatus(status string) string {
	switch strings.ToLower(strings.ReplaceAll(strings.TrimSpace(status), "-", "_")) {
	case "pending":
		return "pending"
	case "in_progress", "inprogress":
		return "in_progress"
	case "completed":
		return "completed"
	default:
		return ""
	}
}

// ParseTaskProgressTool recognizes only explicit plan/todo tools. The caller
// must wait for a successful tool result before publishing the returned plan.
func ParseTaskProgressTool(toolName, input string) *TaskProgress {
	key := strings.ToLower(strings.TrimSpace(toolName))
	if index := strings.LastIndexAny(key, "/:"); index >= 0 {
		key = key[index+1:]
	}
	switch strings.ReplaceAll(key, "-", "_") {
	case "update_plan", "plan_update", "write_todos", "todo_write", "todowrite", "update_todos":
		return ParseTaskProgress(key, input)
	default:
		return nil
	}
}

func formatTaskProgressFallback(progress *TaskProgress) string {
	if progress == nil {
		return ""
	}
	lines := make([]string, 0, len(progress.Steps))
	for _, step := range progress.Steps {
		marker := "[ ]"
		switch step.Status {
		case "completed":
			marker = "[x]"
		case "in_progress":
			marker = "[>]"
		}
		lines = append(lines, marker+" "+step.Title)
	}
	return strings.Join(lines, "\n")
}
