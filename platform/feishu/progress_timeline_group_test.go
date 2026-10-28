package feishu

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/chenhg5/cc-connect/core"
)

// Even one or two calls must start collapsed throughout a turn and retain
// simple details as more consecutive tool calls arrive.
func TestTimelineCard_AllToolCountsDefaultCollapsed(t *testing.T) {
	for _, count := range []int{1, 2, 3} {
		for _, state := range []core.ProgressCardState{core.ProgressCardStateRunning, core.ProgressCardStateCompleted, core.ProgressCardStatePaused, core.ProgressCardStateInterrupted, core.ProgressCardStateFailed} {
			t.Run(fmt.Sprintf("%d/%s", count, state), func(t *testing.T) {
				payload := &core.ProgressCardPayload{Style: "timeline", Lang: "zh", State: state, Items: []core.ProgressCardEntry{
					{Kind: core.ProgressEntryToolUse, Tool: "Bash", Text: "pwd", Status: "completed"},
					{Kind: core.ProgressEntryToolUse, Tool: "Bash", Text: "node --version", Status: "running"},
					{Kind: core.ProgressEntryToolUse, Tool: "Bash", Text: "git --version", Status: "failed"},
				}}
				payload.Items = payload.Items[:count]
				elements := timelineCardElements(t, payload)
				wantElements := 1
				if state == core.ProgressCardStateCompleted {
					wantElements = 3 // collapsed history, separator, completion notice
				}
				if len(elements) != wantElements || elements[0]["tag"] != "collapsible_panel" || elements[0]["expanded"] != false {
					t.Fatalf("want one collapsed panel, got %+v", elements)
				}
				panel := elements[0]
				if state == core.ProgressCardStateCompleted {
					if panel["header"].(map[string]any)["title"].(map[string]any)["content"] != fmt.Sprintf("执行过程（%d 条更新） ▸", count) {
						t.Fatalf("incorrect completed details title: %v", panel["header"])
					}
					children := panel["elements"].([]any)
					if len(children) != 1 {
						t.Fatalf("completed card should contain one inner tool group: %+v", panel)
					}
					panel = children[0].(map[string]any)
				}
				title := panel["header"].(map[string]any)["title"].(map[string]any)["content"]
				if title != fmt.Sprintf("%d 个工具调用 ▸", count) {
					t.Fatalf("incorrect group title: %v", title)
				}
				children := panel["elements"].([]any)
				if len(children) != count {
					t.Fatalf("tool details should be simple rows: %+v", children)
				}
				want := []string{
					"Bash · pwd · 已完成",
					"Bash · node --version · 执行中",
					"Bash · git --version · 失败",
				}
				assertTimelineGreyIcon(t, panel["header"].(map[string]any), "setting_outlined")
				if !strings.HasSuffix(title.(string), " ▸") {
					t.Fatalf("collapsed tool group needs an expand cue: %v", title)
				}
				for index, child := range children {
					row := child.(map[string]any)
					if timelineElementContent(row) != want[index] {
						t.Fatalf("missing or reordered tool detail: %v", row)
					}
					assertTimelineGreyIcon(t, row, "setting_outlined")
				}
			})
		}
	}
}

// Reasoning separates tool groups; single-call groups also remain collapsed.
func TestTimelineCard_ToolGroupsDoNotCrossReasoning(t *testing.T) {
	payload := &core.ProgressCardPayload{Style: "timeline", Lang: "en", State: core.ProgressCardStateRunning, Items: []core.ProgressCardEntry{
		{Kind: core.ProgressEntryToolUse, Tool: "Read", Text: "first"},
		{Kind: core.ProgressEntryToolUse, Tool: "Read", Text: "second"},
		{Kind: core.ProgressEntryThinking, Text: "check results"},
		{Kind: core.ProgressEntryToolUse, Tool: "Bash", Text: "third"},
		{Kind: core.ProgressEntryToolUse, Tool: "Bash", Text: "fourth"},
		{Kind: core.ProgressEntryThinking, Text: "one last check"},
		{Kind: core.ProgressEntryToolUse, Tool: "Read", Text: "fifth"},
	}}
	elements := timelineCardElements(t, payload)
	if len(elements) != 5 {
		t.Fatalf("incorrect group count/order: %+v", elements)
	}
	for _, index := range []int{0, 2} {
		panel := elements[index]
		if panel["tag"] != "collapsible_panel" || panel["expanded"] != false {
			t.Fatalf("group %d was not collapsed: %+v", index, panel)
		}
		if title := panel["header"].(map[string]any)["title"].(map[string]any)["content"]; title != "2 tool calls ▸" {
			t.Fatalf("group %d has incorrect count: %v", index, title)
		}
	}
	if timelineElementContent(elements[1]) != "check results" || timelineElementContent(elements[3]) != "one last check" {
		t.Fatalf("reasoning moved across tools: %+v", elements)
	}
	panel := elements[4]
	if panel["tag"] != "collapsible_panel" || panel["expanded"] != false {
		t.Fatalf("single tool call must stay collapsed: %+v", panel)
	}
	children := panel["elements"].([]any)
	if len(children) != 1 || !strings.Contains(timelineElementContent(children[0].(map[string]any)), "fifth") {
		t.Fatalf("single tool call detail was lost: %+v", panel)
	}
}

