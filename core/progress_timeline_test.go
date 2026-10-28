package core

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"
)

type timelineTestPlatform struct {
	cujStreamingPlatform
	style string
}

func (p *timelineTestPlatform) ProgressStyle() string                 { return p.style }
func (p *timelineTestPlatform) SupportsProgressCardPayload() bool     { return true }
func (p *timelineTestPlatform) SupportsTimelineProgress() bool        { return true }
func (p *timelineTestPlatform) ProgressUpdateInterval() time.Duration { return 0 }

func timelineTestState() (*interactiveState, *timelineTestPlatform) {
	p := &timelineTestPlatform{style: "timeline"}
	return &interactiveState{platform: p, replyCtx: "ctx"}, p
}

func lastTimelinePayload(t *testing.T, p *timelineTestPlatform) *ProgressCardPayload {
	t.Helper()
	// The latest content may be an open (a segment whose first card just
	// rendered) or an update; only the unified order reflects real timing.
	content := p.getLatestCardContent()
	payload, ok := ParseProgressCardPayload(content)
	if !ok {
		t.Fatalf("invalid timeline payload: %q", content)
	}
	return payload
}

func TestTimelineProgress_AdjacentEventsAndSimpleToolStatus(t *testing.T) {
	state, p := timelineTestState()
	w := newTimelineProgressLocked(context.Background(), state, "test:u", "codex", LangChinese, nil, "legacy")
	w.Start()
	t.Cleanup(func() { w.Finalize(ProgressCardStateCompleted) })
	display := DisplayCfg{ThinkingMessages: true, ToolMessages: true}
	for _, event := range []Event{
		{Type: EventThinking, Content: "plan one"}, {Type: EventThinking, Content: "plan two"},
		{Type: EventToolUse, ToolName: "Bash", ToolInput: `{"command":"pwd"}`},
		{Type: EventToolUse, ToolName: "Read", ToolInput: `{"file_path":"/tmp/a"}`},
		{Type: EventThinking, Content: "review"},
		{Type: EventToolResult, ToolName: "Bash", ToolResult: "SECRET FULL OUTPUT"},
		{Type: EventToolResult, ToolName: "Read", ToolStatus: "failed"},
	} {
		w.Handle(event, display)
	}
	payload := lastTimelinePayload(t, p)
	if _, err := time.Parse(time.RFC3339, payload.UpdatedAt); err != nil {
		t.Fatalf("timeline card has no valid update time: %q", payload.UpdatedAt)
	}
	if len(payload.Items) != 5 || payload.Items[0].Text != "plan one" || payload.Items[1].Text != "plan two" || payload.Items[4].Text != "review" {
		t.Fatalf("events were reordered or merged across types: %+v", payload.Items)
	}
	if payload.Items[2].Text != "pwd" || payload.Items[2].Status != "completed" || payload.Items[3].Status != "failed" {
		t.Fatalf("tool summaries/status: %+v", payload.Items)
	}
	for _, update := range p.getPreviewUpdates() {
		if strings.Contains(update.Content, "SECRET FULL OUTPUT") {
			t.Fatal("full tool output leaked")
		}
	}
}

func TestTimelineProgress_BackgroundMarksOnlyLaterEntries(t *testing.T) {
	state, p := timelineTestState()
	w := newTimelineProgressLocked(context.Background(), state, "test:u", "claudecode", LangChinese, nil, "timeline")
	w.Start()
	display := DisplayCfg{ThinkingMessages: true, ToolMessages: true}
	w.Handle(Event{Type: EventThinking, Content: "foreground thought"}, display)
	w.SetBackground(true)
	w.Handle(Event{Type: EventThinking, Content: "background thought"}, display)
	w.Handle(Event{Type: EventToolUse, ToolName: "Bash", ToolInput: "pwd"}, display)

	payload := lastTimelinePayload(t, p)
	if !payload.Background {
		t.Fatal("timeline payload is not marked as background")
	}
	if len(payload.Items) != 3 {
		t.Fatalf("items = %+v, want foreground plus two background entries", payload.Items)
	}
	if payload.Items[0].Background {
		t.Fatal("foreground thought was marked as background")
	}
	if !payload.Items[1].Background || !payload.Items[2].Background {
		t.Fatalf("background entries were not marked: %+v", payload.Items)
	}
}

