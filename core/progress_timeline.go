package core

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

const (
	// Match rich cards: retain recent thoughts and tool calls independently,
	// while preserving their relative order in the timeline.
	timelineMaxEntriesPerKind = 10

	// Timeline labels are shared with platform renderers through the i18n API.
	MsgTimelineRunning          MsgKey = "timeline_running"
	MsgTimelineBackground       MsgKey = "timeline_background"
	MsgTimelineBackgroundTag    MsgKey = "timeline_background_tag"
	MsgTimelineCompleted        MsgKey = "timeline_completed"
	MsgTimelinePaused           MsgKey = "timeline_paused"
	MsgTimelineInterrupted      MsgKey = "timeline_interrupted"
	MsgTimelineFailed           MsgKey = "timeline_failed"
	MsgTimelineStop             MsgKey = "timeline_stop"
	MsgTimelineTruncated        MsgKey = "timeline_truncated"
	MsgTimelineEarlierHidden    MsgKey = "timeline_earlier_hidden"
	MsgTimelineToolGroup        MsgKey = "timeline_tool_group"
	MsgTimelineToolRunning      MsgKey = "timeline_tool_running"
	MsgTimelineToolCompleted    MsgKey = "timeline_tool_completed"
	MsgTimelineToolFailed       MsgKey = "timeline_tool_failed"
	MsgTimelineUpdatedAt        MsgKey = "timeline_updated_at"
	MsgTimelineNoProgress       MsgKey = "timeline_no_progress"
	MsgTimelineWaiting          MsgKey = "timeline_waiting"
	MsgTimelineDetails          MsgKey = "timeline_details"
	MsgTimelineOpenSteps        MsgKey = "timeline_open_steps"
	MsgTimelineStepCompleted    MsgKey = "timeline_step_completed"
	MsgTimelineStepRunning      MsgKey = "timeline_step_running"
	MsgTimelineStepPending      MsgKey = "timeline_step_pending"
	MsgTimelineStepUnfinished   MsgKey = "timeline_step_unfinished"
	MsgTimelineTechnicalDetails MsgKey = "timeline_technical_details"
	MsgTimelineElapsed          MsgKey = "timeline_elapsed"
	MsgTimelineCurrentStep      MsgKey = "timeline_current_step"
	MsgTimelineRecentActivity   MsgKey = "timeline_recent_activity"
	MsgTimelineToolReturned     MsgKey = "timeline_tool_returned"
	// Paused cards are immutable; continuation opens a new card after the prompt.
	ProgressCardStatePaused      ProgressCardState = "paused"
	ProgressCardStateInterrupted ProgressCardState = "interrupted"
)

// TimelineProgressSupport opts a platform into the ordered card schema and its
// stop callbacks. Generic progress-card support alone is insufficient.
type TimelineProgressSupport interface {
	SupportsTimelineProgress() bool
}

// timelineProgress owns one card segment. mu protects state and stop callbacks;
// sendMu serializes card I/O without delaying a stop or another session.
type timelineProgress struct {
	mu                sync.Mutex
	sendMu            sync.Mutex
	ctx               context.Context
	p                 Platform
	replyCtx          any
	transform         func(string) string
	payload           ProgressCardPayload
	handle            any       // sendMu protects the platform's preview handle
	lastUpdate        time.Time // sendMu protects the successful-send timestamp
	titleDots         int       // sendMu protects the last successfully sent running-title phase
	interval          time.Duration
	flushTimer        *time.Timer // trailing flush prevents throttling from hiding the last event
	flushSeq          uint64      // invalidates timer callbacks that lost a stop race
	noticeTimer       *time.Timer // only active until the first visible progress event
	noticeSeq         uint64
	noticeInterval    time.Duration
	heartbeatTimer    *time.Timer // refreshes elapsed/activity ages for business-step cards
	heartbeatSeq      uint64
	heartbeatInterval time.Duration
	startedAt         time.Time
	stopped           chan struct{} // both stop entrypoints close this only while holding mu
	pendingTools      []timelinePendingTool
}

type timelinePendingTool struct {
	name     string
	callID   string
	progress *TaskProgress
}

