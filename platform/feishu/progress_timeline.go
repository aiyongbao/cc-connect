package feishu

import (
	"encoding/json"
	"strings"
	"time"

	"github.com/chenhg5/cc-connect/core"
)

// SupportsTimelineProgress limits timeline to interactive cards; text-only
// deployments continue to use their established rendering path.
func (p *Platform) SupportsTimelineProgress() bool { return p.useInteractiveCard }

// timelineSegment retains group boundaries while sanitizing the entire card.
type timelineSegment struct {
	kind  core.ProgressCardEntryKind
	texts []string
}

// Native outline icons follow Feishu's light/dark themes without colorful emoji.
const (
	timelineThinkingIcon = "efficiency_outlined"
	timelineToolIcon     = "setting_outlined"
)

// buildTimelineCard renders ordered progress blocks without exposing raw tool
// output. Tool calls always start collapsed, including a single call, so progress
// stays compact while preserving each call's status and short input on demand.
func buildTimelineCard(payload *core.ProgressCardPayload) string {
	i18n := core.NewI18n(core.Language(payload.Lang))
	titleKey, color := core.MsgTimelineRunning, "blue"
	switch payload.State {
	case core.ProgressCardStateCompleted:
		titleKey, color = core.MsgTimelineCompleted, "green"
	case core.ProgressCardStatePaused:
		titleKey, color = core.MsgTimelinePaused, "orange"
	case core.ProgressCardStateInterrupted:
		titleKey, color = core.MsgTimelineInterrupted, "orange"
	case core.ProgressCardStateFailed:
		titleKey, color = core.MsgTimelineFailed, "red"
	}
	segments := timelineCardSegments(payload, i18n)
	// 终态若只剩内部占位行（本次执行从未产生思考或工具进度），隐藏过程内容：
	// 头部的终态标题已表达结果，避免渲染只有占位文本的空“执行过程”面板。
	if timelineIsTerminalWithoutProgress(payload, i18n) {
		segments = nil
	}
	// Sanitize all blocks together so tables share the same per-card budget.
	// Keep the segment metadata alongside the sanitized content to preserve the
	// exact position of every collapsed tool group among reasoning blocks.
	var texts []string
	for _, segment := range segments {
		texts = append(texts, segment.texts...)
	}
	texts = sanitizeCardMarkdownSegmentsForCard(texts)
	elements := make([]map[string]any, 0, len(segments))
	offset := 0
	for _, segment := range segments {
		rows := make([]map[string]any, 0, len(segment.texts))
		for _, content := range texts[offset : offset+len(segment.texts)] {
			rows = append(rows, timelineMarkdown(content, segment.kind))
		}
		offset += len(segment.texts)
		if segment.kind == core.ProgressEntryToolUse {
			// The native header icon is reserved for the tool kind. A separate
			// text cue keeps the collapsed group visibly clickable as well.
			panel := buildProgressPanel(i18n.Tf(core.MsgTimelineToolGroup, len(rows))+" ▸", false, rows)
			header := panel["header"].(map[string]any)
			header["icon"] = timelineIcon(timelineToolIcon)
			header["icon_position"] = "left"
			elements = append(elements, panel)
		} else {
			elements = append(elements, rows...)
		}
	}
	if payload.State == core.ProgressCardStateCompleted && len(elements) > 0 {
		// Completed turns already have a separate final reply. Keep the
		// historical progress available without letting it dominate the chat.
		title := i18n.Tf(core.MsgTimelineDetails, len(payload.Items)) + " ▸"
		panel := buildProgressPanel(title, false, elements)
		// Leave the outer panel on Feishu's theme-aware default background.
		// Inner tool groups retain their existing neutral grey treatment.
		delete(panel, "background_color")
		elements = []map[string]any{panel}
	}
	card := map[string]any{
		"schema": "2.0", "config": map[string]any{"wide_screen_mode": true},
		"header": map[string]any{"title": timelineHeaderTitle(i18n, titleKey), "template": color, "text_tag_list": timelineHeaderClockTags(payload.UpdatedAt)},
		"body":   map[string]any{"elements": elements},
	}
	b, _ := json.Marshal(card)
	return string(b)
}