// A quiet period after a burst must still show its last event without waiting
// for another tool or the final result to trigger a flush.
func TestTimelineProgress_ThrottledUpdatesFlushAndStaySealed(t *testing.T) {
	state, p := timelineTestState()
	w := newTimelineProgressLocked(context.Background(), state, "test:u", "codex", LangEnglish, nil, "timeline")
	w.interval = 20 * time.Millisecond
	w.Start()
	t.Cleanup(func() { w.Finalize(ProgressCardStateCompleted) })
	w.Handle(Event{Type: EventThinking, Content: "last thought in burst"}, DisplayCfg{ThinkingMessages: true})
	deadline := time.After(time.Second)
	tick := time.NewTicker(5 * time.Millisecond)
	defer tick.Stop()
	// The first real entry opens the card (SendPreviewStart); throttle-driven
	// later entries arrive as updates. Both channels carry card content.
	for len(p.getPreviewOpens())+len(p.getPreviewUpdates()) == 0 {
		select {
		case <-deadline:
			t.Fatal("throttled update never flushed")
		case <-tick.C:
		}
	}
	if !strings.Contains(p.getPreviewOpens()[0], "last thought in burst") {
		t.Fatal("first card lost progress")
	}
	w.Handle(Event{Type: EventThinking, Content: "before choice"}, DisplayCfg{ThinkingMessages: true})
	w.Finalize(ProgressCardStatePaused)
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.flushTimer != nil {
		t.Fatal("sealed card still has a scheduled update")
	}
}

func TestTimelineProgress_GenericCardsDoNotOptIn(t *testing.T) {
	p := &cujStreamingPlatform{}
	state := &interactiveState{platform: p}
	if newTimelineProgressLocked(context.Background(), state, "test:u", "codex", LangEnglish, nil, "timeline") != nil {
		t.Fatal("generic streaming platform opted into timeline")
	}
}

// Title animation follows successful card writes, including coalescing: queued
// events do not advance the visible phase and terminal cards contain no dots.
func TestTimelineProgress_TitleDotsFollowCardWrites(t *testing.T) {
	for _, terminal := range []ProgressCardState{ProgressCardStateCompleted, ProgressCardStatePaused, ProgressCardStateInterrupted, ProgressCardStateFailed} {
		t.Run(string(terminal), func(t *testing.T) {
			state, p := timelineTestState()
			w := newTimelineProgressLocked(context.Background(), state, "test:u", "codex", LangChinese, nil, "timeline")
			t.Cleanup(func() { w.Finalize(ProgressCardStateCompleted) })
			w.Start()
			if len(p.getPreviewOpens()) != 0 {
				t.Fatal("animation opened a card without progress")
			}
			for i, want := range []int{1, 2, 3, 1} {
				w.Handle(Event{Type: EventThinking, Content: fmt.Sprintf("update %d", i)}, DisplayCfg{ThinkingMessages: true})
				if got := lastTimelinePayload(t, p).TitleDots; got != want {
					t.Fatalf("update %d dots = %d, want %d", i, got, want)
				}
			}
			// Freeze the normal write path to check that coalesced events
			// cannot consume animation phases before their trailing flush.
			w.interval = time.Hour
			before := len(p.getPreviewUpdates())
			w.Handle(Event{Type: EventThinking, Content: "coalesced update"}, DisplayCfg{ThinkingMessages: true})
			if len(p.getPreviewUpdates()) != before {
				t.Fatal("animation bypassed the existing throttle")
			}
			w.flush(true)
			if got := lastTimelinePayload(t, p).TitleDots; got != 2 {
				t.Fatalf("coalesced flush dots = %d, want 2", got)
			}
			w.Finalize(terminal)
			payload := lastTimelinePayload(t, p)
			if payload.State != terminal || payload.TitleDots != 0 {
				t.Fatalf("terminal payload retained animation: %+v", payload)
			}
		})
	}
}

// Reject one card update to verify retries use the next visible phase rather
// than silently advancing the animation for a write the user never received.
type timelineTitleRetryPlatform struct {
	*timelineTestPlatform
	rejectNext bool
}

func (p *timelineTitleRetryPlatform) UpdateMessage(ctx context.Context, handle any, content string) error {
	if p.rejectNext {
		p.rejectNext = false
		return fmt.Errorf("temporary card update failure")
	}
	return p.timelineTestPlatform.UpdateMessage(ctx, handle, content)
}