// inheritTaskProgress continues the same turn after a user decision without
// mutating the paused card or copying its technical history into the new card.
func (w *timelineProgress) inheritTaskProgress(previous *timelineProgress) {
	if w == nil || previous == nil {
		return
	}
	previous.mu.Lock()
	snapshot := previous.payload
	snapshot.Steps = append([]TaskProgressStep(nil), previous.payload.Steps...)
	pending := append([]timelinePendingTool(nil), previous.pendingTools...)
	previous.mu.Unlock()
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.payload.State != ProgressCardStateRunning {
		return
	}
	w.pendingTools = pending
	if len(snapshot.Steps) == 0 {
		return
	}
	w.payload.Steps = snapshot.Steps
	w.payload.PlanSource = snapshot.PlanSource
	w.payload.PlanUpdatedAt = snapshot.PlanUpdatedAt
	w.payload.StartedAt = snapshot.StartedAt
	w.payload.CurrentStepStartedAt = snapshot.CurrentStepStartedAt
	w.payload.LatestActivity = snapshot.LatestActivity
	w.payload.LastActivityAt = snapshot.LastActivityAt
	w.payload.Items = nil
	w.scheduleHeartbeatLocked()
}

// newTimelineProgressLocked installs a fresh segment identity while state.mu is
// held. No network I/O happens here, so session locks never wait on card creation.
func newTimelineProgressLocked(ctx context.Context, state *interactiveState, sessionKey string, agent string, lang Language, transform func(string) string, cardMode string) *timelineProgress {
	state.timeline = nil
	if !timelineProgressEnabled(state.platform, state.replyCtx, cardMode) {
		return nil
	}
	now := time.Now()
	w := &timelineProgress{
		ctx: ctx, p: state.platform, replyCtx: state.replyCtx, transform: transform,
		interval:          500 * time.Millisecond,
		noticeInterval:    30 * time.Second,
		heartbeatInterval: 10 * time.Second,
		startedAt:         now,
		stopped:           make(chan struct{}),
		payload: ProgressCardPayload{
			Version: 2, Style: progressStyleTimeline, TurnID: rand.Text(), SessionKey: sessionKey,
			Agent: normalizeProgressAgentLabel(agent), Lang: string(lang), State: ProgressCardStateRunning,
			StartedAt: now.Format(time.RFC3339), LastActivityAt: now.Format(time.RFC3339),
			Items: []ProgressCardEntry{{Kind: ProgressEntryInfo, Text: NewI18n(lang).T(MsgStarting)}},
		},
	}
	if throttle, ok := state.platform.(ProgressUpdateThrottler); ok {
		w.interval = throttle.ProgressUpdateInterval()
	}
	state.timeline = w
	return w
}

func timelineProgressEnabled(p Platform, replyCtx any, cardMode string) bool {
	capability, supported := p.(TimelineProgressSupport)
	if !supported || !capability.SupportsTimelineProgress() || !progressCardPayloadForTarget(p, replyCtx) || (cardMode != progressStyleTimeline && progressStyleForTarget(p, replyCtx) != progressStyleTimeline) {
		return false
	}
	_, preview := p.(PreviewStarter)
	_, updater := p.(MessageUpdater)
	return preview && updater
}

// Start creates the initial receipt even before agent events.
func (w *timelineProgress) Start() {
	if w == nil {
		return
	}
	w.flush(true)
	w.mu.Lock()
	if w.payload.State == ProgressCardStateRunning {
		if w.startedAt.IsZero() {
			w.startedAt = time.Now()
		}
		w.scheduleNoProgressNoticeLocked()
	}
	w.mu.Unlock()
}

// SetBackground marks a running timeline segment as waiting for agent-managed
// background work. It only changes the timeline payload; other progress
// writers do not call this method and keep their established rendering.
func (w *timelineProgress) SetBackground(background bool) {
	if w == nil {
		return
	}
	w.mu.Lock()
	if w.payload.State != ProgressCardStateRunning || w.payload.Background == background {
		w.mu.Unlock()
		return
	}
	w.payload.Background = background
	w.mu.Unlock()
	w.flush(false)
}

func timelineStopSignal(w *timelineProgress) <-chan struct{} {
	if w == nil {
		return nil
	}
	return w.stopped
}

