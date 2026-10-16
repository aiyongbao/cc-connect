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
				if len(elements) != 1 || elements[0]["tag"] != "collapsible_panel" || elements[0]["expanded"] != false {
					t.Fatalf("want one collapsed panel, got %+v", elements)
				}
				panel := elements[0]
				title := panel["header"].(map[string]any)["title"].(map[string]any)["content"]
				if title != fmt.Sprintf("%d 个工具调用 ▸", count) {
					t.Fatalf("incorrect group title: %v", title)
				}
				children := panel["elements"].([]any)
				if len(children) != count {
					t.Fatalf("tool details should be simple rows: %+v", children)
				}
				want := []string{
					"**Bash** · pwd · <font color='grey'>已完成</font>",
					"**Bash** · node --version · <font color='grey'>执行中</font>",
					"**Bash** · git --version · <font color='grey'>失败</font>",
				}
				assertTimelineGreyIcon(t, panel["header"].(map[string]any), "setting_outlined")
				if !strings.HasSuffix(title.(string), " ▸") {
					t.Fatalf("collapsed tool group needs an expand cue: %v", title)
				}
				for index, child := range children {
					row := child.(map[string]any)
					if row["content"] != want[index] {
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
	if elements[1]["content"] != "check results" || elements[3]["content"] != "one last check" {
		t.Fatalf("reasoning moved across tools: %+v", elements)
	}
	panel := elements[4]
	if panel["tag"] != "collapsible_panel" || panel["expanded"] != false {
		t.Fatalf("single tool call must stay collapsed: %+v", panel)
	}
	children := panel["elements"].([]any)
	if len(children) != 1 || !strings.Contains(children[0].(map[string]any)["content"].(string), "fifth") {
		t.Fatalf("single tool call detail was lost: %+v", panel)
	}
}

// Keep reasoning and tools distinct even in monochrome and retain one prefix
// for merged thoughts, rather than repeating an emoji on every paragraph.
func TestTimelineCard_GreyIconsForMergedThinkingAndSingleTool(t *testing.T) {
	payload := &core.ProgressCardPayload{Style: "timeline", Lang: "en", Items: []core.ProgressCardEntry{
		{Kind: core.ProgressEntryThinking, Text: "first thought"},
		{Kind: core.ProgressEntryThinking, Text: "second thought"},
		{Kind: core.ProgressEntryToolUse, Tool: "Read", Text: "file", Status: "completed"},
		{Kind: core.ProgressEntryInfo, Text: "update"},
	}}
	elements := timelineCardElements(t, payload)
	if len(elements) != 3 || elements[0]["content"] != "first thought\n\nsecond thought" {
		t.Fatalf("thoughts must remain merged: %+v", elements)
	}
	assertTimelineGreyIcon(t, elements[0], "efficiency_outlined")
	assertTimelineGreyIcon(t, elements[1]["header"].(map[string]any), "setting_outlined")
	toolRow := elements[1]["elements"].([]any)[0].(map[string]any)
	assertTimelineGreyIcon(t, toolRow, "setting_outlined")
	if _, hasIcon := elements[2]["icon"]; hasIcon {
		t.Fatalf("informational updates should not have a kind icon: %+v", elements[2])
	}
	for _, element := range []map[string]any{elements[0], toolRow, elements[2]} {
		if strings.ContainsAny(element["content"].(string), "🧠✅❌⏳🔧") {
			t.Fatalf("colorful emoji remained in progress: %+v", element)
		}
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