func TestTimelineProgress_TitleDotsRetryAfterFailedWrite(t *testing.T) {
	state, p := timelineTestState()
	retry := &timelineTitleRetryPlatform{timelineTestPlatform: p, rejectNext: true}
	state.platform = retry
	w := newTimelineProgressLocked(context.Background(), state, "test:u", "codex", LangChinese, nil, "timeline")
	t.Cleanup(func() { w.Finalize(ProgressCardStateCompleted) })
	w.Handle(Event{Type: EventThinking, Content: "first"}, DisplayCfg{ThinkingMessages: true})
	w.Handle(Event{Type: EventThinking, Content: "failed update"}, DisplayCfg{ThinkingMessages: true})
	if got := lastTimelinePayload(t, p).TitleDots; got != 1 {
		t.Fatalf("failed update advanced the visible phase to %d", got)
	}
	w.Handle(Event{Type: EventThinking, Content: "retry"}, DisplayCfg{ThinkingMessages: true})
	if got := lastTimelinePayload(t, p).TitleDots; got != 2 {
		t.Fatalf("retry dots = %d, want 2", got)
	}
}

func TestTimelineProgress_SealedCardsAndOldStopTokens(t *testing.T) {
	state, p := timelineTestState()
	w := newTimelineProgressLocked(context.Background(), state, "test:u", "codex", LangChinese, nil, "timeline")
	w.Start()
	w.Finalize(ProgressCardStatePaused)
	count := len(p.getPreviewUpdates()) + len(p.getPreviewOpens())
	w.Finalize(ProgressCardStateCompleted)
	w.Handle(Event{Type: EventThinking, Content: "late"}, DisplayCfg{ThinkingMessages: true})
	if len(p.getPreviewUpdates())+len(p.getPreviewOpens()) != count || w.interrupt(w.payload.TurnID) {
		t.Fatal("sealed card changed")
	}
	next := newTimelineProgressLocked(context.Background(), state, "test:u", "codex", LangChinese, nil, "timeline")
	if next.interrupt(w.payload.TurnID) {
		t.Fatal("old button cancelled a new segment")
	}
	next.Start()
	if !next.interrupt(next.payload.TurnID) {
		t.Fatal("current stop button did not work")
	}
	next.finishInterrupt()
	next.Finalize(ProgressCardStateCompleted)
	if total := len(p.getPreviewUpdates()) + len(p.getPreviewOpens()); total != count {
		t.Fatalf("quiet segments rendered %d cards total, want %d", total, count)
	}
}

func TestTimelineProgress_BoundsLongRuns(t *testing.T) {
	state, p := timelineTestState()
	w := newTimelineProgressLocked(context.Background(), state, "test:u", "codex", LangChinese, nil, "timeline")
	for i := 0; i < 100; i++ {
		w.Handle(Event{Type: EventThinking, Content: strings.Repeat("思", 9000)}, DisplayCfg{ThinkingMessages: true})
		w.Handle(Event{Type: EventToolUse, ToolName: "Read", ToolInput: fmt.Sprint(i)}, DisplayCfg{ToolMessages: true})
	}
	w.Finalize(ProgressCardStateCompleted)
	payload := lastTimelinePayload(t, p)
	if !payload.Truncated || len(payload.Items) > 20 {
		t.Fatalf("unbounded payload: %+v", payload)
	}
	updates := p.getPreviewUpdates()
	if len(updates[len(updates)-1].Content) > 28000 {
		t.Fatal("card exceeds safe payload budget")
	}
}