// Handle absorbs only progress events; text and session metadata retain their
// normal engine handling. Tool results update the oldest pending matching call
// because the shared Event contract does not expose a tool-call ID.
func (w *timelineProgress) Handle(event Event, display DisplayCfg) bool {
	if w == nil {
		return false
	}
	w.mu.Lock()
	changed := false
	defer func() {
		w.mu.Unlock()
		if changed {
			w.flush(false)
		}
	}()
	if w.payload.State != ProgressCardStateRunning {
		// Shutdown seals the card before cancelling the event loop. Keep
		// owning late progress so it cannot open a legacy fallback card;
		// results and other events still need their normal engine handling.
		switch event.Type {
		case EventThinking, EventToolUse, EventToolResult, EventTaskProgress:
			return true
		}
		return false
	}
	now := time.Now()
	if event.Type == EventText {
		text := strings.TrimSpace(event.Content)
		if len(w.payload.Steps) > 0 && text != "" {
			w.payload.LatestActivity = truncateIf(strings.Join(strings.Fields(text), " "), 240)
			w.payload.LastActivityAt = now.Format(time.RFC3339)
			changed = true
		}
		return false
	}

	var entry ProgressCardEntry
	switch event.Type {
	case EventTaskProgress:
		if event.TaskProgress != nil {
			changed = w.applyTaskProgressLocked(event.TaskProgress, now)
		}
		return true
	case EventThinking:
		if !display.ThinkingMessages || isEllipsisOnly(event.Content) {
			return true
		}
		entry = ProgressCardEntry{Kind: ProgressEntryThinking, Text: truncateIf(event.Content, display.ThinkingMaxLen), Background: w.payload.Background}
		if w.transform != nil {
			entry.Text = w.transform(entry.Text)
		}
	case EventToolUse:
		name := timelineToolName(event)
		var progress *TaskProgress
		if parentID, _ := event.Metadata["parent_tool_use_id"].(string); parentID == "" {
			progress = ParseTaskProgressTool(name, taskProgressToolInput(event, name))
		}
		callID, _ := event.Metadata["tool_use_id"].(string)
		w.pendingTools = append(w.pendingTools, timelinePendingTool{name: name, callID: callID, progress: progress})
		if progress != nil {
			// The plan becomes public only after a successful result. Do not let
			// the bookkeeping call compete with the business-step hierarchy.
			return true
		}
		if len(w.payload.Steps) > 0 {
			w.payload.LatestActivity = timelineToolActivity(name, timelineToolSummary(event.ToolInput))
			w.payload.LastActivityAt = now.Format(time.RFC3339)
			changed = true
		}
		if !display.ToolMessages {
			return true
		}
		entry = ProgressCardEntry{Kind: ProgressEntryToolUse, Tool: name, Text: timelineToolSummary(event.ToolInput), Status: "running", Background: w.payload.Background}
	case EventToolResult:
		callID, _ := event.Metadata["tool_use_id"].(string)
		pending := w.takePendingToolLocked(event.ToolName, callID)
		failed := taskProgressToolFailed(event)
		if pending.progress != nil && !failed {
			changed = w.applyTaskProgressLocked(pending.progress, now) || changed
		}
		if len(w.payload.Steps) > 0 && pending.progress == nil {
			name := strings.TrimSpace(event.ToolName)
			if name == "" {
				name = pending.name
			}
			if name != "" {
				status := NewI18n(Language(w.payload.Lang)).T(MsgTimelineToolReturned)
				if failed {
					status = NewI18n(Language(w.payload.Lang)).T(MsgTimelineToolFailed)
				}
				w.payload.LatestActivity = name + " · " + status
				w.payload.LastActivityAt = now.Format(time.RFC3339)
				changed = true
			}
		}
		if !display.ToolMessages {
			return true
		}
		for i := range w.payload.Items {
			item := &w.payload.Items[i]
			if item.Kind == ProgressEntryToolUse && item.Status == "running" && (event.ToolName == "" || item.Tool == event.ToolName) {
				item.Status = "completed"
				if failed {
					item.Status = "failed"
				}
				changed = true
				break
			}
		}
		return true
	default:
		return false
	}
	entry.Text = strings.TrimSpace(entry.Text)
	if entry.Kind == ProgressEntryThinking && utf8.RuneCountInString(entry.Text) > 2500 {
		entry.Text = truncateIf(entry.Text, 2500)
		w.payload.Truncated = true
	}
	if entry.Text == "" {
		entry.Text = entry.Tool
	}
	if entry.Text == "" {
		return true
	}
	// Count accepted progress before discarding history. A tool
	// result only updates its call's status, so it does not count twice.
	w.payload.TotalEntries++
	items := w.payload.Items
	// Drop the placeholder once real progress arrives. Keep each thought as
	// its own row so rendering and retention preserve every message's icon.
	if len(items) == 1 && items[0].Kind == ProgressEntryInfo {
		items = nil
	}
	items = append(items, entry)
	// Bound both payload size and card element count during long-running work.
	if trimmed := recentTimelineEntriesByKind(items); len(trimmed) < len(items) {
		items = trimmed
		w.payload.Truncated = true
	}
	total := 0
	for _, item := range items {
		total += utf8.RuneCountInString(item.Text)
	}
	for len(items) > 1 && total > 6000 {
		total -= utf8.RuneCountInString(items[0].Text)
		items = items[1:]
		w.payload.Truncated = true
	}
	w.payload.Items = items
	w.cancelNoProgressNoticeLocked()
	changed = true
	return true
}

