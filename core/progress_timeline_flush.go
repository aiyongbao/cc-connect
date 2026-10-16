package core

import (
	"context"
	"encoding/json"
	"log/slog"
	"time"
)

// flush snapshots the latest state only after taking the send lock. Platform
// requests never hold mu, allowing stop to signal immediately under engine
// locks; sendMu ensures the terminal card follows any in-flight running card.
func (w *timelineProgress) flush(force bool) {
	w.sendMu.Lock()
	defer w.sendMu.Unlock()

	w.mu.Lock()
	if !force && (w.payload.State != ProgressCardStateRunning || w.ctx.Err() != nil) {
		w.mu.Unlock()
		return
	}
	if elapsed := time.Since(w.lastUpdate); !force && elapsed < w.interval {
		w.scheduleFlushLocked(w.interval - elapsed)
		w.mu.Unlock()
		return
	}
	w.cancelFlushLocked()
	// Stamp the snapshot sent to the platform, leaving the stored progress
	// untouched so a failed update cannot make an old card look current.
	snapshot := w.payload
	snapshot.UpdatedAt = time.Now().Format(time.RFC3339)
	b, err := json.Marshal(snapshot)
	w.mu.Unlock()
	if err != nil {
		slog.Error("timeline: encode progress", "error", err)
		return
	}

	content := ProgressCardPayloadPrefix + string(b)
	ctx, cancel := context.WithTimeout(w.ctx, compactProgressAPITimeout)
	defer cancel()
	if w.handle == nil {
		starter, ok := w.p.(PreviewStarter)
		if !ok {
			slog.Warn("timeline: platform lacks PreviewStarter")
			return
		}
		w.handle, err = starter.SendPreviewStart(ctx, w.replyCtx, content)
	} else if updater, ok := w.p.(MessageUpdater); ok {
		err = updater.UpdateMessage(ctx, w.handle, content)
	}
	if err != nil {
		slog.Warn("timeline: update progress", "error", err)
		return
	}
	w.lastUpdate = time.Now()
}

// scheduleFlushLocked coalesces throttled updates. A cancelled callback must not
// clear a newer timer or send a running update after the segment is sealed.
func (w *timelineProgress) scheduleFlushLocked(delay time.Duration) {
	if w.flushTimer != nil {
		return
	}
	w.flushSeq++
	seq := w.flushSeq
	w.flushTimer = time.AfterFunc(delay, func() {
		w.mu.Lock()
		if seq != w.flushSeq {
			w.mu.Unlock()
			return
		}
		w.flushTimer = nil
		active := w.payload.State == ProgressCardStateRunning && w.ctx.Err() == nil
		w.mu.Unlock()
		if active {
			w.flush(false)
		}
	})
}