// Sliding history must keep rendering new calls beyond the old 40-entry cap.
// Real thinking arriving later must remain in order and tool results must
// still update retained calls, even while the visible count stays at ten.
func TestTimelineProgress_RecentTenKeepUpdatingBeyondFortyCalls(t *testing.T) {
	state, p := timelineTestState()
	w := newTimelineProgressLocked(context.Background(), state, "test:u", "codex", LangChinese, nil, "timeline")
	t.Cleanup(func() { w.Finalize(ProgressCardStateCompleted) })
	w.Start()
	display := DisplayCfg{ToolMessages: true, ThinkingMessages: true}
	for i := 1; i <= 45; i++ {
		w.Handle(Event{Type: EventToolUse, ToolName: "Read", ToolInput: fmt.Sprintf("file-%02d", i)}, display)
		payload := lastTimelinePayload(t, p)
		wantCount := i
		if wantCount > 10 {
			wantCount = 10
		}
		if len(payload.Items) != wantCount || payload.Truncated != (i > 10) {
			t.Fatalf("call %d retained an incorrect window: %+v", i, payload)
		}
		if got := payload.Items[len(payload.Items)-1].Text; got != fmt.Sprintf("file-%02d", i) {
			t.Fatalf("call %d did not update the latest visible tool: %q", i, got)
		}
		w.Handle(Event{Type: EventToolResult, ToolName: "Read"}, display)
		if lastTimelinePayload(t, p).Items[wantCount-1].Status != "completed" {
			t.Fatalf("call %d lost its tool result after truncation", i)
		}
	}
	payload := lastTimelinePayload(t, p)
	if payload.Items[0].Text != "file-36" || len(p.getPreviewUpdates()) != 89 {
		t.Fatalf("history stopped sliding: first=%q writes=%d", payload.Items[0].Text, len(p.getPreviewUpdates()))
	}
	w.Handle(Event{Type: EventThinking, Content: "review after all calls"}, display)
	payload = lastTimelinePayload(t, p)
	last := payload.Items[len(payload.Items)-1]
	if len(payload.Items) != 11 || last.Kind != ProgressEntryThinking || last.Text != "review after all calls" {
		t.Fatalf("real thinking was hidden after the window filled: %+v", payload.Items)
	}
	w.Finalize(ProgressCardStateCompleted)
	if lastTimelinePayload(t, p).State != ProgressCardStateCompleted {
		t.Fatal("long-running progress failed to reach completed state")
	}
}

// Tool bursts must not consume the reasoning window. Count every accepted
// thinking update/tool call, including consecutive thoughts, without double-counting
// tool result/status changes or hidden/empty thinking events.
func TestTimelineProgress_IndependentWindowsAndCumulativeCount(t *testing.T) {
	state, p := timelineTestState()
	w := newTimelineProgressLocked(context.Background(), state, "test:u", "codex", LangChinese, nil, "timeline")
	t.Cleanup(func() { w.Finalize(ProgressCardStateCompleted) })
	display := DisplayCfg{ThinkingMessages: true, ToolMessages: true}
	for i := 1; i <= 12; i++ {
		w.Handle(Event{Type: EventThinking, Content: fmt.Sprintf("thought-%02d", i)}, display)
		w.Handle(Event{Type: EventToolUse, ToolName: "Read", ToolInput: fmt.Sprintf("initial-%02d", i)}, display)
		w.Handle(Event{Type: EventToolResult, ToolName: "Read"}, display)
	}
	payload := lastTimelinePayload(t, p)
	if len(payload.Items) != 20 || payload.TotalEntries != 24 || !payload.Truncated {
		t.Fatalf("independent windows/cumulative total incorrect: %+v", payload)
	}
	for i := 0; i < 10; i++ {
		if payload.Items[2*i].Text != fmt.Sprintf("thought-%02d", i+3) || payload.Items[2*i+1].Text != fmt.Sprintf("initial-%02d", i+3) {
			t.Fatalf("interleaved ordering lost at %d: %+v", i, payload.Items)
		}
	}
	for i := 1; i <= 45; i++ {
		w.Handle(Event{Type: EventToolUse, ToolName: "Read", ToolInput: fmt.Sprintf("burst-%02d", i)}, display)
		w.Handle(Event{Type: EventToolResult, ToolName: "Read"}, display)
	}
	payload = lastTimelinePayload(t, p)
	if len(payload.Items) != 20 || payload.TotalEntries != 69 {
		t.Fatalf("tool burst lost history/count: %+v", payload)
	}
	for i := 0; i < 10; i++ {
		if payload.Items[i].Text != fmt.Sprintf("thought-%02d", i+3) || payload.Items[10+i].Text != fmt.Sprintf("burst-%02d", i+36) {
			t.Fatalf("tool burst displaced reasoning or stopped rolling: %+v", payload.Items)
		}
	}
	w.Handle(Event{Type: EventThinking, Content: "review one"}, display)
	w.Handle(Event{Type: EventThinking, Content: "review two"}, display)
	w.Handle(Event{Type: EventThinking, Content: ""}, display)
	w.Handle(Event{Type: EventThinking, Content: "hidden"}, DisplayCfg{})
	w.Finalize(ProgressCardStateCompleted)
	payload = lastTimelinePayload(t, p)
	if payload.TotalEntries != 71 || len(payload.Items) != 20 {
		t.Fatalf("consecutive/hidden thoughts changed cumulative semantics: %+v", payload)
	}
	last := payload.Items[len(payload.Items)-1]
	if last.Kind != ProgressEntryThinking || last.Text != "review two" || payload.Items[len(payload.Items)-2].Text != "review one" {
		t.Fatalf("adjacent thinking lost message boundaries: %+v", payload.Items)
	}
}

