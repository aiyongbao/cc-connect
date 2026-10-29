package core

import "strings"

// applyInitialMessageContext consumes the fresh-session slot for every first
// message, even if it has no background. Later messages (including queued
// follow-ups) must rely on the conversation rather than replay its root.
func applyInitialMessageContext(state *interactiveState, msg *Message) {
	state.mu.Lock()
	include := state.initialContextPending
	state.initialContextPending = false
	state.mu.Unlock()
	initial := msg.InitialContext
	msg.InitialContext = nil
	if !include || initial == nil {
		return
	}
	if initial.Text != "" {
		msg.Content = strings.TrimSpace(initial.Text + "\n" + msg.Content)
	}
	msg.Images = append(append([]ImageAttachment(nil), initial.Images...), msg.Images...)
	msg.Files = append(append([]FileAttachment(nil), initial.Files...), msg.Files...)
}
