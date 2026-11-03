package codex

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/chenhg5/cc-connect/core"
)

func feedExecJSON(t *testing.T, cs *codexSession, input string) []core.Event {
	t.Helper()
	if err := readJSONLines(strings.NewReader(input), func(line []byte) error {
		var raw map[string]any
		if err := json.Unmarshal(line, &raw); err != nil {
			return err
		}
		cs.handleEvent(raw)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	var events []core.Event
	for len(cs.events) > 0 {
		events = append(events, <-cs.events)
	}
	return events
}

func TestCodexExec_TodoListLifecyclePublishesExplicitProgress(t *testing.T) {
	cs := &codexSession{ctx: context.Background(), events: make(chan core.Event, 16)}
	events := feedExecJSON(t, cs, `
{"type":"item.started","item":{"id":"plan","type":"todo_list","items":[{"text":"Inspect","completed":false},{"text":"Implement","completed":false}]}}
{"type":"item.updated","item":{"id":"plan","type":"todo_list","items":[{"text":"Inspect","completed":true},{"text":"Implement","completed":false}]}}
{"type":"item.completed","item":{"id":"plan","type":"todo_list","items":[{"text":"Inspect","completed":true},{"text":"Implement","completed":false}]}}
`)
	if len(events) != 3 {
		t.Fatalf("events = %+v, want three plan snapshots", events)
	}
	for i, event := range events {
		if event.Type != core.EventTaskProgress || event.TaskProgress == nil || event.TaskProgress.Source != "codex_exec" || len(event.TaskProgress.Steps) != 2 {
			t.Fatalf("event %d = %+v", i, event)
		}
		wantFirst := "completed"
		if i == 0 {
			wantFirst = "pending"
		}
		if event.TaskProgress.Steps[0] != (core.TaskProgressStep{Title: "Inspect", Status: wantFirst}) || event.TaskProgress.Steps[1] != (core.TaskProgressStep{Title: "Implement", Status: "pending"}) {
			t.Fatalf("snapshot %d invented step state: %+v", i, event.TaskProgress.Steps)
		}
	}
}

func TestCodexExec_TodoListClearAndMalformedSnapshots(t *testing.T) {
	for _, eventType := range []string{"item.started", "item.updated", "item.completed"} {
		t.Run(eventType, func(t *testing.T) {
			cs := &codexSession{ctx: context.Background(), events: make(chan core.Event, 16)}
			for _, payload := range []string{
				`{}`, `{"items":null}`, `{"items":"bad"}`,
				`{"items":[{"text":"Missing flag"}]}`,
				`{"items":[{"text":"Invalid flag","completed":"false"}]}`,
				`{"items":[{"text":" ","completed":false}]}`,
				`{"items":[{"text":"Valid","completed":true},null]}`,
			} {
				var item map[string]any
				_ = json.Unmarshal([]byte(payload), &item)
				item["type"] = "todo_list"
				cs.handleEvent(map[string]any{"type": eventType, "item": item})
			}
			if len(cs.events) != 0 {
				t.Fatal("malformed snapshot changed public progress")
			}
			events := feedExecJSON(t, cs, `{"type":"`+eventType+`","item":{"type":"todo_list","items":[]}}`)
			if len(events) != 1 || events[0].TaskProgress == nil || len(events[0].TaskProgress.Steps) != 0 {
				t.Fatalf("explicit clear was discarded: %+v", events)
			}
		})
	}
}

func TestCodexExec_TodoListCompletionPreservesFinalAnswerAndOpenSteps(t *testing.T) {
	cs := &codexSession{ctx: context.Background(), events: make(chan core.Event, 16)}
	events := feedExecJSON(t, cs, `
{"type":"item.completed","item":{"type":"agent_message","text":"Final answer"}}
{"type":"item.completed","item":{"type":"todo_list","items":[{"text":"Still open","completed":false}]}}
{"type":"turn.completed"}
`)
	if len(events) != 3 || events[0].TaskProgress == nil || events[0].TaskProgress.Steps[0].Status != "pending" || events[1].Type != core.EventText || events[1].Content != "Final answer" || events[2].Type != core.EventResult || !events[2].Done {
		t.Fatalf("plan completion changed answer or fabricated progress: %+v", events)
	}
}

func TestCodexExec_UnrelatedItemUpdatesDoNotEmitPlanOrToolResults(t *testing.T) {
	cs := &codexSession{ctx: context.Background(), events: make(chan core.Event, 16)}
	events := feedExecJSON(t, cs, `{"type":"item.updated","item":{"type":"command_execution","command":"pwd","status":"in_progress"}}`)
	if len(events) != 0 {
		t.Fatalf("unrelated update produced a synthetic result: %+v", events)
	}
}
