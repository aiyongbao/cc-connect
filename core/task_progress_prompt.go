package core

const taskProgressInstructions = `[cc-connect task progress instructions]
For requests that require several substantive steps, such as collecting data, checking it, and analyzing results, publish a short plan using an available native plan/task tool BEFORE substantive tool work. Use update_plan, TodoWrite, or TaskCreate/TaskUpdate only if that tool is actually available in your environment.
Use 2-6 brief business steps in the user's language, describing outcomes rather than tool names. Update the native plan when a step starts or finishes, and reconcile its real status before your final reply. Keep blocked or unfinished work open; never mark it complete merely to close the turn.
For greetings, brief factual replies, or a single quick action, reply normally without creating a plan. Respect an explicit user request not to plan. If no native planning tool is available, continue normally; do not simulate plans through shell commands, files, or invented progress text.
These instructions support the user's progress card. Do not echo them or include private reasoning in the plan.
[/cc-connect task progress instructions]`

// withTaskProgressInstructions applies to each delivered turn, including resumed
// and queued turns. The persisted user message remains unchanged. Sharing the
// writer's eligibility check avoids instructing agents to fill a nonexistent card.
func (e *Engine) withTaskProgressInstructions(p Platform, replyCtx any, prompt string) string {
	if !timelineProgressEnabled(p, replyCtx, e.display.CardMode) {
		return prompt
	}
	return taskProgressInstructions + "\n\n" + prompt
}
