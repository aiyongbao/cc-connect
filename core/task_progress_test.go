package core

import "testing"

func TestParseTaskProgress_NormalizesNativePlans(t *testing.T) {
	progress := ParseTaskProgress("codex", map[string]any{
		"explanation": "Move to implementation",
		"plan": []any{
			map[string]any{"step": "Inspect the current card", "status": "completed"},
			map[string]any{"step": "Implement the visual hierarchy", "status": "inProgress"},
			map[string]any{"step": "Verify the final card", "status": "pending"},
		},
	})
	if progress == nil || len(progress.Steps) != 3 {
		t.Fatalf("progress = %#v", progress)
	}
	if progress.Steps[1].Status != "in_progress" || progress.Explanation != "Move to implementation" {
		t.Fatalf("progress was not normalized: %#v", progress)
	}
}

func TestParseTaskProgressTool_AcceptsExplicitPlanToolsOnly(t *testing.T) {
	progress := ParseTaskProgressTool("update_plan", `{"plan":[{"step":"Inspect","status":"in_progress"}]}`)
	if progress == nil || progress.Steps[0].Title != "Inspect" {
		t.Fatalf("update_plan = %#v", progress)
	}
	if got := ParseTaskProgressTool("Bash", `{"plan":[{"step":"Pretend","status":"completed"}]}`); got != nil {
		t.Fatalf("ordinary tool produced progress: %#v", got)
	}
}

func TestParseTaskProgress_RejectsMalformedOrInventedStatuses(t *testing.T) {
	for _, value := range []any{
		map[string]any{"steps": nil},
		map[string]any{"steps": []any{map[string]any{"title": "Guess", "status": "running"}}},
		map[string]any{"steps": []any{map[string]any{"title": "", "status": "pending"}}},
	} {
		if got := ParseTaskProgress("test", value); got != nil {
			t.Fatalf("invalid payload accepted: %#v", got)
		}
	}
}

func TestParseTaskProgress_ExplicitEmptyPlansClearButMissingPlansDoNot(t *testing.T) {
	for _, input := range []string{`{"steps":[]}`, `{"plan":[]}`, `{"todos":[]}`, `{"entries":[]}`, `{"update":{"plan":[]}}`} {
		progress := ParseTaskProgress("test", input)
		if progress == nil || len(progress.Steps) != 0 {
			t.Fatalf("explicit clear rejected: %s", input)
		}
	}
	for _, input := range []string{`{}`, `{"plan":null}`, `{"plan":"invalid"}`} {
		if got := ParseTaskProgress("test", input); got != nil {
			t.Fatalf("missing/malformed plan cleared progress: %s", input)
		}
	}
}