// timelineIsTerminalWithoutProgress reports whether a finished payload still
// carries only the internal placeholder row that finishNoProgressLocked keeps
// for adapters that never emitted progress events. The header already shows
// the terminal state, so an empty body reads better than a panel whose only
// content is "no details available".
func timelineIsTerminalWithoutProgress(payload *core.ProgressCardPayload, i18n *core.I18n) bool {
	switch payload.State {
	case core.ProgressCardStateCompleted, core.ProgressCardStateInterrupted, core.ProgressCardStateFailed:
	default:
		return false
	}
	return len(payload.Items) == 1 &&
		payload.Items[0].Kind == core.ProgressEntryInfo &&
		payload.Items[0].Text == i18n.T(core.MsgTimelineNoProgress)
}

// timelineHeaderTitle keeps the state label as plain text; the clock moves to
// the header's text_tag_list so Feishu renders it as a small metadata badge.
func timelineHeaderTitle(i18n *core.I18n, titleKey core.MsgKey) map[string]any {
	return map[string]any{"tag": "plain_text", "content": i18n.T(titleKey)}
}

// timelineHeaderClockTags renders the last-update clock as a small neutral
// badge beside the state title. Feishu shows it in the reader's timezone and
// keeps the body focused on progress content.
func timelineHeaderClockTags(updatedAt string) []map[string]any {
	t, err := time.Parse(time.RFC3339, updatedAt)
	if err != nil {
		return nil
	}
	// text_tag_list renders plain text only; the dynamic local_datetime tag is
	// a body-only markdown component, so format the clock server-side.
	clock := t.Format("15:04:05")
	return []map[string]any{{
		"tag":   "text_tag",
		"text":  map[string]any{"tag": "plain_text", "content": clock},
		"color": "neutral",
	}}
}

// timelineCardSegments groups only adjacent entries of the same kind, so tool
// summaries never cross a reasoning or informational update in the timeline.
func timelineCardSegments(payload *core.ProgressCardPayload, i18n *core.I18n) []timelineSegment {
	segments := make([]timelineSegment, 0, len(payload.Items)+1)
	if payload.Truncated {
		segments = append(segments, timelineSegment{texts: []string{i18n.T(core.MsgTimelineTruncated)}})
	}
	var groupKind core.ProgressCardEntryKind
	var group []string
	flush := func() {
		if len(group) == 0 {
			return
		}
		texts := group
		if groupKind != core.ProgressEntryToolUse {
			texts = []string{strings.Join(group, "\n\n")}
		}
		segments = append(segments, timelineSegment{kind: groupKind, texts: texts})
		group = nil
	}
	for _, item := range payload.Items {
		kind := item.Kind
		if kind != core.ProgressEntryThinking && kind != core.ProgressEntryToolUse {
			kind = core.ProgressEntryInfo
		}
		if groupKind != kind {
			flush()
			groupKind = kind
		}
		switch kind {
		case core.ProgressEntryThinking:
			group = append(group, item.Text)
		case core.ProgressEntryToolUse:
			status := core.MsgTimelineToolRunning
			if item.Status == "completed" {
				status = core.MsgTimelineToolCompleted
			}
			if item.Status == "failed" {
				status = core.MsgTimelineToolFailed
			}
			name := strings.TrimSpace(item.Tool)
			if name == "" {
				name = "Tool"
			}
			line := "**" + escapeTimelineMarkdown(name) + "**"
			if item.Text != "" && item.Text != name {
				line += " · " + escapeTimelineMarkdown(item.Text)
			}
			group = append(group, line+" · <font color='grey'>"+i18n.T(status)+"</font>")
		default:
			group = append(group, escapeTimelineMarkdown(item.Text))
		}
	}
	flush()
	return segments
}

// timelineMarkdown adds one stable kind icon per merged thought or tool row.
func timelineMarkdown(content string, kind core.ProgressCardEntryKind) map[string]any {
	element := map[string]any{"tag": "markdown", "content": content}
	switch kind {
	case core.ProgressEntryThinking:
		element["icon"] = timelineIcon(timelineThinkingIcon)
	case core.ProgressEntryToolUse:
		element["icon"] = timelineIcon(timelineToolIcon)
	}
	return element
}

func timelineIcon(token string) map[string]any {
	return map[string]any{"tag": "standard_icon", "token": token, "color": "grey"}
}

func escapeTimelineMarkdown(s string) string {
	s = strings.ReplaceAll(s, "\n", " ")
	s = strings.ReplaceAll(s, "`", "\\`")
	s = strings.ReplaceAll(s, "*", "\\*")
	return s
}
