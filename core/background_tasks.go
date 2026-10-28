package core

// BackgroundTaskReporter is an optional capability for agent sessions that
// expose work continuing after a foreground turn has produced an interim
// result. The engine uses it only to keep that turn open until the agent's
// background work has finished; sessions without this capability retain the
// existing EventResult behavior.
type BackgroundTaskReporter interface {
	HasPendingBackgroundTasks() bool
}
