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
	updates := p.getPreviewUpdates()
	content := ""
	if len(updates) > 0 {
		content = updates[len(updates)-1].Content
	} else {
		opens := p.getPreviewOpens()
		if len(opens) > 0 {
			content = opens[len(opens)-1]
		}
	}
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
	for len(p.getPreviewUpdates()) == 0 {
		select {
		case <-deadline:
			t.Fatal("throttled update never flushed")
		case <-tick.C:
		}
	}
	if !strings.Contains(p.getPreviewUpdates()[0].Content, "last thought in burst") {
		t.Fatal("trailing flush lost progress")
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
	count := len(p.getPreviewUpdates())
	w.Finalize(ProgressCardStateCompleted)
	w.Handle(Event{Type: EventThinking, Content: "late"}, DisplayCfg{ThinkingMessages: true})
	if len(p.getPreviewUpdates()) != count || w.interrupt(w.payload.TurnID) {
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
	if lastTimelinePayload(t, p).State != ProgressCardStateInterrupted {
		t.Fatal("completion overwrote interruption")
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
// must show elapsed time, then remove the running placeholder on completion.
func TestTimelineProgress_NoEventStreamUpdatesThenClearsWaiting(t *testing.T) {
	state, p := timelineTestState()
	w := newTimelineProgressLocked(context.Background(), state, "test:u", "codex", LangChinese, nil, "timeline")
	w.noticeInterval = 20 * time.Millisecond
	t.Cleanup(func() { w.Finalize(ProgressCardStateCompleted) })
	w.Start()
	w.mu.Lock()
	w.startedAt = time.Now().Add(-time.Minute)
	w.mu.Unlock()
	deadline := time.After(time.Second)
	for {
		if strings.Contains(lastTimelinePayload(t, p).Items[0].Text, "已用时") {
			break
		}
		select {
		case <-deadline:
			t.Fatal("quiet turn did not show an elapsed-time notice")
		case <-time.After(5 * time.Millisecond):
		}
	}
	w.Finalize(ProgressCardStateCompleted)
	completed := lastTimelinePayload(t, p)
	if completed.State != ProgressCardStateCompleted || completed.Items[0].Text != NewI18n(LangChinese).T(MsgTimelineNoProgress) {
		t.Fatalf("quiet completed card retained a running placeholder: %+v", completed)
	}
	count := len(p.getPreviewUpdates())
	time.Sleep(50 * time.Millisecond)
	if len(p.getPreviewUpdates()) != count {
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
func TestTimelineProgress_NoEventsClearsPlaceholderForEveryTerminalPath(t *testing.T) {
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
			payload := lastTimelinePayload(t, p)
			if len(payload.Items) != 1 || payload.Items[0].Text != NewI18n(LangChinese).T(MsgTimelineNoProgress) {
				t.Fatalf("terminal path retained processing text: %+v", payload)
			}
			w.mu.Lock()
			defer w.mu.Unlock()
			if w.noticeTimer != nil {
				t.Fatal("terminal path left no-progress timer active")
			}
		})
	}
}
