package core

// BackgroundTaskReporter is an optional capability for agent sessions that
// expose work continuing after a foreground turn has produced an interim
// result. The engine uses it only to keep that turn open until the agent's
// background work has finished; sessions without this capability retain the
// existing EventResult behavior.
type BackgroundTaskReporter interface {
	HasPendingBackgroundTasks() bool
}

// EventMetaTurnBackstop marks a terminal EventResult that an agent synthesizes
// to close a turn whose real result was already emitted but left the turn open
// (Done=false, or Done=true while background tasks were still reported). The
// agent sends it once it knows nothing more will follow, so a waiting
// foreground turn finalizes with that content instead of hanging until the
// idle timeout. The unsolicited reader ignores it: it treats every result as
// final and has already relayed the original content.
const EventMetaTurnBackstop = "turn_backstop"
