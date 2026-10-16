package core

// stopTimelineTurn validates the card's segment identity inside the same engine
// lock used by teardown. Old cards therefore cannot cancel a later turn.
func (e *Engine) stopTimelineTurn(sessionKey, token string) {
	key := sessionKey // timeline payload carries the canonical interactive key
	e.interactiveMu.Lock()
	state := e.interactiveStates[key]
	var progress *timelineProgress
	if state != nil {
		state.mu.Lock()
		progress = state.timeline
		state.mu.Unlock()
	}
	e.interactiveMu.Unlock()
	if progress == nil {
		return
	}
	e.stopInteractiveSessionExpected(key, true, progress, token)
}
