package feishu

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/chenhg5/cc-connect/core"
)

func TestTimelineCard_OrderedGroupsWithoutStopButton(t *testing.T) {
	payload := &core.ProgressCardPayload{Style: "timeline", Lang: "zh", State: core.ProgressCardStateRunning, TurnID: "unique-token", SessionKey: "feishu:chat:thread", Items: []core.ProgressCardEntry{
		{Kind: core.ProgressEntryThinking, Text: "first thought"},
		{Kind: core.ProgressEntryToolUse, Tool: "Bash", Text: "pwd", Status: "completed"},
		{Kind: core.ProgressEntryToolUse, Tool: "Read", Text: "file", Status: "failed"},
		{Kind: core.ProgressEntryThinking, Text: "last thought"},
	}}
	raw := buildProgressCardJSONFromPayload(payload)
	var card struct {
		Body struct {
			Elements []map[string]any `json:"elements"`
		} `json:"body"`
	}
	if err := json.Unmarshal([]byte(raw), &card); err != nil {
		t.Fatal(err)
	}
	elements := card.Body.Elements
	if len(elements) != 3 {
		t.Fatalf("want 3 ordered groups, got %s", raw)
	}
	toolGroup := elements[1]
	toolRows := toolGroup["elements"].([]any)
	if elements[0]["content"] != "first thought" || !strings.Contains(toolRows[0].(map[string]any)["content"].(string), "**Bash**") || !strings.Contains(toolRows[1].(map[string]any)["content"].(string), "**Read**") || elements[2]["content"] != "last thought" {
		t.Fatalf("incorrect order/grouping: %s", raw)
	}
	if toolGroup["tag"] != "collapsible_panel" || toolGroup["expanded"] != false || strings.Count(raw, "collapsible_panel") != 1 {
		t.Fatalf("want one collapsed tool group: %s", raw)
	}
	// Stopping uses the existing /stop command in every card state.
	for _, state := range []core.ProgressCardState{core.ProgressCardStateRunning, core.ProgressCardStateCompleted, core.ProgressCardStatePaused, core.ProgressCardStateInterrupted, core.ProgressCardStateFailed} {
		payload.State = state
		raw = buildProgressCardJSONFromPayload(payload)
		if strings.Contains(raw, `"tag":"button"`) || strings.Contains(raw, "timeline-stop") || strings.Contains(raw, "unique-token") || strings.Contains(raw, "feishu:chat:thread") {
			t.Fatalf("stop button or callback metadata on %s card: %s", state, raw)
		}
		if state == core.ProgressCardStateInterrupted && !strings.Contains(raw, "已被中断") {
			t.Fatal(raw)
		}
	}
}

func TestTimelineCard_InterruptedUsesWarningColor(t *testing.T) {
	payload := &core.ProgressCardPayload{
		Style: "timeline", Lang: "zh", State: core.ProgressCardStateInterrupted,
		Items: []core.ProgressCardEntry{{Kind: core.ProgressEntryThinking, Text: "执行被停止"}},
	}
	var card struct {
		Header struct {
			Template string `json:"template"`
		} `json:"header"`
	}
	if err := json.Unmarshal([]byte(buildProgressCardJSONFromPayload(payload)), &card); err != nil {
		t.Fatal(err)
	}
	if card.Header.Template != "orange" {
		t.Fatalf("interrupted card template = %q, want orange", card.Header.Template)
	}
}

func TestTimelineCard_CompletedCollapsesWholeProcess(t *testing.T) {
	payload := &core.ProgressCardPayload{
		Style: "timeline", Lang: "zh", State: core.ProgressCardStateCompleted,
		UpdatedAt: "2026-10-16T11:05:45+08:00",
		Items: []core.ProgressCardEntry{
			{Kind: core.ProgressEntryThinking, Text: "检查项目"},
			{Kind: core.ProgressEntryToolUse, Tool: "Read", Text: "main.go", Status: "completed"},
		},
	}
	elements := timelineCardElements(t, payload)
	if len(elements) != 1 || elements[0]["tag"] != "collapsible_panel" || elements[0]["expanded"] != false {
		t.Fatalf("completed process should be collapsed, got %+v", elements)
	}
	title := elements[0]["header"].(map[string]any)["title"].(map[string]any)["content"]
	if title != "执行过程（2 条更新） ▸" {
		t.Fatalf("unexpected completed process title: %v", title)
	}
	children := elements[0]["elements"].([]any)
	if len(children) != 2 || children[1].(map[string]any)["tag"] != "collapsible_panel" {
		t.Fatalf("completed process lost its inner groups: %+v", children)
	}
	if _, hasBackground := elements[0]["background_color"]; hasBackground {
		t.Fatalf("completed outer panel should use the theme default, got %v", elements[0]["background_color"])
	}
	if children[1].(map[string]any)["background_color"] != "grey" {
		t.Fatalf("inner tool panel background = %v, want grey", children[1].(map[string]any)["background_color"])
	}
}

