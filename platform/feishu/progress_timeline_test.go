package feishu

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

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
	if timelineElementContent(elements[0]) != "first thought" || !strings.Contains(timelineElementContent(toolRows[0].(map[string]any)), "Bash") || !strings.Contains(timelineElementContent(toolRows[1].(map[string]any)), "Read") || timelineElementContent(elements[2]) != "last thought" {
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

// Running titles animate using the supplied write phase. All other states
// keep the existing Chinese translations even if a stale phase is supplied.
func TestTimelineCard_TitleDotsAndTranslatedStates(t *testing.T) {
	for _, tc := range []struct {
		state core.ProgressCardState
		dots  int
		want  string
	}{
		{core.ProgressCardStateRunning, 1, "执行中."},
		{core.ProgressCardStateRunning, 2, "执行中.."},
		{core.ProgressCardStateRunning, 3, "执行中..."},
		{core.ProgressCardStateRunning, 0, "执行中."},
		{core.ProgressCardStatePaused, 3, "等待你的选择"},
		{core.ProgressCardStateCompleted, 3, "已完成"},
		{core.ProgressCardStateInterrupted, 3, "已被中断"},
		{core.ProgressCardStateFailed, 3, "执行失败"},
	} {
		payload := &core.ProgressCardPayload{
			Style: "timeline", Lang: "zh", State: tc.state, TitleDots: tc.dots,
			Items: []core.ProgressCardEntry{{Kind: core.ProgressEntryThinking, Text: "检查中"}},
		}
		var card struct {
			Header struct {
				Title struct{ Content string }
			}
		}
		if err := json.Unmarshal([]byte(buildTimelineCard(payload)), &card); err != nil {
			t.Fatal(err)
		}
		if card.Header.Title.Content != tc.want {
			t.Fatalf("%s/%d title = %q, want %q", tc.state, tc.dots, card.Header.Title.Content, tc.want)
		}
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
	if len(elements) != 3 || elements[0]["tag"] != "collapsible_panel" || elements[0]["expanded"] != false {
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

// Progress text retains its existing sanitizer even when rendered as plain
// small text. Invalid images and over-budget tables must remain normalized.
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
				segments := collectTimelineTextContents(t, payload)
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

// Progress rows match rich Reasoning font/colour, both while running and
// inside completed collapsed history. Plain text must not expose generated
// markdown wrappers around tool names or statuses.
func TestTimelineCard_ThoughtAndToolTextMatchRichReasoningStyle(t *testing.T) {
	reference := richStepElement(core.ToolStep{Kind: core.ToolStepKindThinking, Summary: "reference"})["text"].(map[string]any)
	for _, state := range []core.ProgressCardState{core.ProgressCardStateRunning, core.ProgressCardStateCompleted} {
		payload := &core.ProgressCardPayload{
			Style: "timeline", Lang: "zh", State: state,
			Items: []core.ProgressCardEntry{
				{Kind: core.ProgressEntryThinking, Text: "检查中"},
				{Kind: core.ProgressEntryToolUse, Tool: "Read", Text: "/tmp/file", Status: "completed"},
			},
		}
		thought, tool := false, false
		var visit func([]map[string]any)
		visit = func(elements []map[string]any) {
			for _, element := range elements {
				if icon, ok := element["icon"].(map[string]any); ok {
					token := icon["token"]
					if element["tag"] == "div" && (token == timelineThinkingIcon || token == timelineToolIcon) {
						text := element["text"].(map[string]any)
						for _, key := range []string{"tag", "text_size", "text_color"} {
							if text[key] != reference[key] {
								t.Fatalf("%s %v differs from Reasoning style: %+v", state, key, text)
							}
						}
						thought = thought || token == timelineThinkingIcon
						tool = tool || token == timelineToolIcon
						content := text["content"].(string)
						if strings.Contains(content, "<font") || strings.Contains(content, "**Read**") {
							t.Fatalf("plain text row exposes generated formatting: %s", content)
						}
					}
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
		if !thought || !tool {
			t.Fatalf("%s card lost the thought/tool text rows", state)
		}
	}
}

// 回归：从未产生思考或工具进度的对话，终态卡片不应再渲染只有占位文本的
// “执行过程”面板；完成时只显示引导用户查看下一条答复的提示。
func TestTimelineCard_TerminalWithoutProgressHidesPlaceholder(t *testing.T) {
	placeholder := core.NewI18n(core.LangChinese).T(core.MsgTimelineNoProgress)
	for _, state := range []core.ProgressCardState{core.ProgressCardStateCompleted, core.ProgressCardStateInterrupted, core.ProgressCardStateFailed} {
		payload := &core.ProgressCardPayload{
			Style: "timeline", Lang: "zh", State: state,
			Items: []core.ProgressCardEntry{{Kind: core.ProgressEntryInfo, Text: placeholder}},
		}
		elements := timelineCardElements(t, payload)
		if state == core.ProgressCardStateCompleted {
			if len(elements) != 1 || elements[0]["tag"] != "div" {
				t.Fatalf("empty completed card should contain only the notice, got %+v", elements)
			}
			continue
		}
		if len(elements) != 0 {
			t.Fatalf("%s card should hide the placeholder body, got %+v", state, elements)
		}
		// 飞书对空 body 仍渲染默认内边距，且 schema 2.0 强制要求
		// body 字段（缺失报 "card must have body"），因此终态无过程时
		// 必须输出上下内边距为 0 的空 body，避免头部下方留白。
		raw := buildTimelineCard(payload)
		if !strings.Contains(raw, "\"padding\":\"0px 16px\"") {
			t.Fatalf("%s card should collapse the empty body padding, got %s", state, raw)
		}
	}
	// 对照：存在真实过程时，“执行过程”折叠面板保持原行为。
	payload := &core.ProgressCardPayload{
		Style: "timeline", Lang: "zh", State: core.ProgressCardStateCompleted,
		Items: []core.ProgressCardEntry{{Kind: core.ProgressEntryThinking, Text: "检查中"}},
	}
	elements := timelineCardElements(t, payload)
	if len(elements) != 3 || elements[0]["tag"] != "collapsible_panel" {
		t.Fatalf("real progress should keep the details panel, got %+v", elements)
	}
}

// The completion notice uses the legacy text/style and remains outside the
// collapsed history. Empty completed cards contain the notice alone.
func TestTimelineCard_CompletionNoticeMatchesLegacy(t *testing.T) {
	for _, lang := range []string{"zh", "en"} {
		for _, withProgress := range []bool{false, true} {
			payload := &core.ProgressCardPayload{
				Style: "timeline", Lang: lang, State: core.ProgressCardStateCompleted,
			}
			if withProgress {
				payload.Items = []core.ProgressCardEntry{{Kind: core.ProgressEntryThinking, Text: "review"}}
			}
			elements := timelineCardElements(t, payload)
			wantCount := 1
			if withProgress {
				wantCount = 3
				if elements[0]["tag"] != "collapsible_panel" || elements[1]["tag"] != "hr" {
					t.Fatalf("notice should follow collapsed history: %+v", elements)
				}
			}
			if len(elements) != wantCount {
				t.Fatalf("notice card elements = %d, want %d", len(elements), wantCount)
			}
			notice := elements[len(elements)-1]
			text := notice["text"].(map[string]any)
			_, _, wantText := progressStateMeta(payload.State, lang, "")
			if notice["tag"] != "div" || text["content"] != wantText || text["text_size"] != "notation" || text["text_color"] != "grey" {
				t.Fatalf("completion notice differs from legacy: %+v", notice)
			}
			for _, state := range []core.ProgressCardState{core.ProgressCardStateRunning, core.ProgressCardStatePaused, core.ProgressCardStateInterrupted, core.ProgressCardStateFailed} {
				payload.State = state
				if strings.Contains(buildTimelineCard(payload), wantText) {
					t.Fatalf("completion notice leaked into %s card", state)
				}
			}
		}
	}
}

// The completed panel's number is cumulative even when the body holds only
// the latest entries; old payloads without TotalEntries retain their fallback.
func TestTimelineCard_CompletedTitleUsesCumulativeCount(t *testing.T) {
	payload := &core.ProgressCardPayload{
		Style: "timeline", Lang: "zh", State: core.ProgressCardStateCompleted,
		TotalEntries: 71, Truncated: true,
		Items: []core.ProgressCardEntry{
			{Kind: core.ProgressEntryThinking, Text: "latest thought"},
			{Kind: core.ProgressEntryToolUse, Tool: "Read", Text: "latest file", Status: "completed"},
		},
	}
	for _, tc := range []struct{ total, want int }{{71, 71}, {0, 2}} {
		payload.TotalEntries = tc.total
		elements := timelineCardElements(t, payload)
		title := elements[0]["header"].(map[string]any)["title"].(map[string]any)["content"]
		if title != fmt.Sprintf("执行过程（%d 条更新） ▸", tc.want) {
			t.Fatalf("completed title counted only retained entries: %v", title)
		}
	}
}

func TestTimelineCard_TruncatedHintUsesRichStyleAndCount(t *testing.T) {
	payload := &core.ProgressCardPayload{
		Style: "timeline", Lang: "zh", State: core.ProgressCardStateRunning,
		TotalEntries: 27, Truncated: true,
		Items: []core.ProgressCardEntry{{Kind: core.ProgressEntryThinking, Text: "latest thought"}},
	}
	elements := timelineCardElements(t, payload)
	if len(elements) == 0 || elements[0]["tag"] != "div" {
		t.Fatalf("truncated hint should be the first muted div: %+v", elements)
	}
	text, ok := elements[0]["text"].(map[string]any)
	if !ok {
		t.Fatalf("truncated hint has no text block: %+v", elements[0])
	}
	if text["content"] != "... 26 earlier steps hidden" || text["text_size"] != "notation" || text["text_color"] != "grey" {
		t.Fatalf("truncated hint style/content = %+v", text)
	}
	if _, hasIcon := elements[0]["icon"]; hasIcon {
		t.Fatalf("truncated hint should not add a reasoning icon: %+v", elements[0])
	}
}

func TestTimelineCard_BusinessStepsArePrimaryAndTechnicalWorkIsFlatFold(t *testing.T) {
	payload := &core.ProgressCardPayload{
		Style: "timeline", Lang: "zh", State: core.ProgressCardStateRunning,
		StartedAt:            time.Now().Add(-45 * time.Second).Format(time.RFC3339),
		LastActivityAt:       time.Now().Add(-3 * time.Second).Format(time.RFC3339),
		CurrentStepStartedAt: time.Now().Add(-20 * time.Second).Format(time.RFC3339),
		LatestActivity:       "正在核对消息更新链路",
		Steps: []core.TaskProgressStep{
			{Title: "梳理现有卡片", Status: "completed"},
			{Title: "实现业务步骤主视图", Status: "in_progress"},
			{Title: "验证最终样式", Status: "pending"},
		},
		TotalEntries: 2,
		Items: []core.ProgressCardEntry{
			{Kind: core.ProgressEntryThinking, Text: "检查事件路由"},
			{Kind: core.ProgressEntryToolUse, Tool: "Read", Text: "progress_timeline.go", Status: "completed"},
		},
	}
	elements := timelineCardElements(t, payload)
	if len(elements) != 6 {
		t.Fatalf("business card elements = %d, want 6: %+v", len(elements), elements)
	}
	for i, wantBackground := range []string{"default", "blue-50", "default"} {
		row := elements[map[int]int{0: 0, 1: 1, 2: 3}[i]]
		if row["tag"] != "column_set" || row["background_style"] != wantBackground {
			t.Fatalf("step %d hierarchy = %+v", i, row)
		}
	}
	if hint := elements[2]; hint["tag"] != "markdown" || !strings.Contains(fmt.Sprint(hint["content"]), "正在核对消息更新链路") {
		t.Fatalf("active-step hint = %+v", hint)
	}
	panel := elements[4]
	if panel["tag"] != "collapsible_panel" || panel["expanded"] != false {
		t.Fatalf("technical work is not collapsed: %+v", panel)
	}
	if title := panel["header"].(map[string]any)["title"].(map[string]any)["content"]; title != "技术执行明细（2 条更新） ▸" {
		t.Fatalf("technical title = %v", title)
	}
	children := panel["elements"].([]any)
	for _, child := range children {
		if child.(map[string]any)["tag"] == "collapsible_panel" {
			t.Fatalf("technical fold contains a nested panel: %+v", children)
		}
	}
	footer := elements[5]["text"].(map[string]any)["content"].(string)
	if !strings.Contains(footer, "已用") || !strings.Contains(footer, "本步") || !strings.Contains(footer, "最近活动") {
		t.Fatalf("business timing footer = %q", footer)
	}
}

func TestTimelineCard_UnfinishedBusinessStepsRemainVisibleAtCompletion(t *testing.T) {
	payload := &core.ProgressCardPayload{
		Style: "timeline", Lang: "zh", State: core.ProgressCardStateCompleted,
		StartedAt: time.Now().Add(-time.Minute).Format(time.RFC3339),
		Steps: []core.TaskProgressStep{
			{Title: "已核验数据", Status: "completed"},
			{Title: "等待补充来源", Status: "in_progress"},
			{Title: "交付最终结论", Status: "pending"},
		},
	}
	raw := buildTimelineCard(payload)
	var card struct {
		Header struct {
			Title    struct{ Content string }
			Template string
		}
		Body struct{ Elements []map[string]any }
	}
	if err := json.Unmarshal([]byte(raw), &card); err != nil {
		t.Fatal(err)
	}
	if card.Header.Template != "orange" || card.Header.Title.Content != "本轮回复结束 · 步骤尚未全部完成" {
		t.Fatalf("unfinished terminal header = %+v", card.Header)
	}
	if strings.Contains(raw, "blue-50") || strings.Count(raw, "本轮未完成") != 2 {
		t.Fatalf("terminal steps imply live or completed work: %s", raw)
	}
	if len(card.Body.Elements) < 5 || card.Body.Elements[0]["tag"] != "column_set" {
		t.Fatalf("business steps disappeared at completion: %+v", card.Body.Elements)
	}

	for i := range payload.Steps {
		payload.Steps[i].Status = "completed"
	}
	raw = buildTimelineCard(payload)
	if !strings.Contains(raw, `"template":"green"`) || strings.Contains(raw, "步骤尚未全部完成") {
		t.Fatalf("fully completed plan did not return to green: %s", raw)
	}
}

func TestTimelineCard_BusinessStepTextCannotInjectCardMarkup(t *testing.T) {
	payload := &core.ProgressCardPayload{
		Style: "timeline", Lang: "zh", State: core.ProgressCardStateRunning,
		Steps: []core.TaskProgressStep{{Title: "<at id=ou_secret>all</at> **伪装**", Status: "in_progress"}},
	}
	raw := buildTimelineCard(payload)
	if strings.Contains(raw, "<at id=") || !strings.Contains(raw, "\\u0026lt;at id=ou\\\\_secret") {
		t.Fatalf("business step markup was not escaped: %s", raw)
	}
}
