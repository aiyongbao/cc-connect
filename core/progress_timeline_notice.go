package core

import "time"

// scheduleNoProgressNoticeLocked updates only the initial placeholder. Elapsed
// time acknowledges a quiet event stream without claiming the agent is healthy
// or fabricating tool calls. Real progress and terminal states cancel it.
func (w *timelineProgress) scheduleNoProgressNoticeLocked() {
	if w.noticeTimer != nil || !w.waitingForProgressLocked() || w.ctx.Err() != nil {
		return
	}
	w.noticeSeq++
	seq := w.noticeSeq
	w.noticeTimer = time.AfterFunc(w.noticeInterval, func() {
		w.mu.Lock()
		if seq != w.noticeSeq {
			w.mu.Unlock()
			return
		}
		w.noticeTimer = nil
		if !w.waitingForProgressLocked() || w.ctx.Err() != nil {
			w.mu.Unlock()
			return
		}
		elapsed := time.Since(w.startedAt).Truncate(time.Second).String()
		w.payload.Items[0].Text = NewI18n(Language(w.payload.Lang)).Tf(MsgTimelineWaiting, elapsed)
		w.scheduleNoProgressNoticeLocked()
		w.mu.Unlock()
		w.flush(false)
	})
}

// waitingForProgressLocked distinguishes the receipt from actual thoughts and
// tools, including progress that was intentionally hidden by display settings.
func (w *timelineProgress) waitingForProgressLocked() bool {
	return w.payload.State == ProgressCardStateRunning && len(w.payload.Steps) == 0 && len(w.payload.Items) == 1 && w.payload.Items[0].Kind == ProgressEntryInfo
}

// finishNoProgressLocked removes the running receipt even when an adapter never
// emitted progress. The card header supplies the actual terminal state.
func (w *timelineProgress) finishNoProgressLocked() {
	w.cancelNoProgressNoticeLocked()
	if len(w.payload.Steps) == 0 && len(w.payload.Items) == 1 && w.payload.Items[0].Kind == ProgressEntryInfo {
		w.payload.Items[0].Text = NewI18n(Language(w.payload.Lang)).T(MsgTimelineNoProgress)
	}
}

// cancelNoProgressNoticeLocked invalidates callbacks already waiting on mu.
func (w *timelineProgress) cancelNoProgressNoticeLocked() {
	w.noticeSeq++
	if w.noticeTimer != nil {
		w.noticeTimer.Stop()
		w.noticeTimer = nil
	}
}