// Only timeline cards show the timestamp of their latest write. It does not
// update on its own while the agent is silent.
func TestTimelineCard_ShowsOneUpdateTime(t *testing.T) {
	payload := &core.ProgressCardPayload{
		Style: "timeline", Lang: "zh", State: core.ProgressCardStateRunning,
		UpdatedAt: "2026-10-16T11:05:45+08:00",
		Items:     []core.ProgressCardEntry{{Kind: core.ProgressEntryThinking, Text: "检查中"}},
	}
	card := struct {
		Header struct {
			Title struct {
				Content string `json:"content"`
			} `json:"title"`
			TextTagList []map[string]any `json:"text_tag_list"`
		} `json:"header"`
		Body struct {
			Elements []map[string]any `json:"elements"`
		} `json:"body"`
	}{}
	if err := json.Unmarshal([]byte(buildProgressCardJSONFromPayload(payload)), &card); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(card.Header.Title.Content, "执行中") {
		t.Fatalf("header title should keep the state label, got %q", card.Header.Title.Content)
	}
	if len(card.Header.TextTagList) != 1 {
		t.Fatalf("want one header clock tag, got %+v", card.Header.TextTagList)
	}
	tagText, _ := card.Header.TextTagList[0]["text"].(map[string]any)
	// 2026-10-16T11:05:45+08:00 formatted server-side as HH:MM:SS.
	if content, _ := tagText["content"].(string); content != "11:05:45" {
		t.Fatalf("clock tag should show the time only, got %v", tagText)
	}
	for _, el := range card.Body.Elements {
		if strings.Contains(fmt.Sprint(el["content"]), "更新时间") {
			t.Fatalf("update time should not appear in the body: %+v", el)
		}
	}
	payload.Style = "compact"
	if strings.Contains(buildProgressCardJSONFromPayload(payload), "更新时间") {
		t.Fatal("timeline timestamp leaked into an existing progress style")
	}
}

// All progress groups share the existing per-card table budget. Invalid images
// must not keep subsequent updates and terminal cards from being accepted.
func TestTimelineCard_SanitizesMarkdownWithSharedTableBudget(t *testing.T) {
	for _, grouped := range []bool{false, true} {
		for _, state := range []core.ProgressCardState{core.ProgressCardStateRunning, core.ProgressCardStateCompleted, core.ProgressCardStatePaused, core.ProgressCardStateInterrupted, core.ProgressCardStateFailed} {
			t.Run(string(state)+map[bool]string{false: "/single", true: "/multiple"}[grouped], func(t *testing.T) {
				table := "| A | B |\n| --- | --- |\n| x | y |\n\n"
				items := []core.ProgressCardEntry{{Kind: core.ProgressEntryThinking, Text: "Tables\n\n" + strings.Repeat(table, 6)}}
				if grouped {
					items = nil
					for i := 0; i < 3; i++ {
						items = append(items, core.ProgressCardEntry{Kind: core.ProgressEntryThinking, Text: "Tables\n\n" + strings.Repeat(table, 2)}, core.ProgressCardEntry{Kind: core.ProgressEntryToolUse, Tool: "Read", Text: "file", Status: "completed"})
					}
				}
				items = append(items, core.ProgressCardEntry{Kind: core.ProgressEntryThinking, Text: "Images\n\n![remote](https://example.com/image.png)\n\n![local](/tmp/image.png)\n\n![ok](img_v3_valid)"})
				payload := &core.ProgressCardPayload{Style: "timeline", Lang: "en", State: state, Items: items}
				segments := collectCardMarkdownContents(t, buildTimelineCard(payload))
				count := 0
				for _, content := range segments {
					count += len(findMarkdownTablesOutsideCodeBlocks(content))
				}
				if count != feishuCardTableLimit {
					t.Fatalf("card contains %d tables, want shared budget %d", count, feishuCardTableLimit)
				}
				content := strings.Join(segments, "\n")
				if strings.Contains(content, "![remote]") || strings.Contains(content, "![local]") || !strings.Contains(content, "![ok](img_v3_valid)") {
					t.Fatalf("invalid card image handling: %q", content)
				}
				if !strings.Contains(content, "```\n| A | B |") {
					t.Fatal("over-budget tables were not preserved as code blocks")
				}
			})
		}
	}
}