func taskProgressToolInput(event Event, normalizedName string) string {
	if !strings.EqualFold(event.ToolName, "MCP") {
		return event.ToolInput
	}
	first, rest, ok := strings.Cut(event.ToolInput, "\n")
	if ok && strings.EqualFold(strings.TrimSpace(first), normalizedName) {
		return rest
	}
	return event.ToolInput
}

func taskProgressToolFailed(event Event) bool {
	return event.ToolSuccess != nil && !*event.ToolSuccess ||
		event.ToolExitCode != nil && *event.ToolExitCode != 0 ||
		strings.EqualFold(event.ToolStatus, "failed") ||
		strings.EqualFold(event.ToolStatus, "error")
}

func timelineToolActivity(name, summary string) string {
	name = strings.TrimSpace(name)
	summary = strings.TrimSpace(summary)
	if summary == "" || summary == name {
		return truncateIf(name, 240)
	}
	return truncateIf(name+" · "+summary, 240)
}

func (w *timelineProgress) takePendingToolLocked(resultName, callID string) timelinePendingTool {
	if len(w.pendingTools) == 0 {
		return timelinePendingTool{}
	}
	index := 0
	if resultName = strings.TrimSpace(resultName); resultName != "" || callID != "" {
		index = -1
		for i, pending := range w.pendingTools {
			if callID != "" && pending.callID == callID || callID == "" && strings.EqualFold(pending.name, resultName) {
				index = i
				break
			}
		}
		if index < 0 {
			return timelinePendingTool{}
		}
	}
	pending := w.pendingTools[index]
	w.pendingTools = append(w.pendingTools[:index], w.pendingTools[index+1:]...)
	return pending
}

func (w *timelineProgress) applyTaskProgressLocked(progress *TaskProgress, now time.Time) bool {
	if progress == nil {
		return false
	}
	stepsChanged := !taskProgressStepsEqual(w.payload.Steps, progress.Steps)
	explanation := strings.TrimSpace(progress.Explanation)
	if !stepsChanged && explanation == "" {
		return false
	}
	before := activeTaskProgressTitles(w.payload.Steps)
	after := activeTaskProgressTitles(progress.Steps)
	if stepsChanged {
		w.payload.Steps = append([]TaskProgressStep(nil), progress.Steps...)
		w.payload.PlanSource = progress.Source
		w.payload.PlanUpdatedAt = now.Format(time.RFC3339)
		if !equalTaskProgressTitles(before, after) {
			if len(after) > 0 {
				w.payload.CurrentStepStartedAt = now.Format(time.RFC3339)
			} else {
				w.payload.CurrentStepStartedAt = ""
			}
			w.payload.LatestActivity = ""
		}
		if len(w.payload.Items) == 1 && w.payload.Items[0].Kind == ProgressEntryInfo {
			w.payload.Items = nil
		}
		w.cancelNoProgressNoticeLocked()
		if len(progress.Steps) == 0 {
			w.cancelHeartbeatLocked()
			w.payload.LatestActivity = ""
			if len(w.payload.Items) == 0 {
				w.payload.Items = []ProgressCardEntry{{Kind: ProgressEntryInfo, Text: NewI18n(Language(w.payload.Lang)).T(MsgTimelineNoProgress)}}
			}
		} else {
			w.scheduleHeartbeatLocked()
		}
	}
	if explanation != "" {
		w.payload.LatestActivity = truncateIf(strings.Join(strings.Fields(explanation), " "), 240)
	}
	w.payload.LastActivityAt = now.Format(time.RFC3339)
	return true
}

func taskProgressStepsEqual(left, right []TaskProgressStep) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}

func activeTaskProgressTitles(steps []TaskProgressStep) []string {
	titles := make([]string, 0, len(steps))
	for _, step := range steps {
		if step.Status == "in_progress" {
			titles = append(titles, step.Title)
		}
	}
	return titles
}

func equalTaskProgressTitles(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}

