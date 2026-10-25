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
	if len(payload.Items) != 4 || payload.Items[0].Text != "plan one\n\nplan two" || payload.Items[3].Text != "review" {
		t.Fatalf("events were reordered or merged across types: %+v", payload.Items)
	}
	if payload.Items[1].Text != "pwd" || payload.Items[1].Status != "completed" || payload.Items[2].Status != "failed" {
		t.Fatalf("tool summaries/status: %+v", payload.Items)
	}
	for _, update := range p.getPreviewUpdates() {
		if strings.Contains(update.Content, "SECRET FULL OUTPUT") {
			t.Fatal("full tool output leaked")
		}
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
	if !payload.Truncated || len(payload.Items) > 40 {
		t.Fatalf("unbounded payload: %+v", payload)
	}
	updates := p.getPreviewUpdates()
	if len(updates[len(updates)-1].Content) > 28000 {
		t.Fatal("card exceeds safe payload budget")
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

// A quiet turn's final answer is embedded in the terminal card via
// SetFinalAnswer: the card must flush even though no progress entry arrived,
// and the answer must survive on the payload for the renderer.
func TestTimelineProgress_SetFinalAnswerFlushesTerminalCard(t *testing.T) {
	state, p := timelineTestState()
	w := newTimelineProgressLocked(context.Background(), state, "test:u", "codex", LangChinese, nil, "timeline")
	t.Cleanup(func() { w.Finalize(ProgressCardStateCompleted) })
	w.Start()
	if got := len(p.getPreviewOpens()); got != 0 {
		t.Fatalf("quiet turn rendered %d cards before completion, want 0", got)
	}
	w.Finalize(ProgressCardStateCompleted)
	if got := len(p.getPreviewOpens()); got != 0 {
		t.Fatalf("answer-less terminal card was rendered")
	}
	w.SetFinalAnswer("quiet answer body")
	if len(p.getPreviewOpens()) != 1 {
		t.Fatal("final answer did not flush the terminal card")
	}
	payload := lastTimelinePayload(t, p)
	if payload.State != ProgressCardStateCompleted || payload.FinalAnswer != "quiet answer body" {
		t.Fatalf("terminal card lost the embedded answer: %+v", payload)
	}
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