// Keep reasoning and tools distinct even in monochrome. Every thought gets
// its own outline icon, including adjacent messages within the same segment.
func TestTimelineCard_GreyIconsForEveryThinkingAndSingleTool(t *testing.T) {
	payload := &core.ProgressCardPayload{Style: "timeline", Lang: "en", Items: []core.ProgressCardEntry{
		{Kind: core.ProgressEntryThinking, Text: "first thought"},
		{Kind: core.ProgressEntryThinking, Text: "second thought"},
		{Kind: core.ProgressEntryToolUse, Tool: "Read", Text: "file", Status: "completed"},
		{Kind: core.ProgressEntryInfo, Text: "update"},
	}}
	elements := timelineCardElements(t, payload)
	if len(elements) != 4 || timelineElementContent(elements[0]) != "first thought" || timelineElementContent(elements[1]) != "second thought" {
		t.Fatalf("thoughts must retain separate icon rows: %+v", elements)
	}
	assertTimelineGreyIcon(t, elements[0], "mindmap_outlined")
	assertTimelineGreyIcon(t, elements[1], "mindmap_outlined")
	assertTimelineGreyIcon(t, elements[2]["header"].(map[string]any), "setting_outlined")
	toolRow := elements[2]["elements"].([]any)[0].(map[string]any)
	assertTimelineGreyIcon(t, toolRow, "setting_outlined")
	if _, hasIcon := elements[3]["icon"]; hasIcon {
		t.Fatalf("informational updates should not have a kind icon: %+v", elements[3])
	}
	for _, element := range []map[string]any{elements[0], elements[1], toolRow, elements[3]} {
		if strings.ContainsAny(timelineElementContent(element), "🧠✅❌⏳🔧") {
			t.Fatalf("colorful emoji remained in progress: %+v", element)
		}
	}
	// Completion wraps the rows in history; expanding it must still expose
	// one icon per thought instead of flattening the consecutive messages.
	payload.State = core.ProgressCardStateCompleted
	completed := timelineCardElements(t, payload)
	children := completed[0]["elements"].([]any)
	for i, want := range []string{"first thought", "second thought"} {
		row := children[i].(map[string]any)
		if timelineElementContent(row) != want {
			t.Fatalf("completed thought %d lost its boundary: %+v", i, row)
		}
		assertTimelineGreyIcon(t, row, "mindmap_outlined")
	}
}

func TestTimelineCard_BackgroundRunningUsesBackgroundLabelAndPrefix(t *testing.T) {
	payload := &core.ProgressCardPayload{
		Style: "timeline", Lang: "zh", State: core.ProgressCardStateRunning,
		Background: true, TitleDots: 3,
		Items: []core.ProgressCardEntry{
			{Kind: core.ProgressEntryThinking, Text: "等待后台任务", Background: true},
			{Kind: core.ProgressEntryToolUse, Tool: "Bash", Text: "pwd", Status: "running", Background: true},
		},
	}
	var card struct {
		Header struct {
			Title struct {
				Content string `json:"content"`
			} `json:"title"`
		} `json:"header"`
	}
	if err := json.Unmarshal([]byte(buildTimelineCard(payload)), &card); err != nil {
		t.Fatal(err)
	}
	if card.Header.Title.Content != "后台执行中..." {
		t.Fatalf("background title = %q, want 后台执行中...", card.Header.Title.Content)
	}
	elements := timelineCardElements(t, payload)
	if timelineElementContent(elements[0]) != "(后台) 等待后台任务" {
		t.Fatalf("background thinking = %q", timelineElementContent(elements[0]))
	}
	toolRows := elements[1]["elements"].([]any)
	if len(toolRows) != 1 || timelineElementContent(toolRows[0].(map[string]any)) != "(后台) Bash · pwd · 执行中" {
		t.Fatalf("background tool row = %+v", toolRows)
	}
}

// Inspect the actual platform JSON to catch unsupported emoji fallbacks.
func assertTimelineGreyIcon(t *testing.T, element map[string]any, token string) {
	t.Helper()
	icon, ok := element["icon"].(map[string]any)
	if !ok || icon["tag"] != "standard_icon" || icon["token"] != token || icon["color"] != "grey" {
		t.Fatalf("want native grey icon %s, got %+v", token, element["icon"])
	}
}

// Decode the rendered card at the platform boundary so grouping tests cover
// the same JSON schema that Feishu receives, not renderer implementation state.
func timelineCardElements(t *testing.T, payload *core.ProgressCardPayload) []map[string]any {
	t.Helper()
	var card struct {
		Body struct {
			Elements []map[string]any `json:"elements"`
		} `json:"body"`
	}
	if err := json.Unmarshal([]byte(buildProgressCardJSONFromPayload(payload)), &card); err != nil {
		t.Fatal(err)
	}
	return card.Body.Elements
}

// timelineElementContent reads either a progress text row or an informational
// markdown element from the actual rendered card.
func timelineElementContent(element map[string]any) string {
	if text, ok := element["text"].(map[string]any); ok {
		content, _ := text["content"].(string)
		return content
	}
	content, _ := element["content"].(string)
	return content
}

// collectTimelineTextContents preserves row order across nested tool/history
// panels while reading both plain text and informational markdown.
func collectTimelineTextContents(t *testing.T, payload *core.ProgressCardPayload) []string {
	t.Helper()
	var contents []string
	var visit func([]map[string]any)
	visit = func(elements []map[string]any) {
		for _, element := range elements {
			if content := timelineElementContent(element); content != "" {
				contents = append(contents, content)
			}
			if children, ok := element["elements"].([]any); ok {
				nested := make([]map[string]any, 0, len(children))
				for _, child := range children {
					nested = append(nested, child.(map[string]any))
				}
				visit(nested)
			}
		}
	}
	visit(timelineCardElements(t, payload))
	return contents
}