// Codex app_server starts MCP calls under the generic "MCP" label, but emits
// results under the actual tool name. Read its untruncated input header so an
// out-of-order result can update the matching row, including failed calls.
func timelineToolName(event Event) string {
	if !strings.EqualFold(event.ToolName, "MCP") {
		return event.ToolName
	}
	line, _, ok := strings.Cut(event.ToolInput, "\n")
	if !ok {
		return event.ToolName
	}
	line = strings.TrimSpace(line)
	if index := strings.LastIndexByte(line, ':'); index >= 0 {
		line = strings.TrimSpace(line[index+1:])
	}
	if line == "" {
		return event.ToolName
	}
	return line
}

func timelineToolSummary(input string) string {
	// Prefer useful arguments over raw JSON and keep each tool on one line.
	var args map[string]any
	if json.Unmarshal([]byte(input), &args) == nil {
		for _, key := range []string{"command", "cmd", "file_path", "path", "pattern", "query", "description"} {
			if value, ok := args[key].(string); ok && value != "" {
				input = value
				break
			}
		}
	}
	return truncateIf(strings.Join(strings.Fields(input), " "), 160)
}

// Finalize seals this segment; neither later events nor deferred cleanup may
// edit an already sealed card. A paused segment never reopens.
func (w *timelineProgress) Finalize(state ProgressCardState) {
	if w == nil {
		return
	}
	w.mu.Lock()
	if w.payload.State != ProgressCardStateRunning {
		w.mu.Unlock()
		return
	}
	w.payload.State = state
	w.cancelFlushLocked()
	w.cancelHeartbeatLocked()
	w.finishNoProgressLocked()
	if state == ProgressCardStateCompleted {
		// Some adapters omit individual results; no tool should remain shown
		// as running after the agent has completed the turn.
		for i := range w.payload.Items {
			if w.payload.Items[i].Status == "running" {
				w.payload.Items[i].Status = "completed"
			}
		}
	}
	w.mu.Unlock()
	w.flush(true)
}

// interrupt claims a running card atomically, before session teardown. Sending
// the final card happens outside engine locks via finishInterrupt.
func (w *timelineProgress) interrupt(token string) bool {
	if w == nil {
		return false
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.payload.TurnID != token || w.payload.State != ProgressCardStateRunning {
		return false
	}
	w.payload.State = ProgressCardStateInterrupted
	w.cancelFlushLocked()
	w.cancelHeartbeatLocked()
	w.finishNoProgressLocked()
	close(w.stopped)
	return true
}

func (w *timelineProgress) finishInterrupt() {
	if w != nil {
		w.flush(true)
	}
}

// stop also closes the turn signal while waiting for permission. Paused cards
// remain sealed; only the active running segment changes to interrupted.
func (w *timelineProgress) stop() {
	if w == nil {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	select {
	case <-w.stopped:
		return
	default:
		close(w.stopped)
	}
	if w.payload.State == ProgressCardStateRunning {
		w.payload.State = ProgressCardStateInterrupted
	}
	w.cancelFlushLocked()
	w.cancelHeartbeatLocked()
	w.finishNoProgressLocked()
}

// cancelFlushLocked seals the scheduled update along with its card segment.
func (w *timelineProgress) cancelFlushLocked() {
	w.flushSeq++
	if w.flushTimer != nil {
		w.flushTimer.Stop()
		w.flushTimer = nil
	}
}

func (w *timelineProgress) scheduleHeartbeatLocked() {
	if w.heartbeatTimer != nil || w.payload.State != ProgressCardStateRunning || len(w.payload.Steps) == 0 || w.ctx.Err() != nil {
		return
	}
	w.heartbeatSeq++
	seq := w.heartbeatSeq
	w.heartbeatTimer = time.AfterFunc(w.heartbeatInterval, func() {
		w.mu.Lock()
		if seq != w.heartbeatSeq {
			w.mu.Unlock()
			return
		}
		w.heartbeatTimer = nil
		active := w.payload.State == ProgressCardStateRunning && len(w.payload.Steps) > 0 && w.ctx.Err() == nil
		if active {
			w.scheduleHeartbeatLocked()
		}
		w.mu.Unlock()
		if active {
			w.flush(false)
		}
	})
}

func (w *timelineProgress) cancelHeartbeatLocked() {
	w.heartbeatSeq++
	if w.heartbeatTimer != nil {
		w.heartbeatTimer.Stop()
		w.heartbeatTimer = nil
	}
}