func TestTimelineProgress_ExistingModesRemainDisabled(t *testing.T) {
	for _, style := range []string{"legacy", "compact", "card"} {
		state, p := timelineTestState()
		p.style = style
		if newTimelineProgressLocked(context.Background(), state, "test:u", "codex", LangEnglish, nil, "rich") != nil {
			t.Fatalf("timeline enabled for %s", style)
		}
	}
}

// Codex exec can omit some internal calls from its JSON stream. A quiet turn
// never renders a card at all: its final answer is a plain reply, so neither
// the waiting notice nor any terminal state may create card updates.
func TestTimelineProgress_NoEventStreamNeverShowsCard(t *testing.T) {
	state, p := timelineTestState()
	w := newTimelineProgressLocked(context.Background(), state, "test:u", "codex", LangChinese, nil, "timeline")
	w.noticeInterval = 20 * time.Millisecond
	t.Cleanup(func() { w.Finalize(ProgressCardStateCompleted) })
	w.Start()
	w.mu.Lock()
	w.startedAt = time.Now().Add(-time.Minute)
	w.mu.Unlock()
	time.Sleep(60 * time.Millisecond)
	if total := len(p.getPreviewOpens()) + len(p.getPreviewUpdates()); total != 0 {
		t.Fatalf("quiet turn rendered %d card updates before completion, want none", total)
	}
	w.Finalize(ProgressCardStateCompleted)
	count := len(p.getPreviewUpdates()) + len(p.getPreviewOpens())
	time.Sleep(50 * time.Millisecond)
	if len(p.getPreviewUpdates())+len(p.getPreviewOpens()) != count {
		t.Fatal("no-progress notice updated a sealed card")
	}
}

func TestTimelineProgress_RealEventCancelsNoProgressNotice(t *testing.T) {
	state, p := timelineTestState()
	w := newTimelineProgressLocked(context.Background(), state, "test:u", "codex", LangEnglish, nil, "timeline")
	w.noticeInterval = 20 * time.Millisecond
	t.Cleanup(func() { w.Finalize(ProgressCardStateCompleted) })
	w.Start()
	w.Handle(Event{Type: EventToolUse, ToolName: "Bash", ToolInput: "pwd"}, DisplayCfg{ToolMessages: true})
	time.Sleep(50 * time.Millisecond)
	payload := lastTimelinePayload(t, p)
	if len(payload.Items) != 1 || payload.Items[0].Kind != ProgressEntryToolUse || strings.Contains(payload.Items[0].Text, "Waiting") {
		t.Fatalf("notice interrupted real progress: %+v", payload.Items)
	}
	w.Finalize(ProgressCardStateCompleted)
}

// Stop and permission paths can seal a segment before any visible agent event.
// Such segments must never render a card; the final plain reply is the only
// message the user sees for a quiet turn.
func TestTimelineProgress_NoEventsNeverSendsCardForEveryTerminalPath(t *testing.T) {
	for _, state := range []ProgressCardState{ProgressCardStateCompleted, ProgressCardStatePaused, ProgressCardStateFailed, ProgressCardStateInterrupted, "stop", "button"} {
		t.Run(string(state), func(t *testing.T) {
			interactive, p := timelineTestState()
			w := newTimelineProgressLocked(context.Background(), interactive, "test:u", "codex", LangChinese, nil, "timeline")
			w.Start()
			switch state {
			case "stop":
				w.stop()
				w.finishInterrupt()
			case "button":
				w.interrupt(w.payload.TurnID)
				w.finishInterrupt()
			default:
				w.Finalize(state)
			}
			if total := len(p.getPreviewOpens()) + len(p.getPreviewUpdates()); total != 0 {
				t.Fatalf("terminal path without events rendered %d cards, want none", total)
			}
			w.mu.Lock()
			defer w.mu.Unlock()
			if w.noticeTimer != nil {
				t.Fatal("terminal path left no-progress timer active")
			}
		})
	}
}
