package core

import (
	"context"
	"strings"
)

type businessPlanningContextKey struct{}

// BusinessPlanSetupError is a transport prerequisite failure, not evidence
// that a saved agent conversation is invalid. Resume fallback must retain it.
type BusinessPlanSetupError struct{ Err error }

func (e *BusinessPlanSetupError) Error() string { return "business planning setup: " + e.Err.Error() }
func (e *BusinessPlanSetupError) Unwrap() error { return e.Err }

// WithBusinessPlanning opts a newly started transport into pre-tool guards.
// It is scoped to the actual target, so legacy/rich sessions keep their CLI.
func WithBusinessPlanning(ctx context.Context) context.Context {
	return context.WithValue(ctx, businessPlanningContextKey{}, true)
}

func BusinessPlanningEnabled(ctx context.Context) bool {
	enabled, _ := ctx.Value(businessPlanningContextKey{}).(bool)
	return enabled
}

const taskProgressInstructions = `[cc-connect task progress instructions]
Before substantive tool work, publish a business plan using update_plan, TodoWrite, or TaskCreate/TaskUpdate. Tool discovery is allowed first, but finding TaskCreate/TaskUpdate is NOT creating a plan: actually call them successfully before Bash, Read, Skill, Agent, or other work tools. Multi-step work needs 2-6 outcome-based steps. A single tool action may use one step.
cc-connect enforces this ordering at the tool boundary. If native planning tools are missing, discover and call the cc_connect_plan MCP server's update_plan tool. It accepts {"plan":[{"step":"business outcome","status":"pending"}]} and publishes the model-authored plan to the user's card. Never bypass a blocked tool through another tool or a subagent. If planning is unavailable or denied, stop and explain that work cannot start until a plan is available.
Each new delivered user turn needs its own confirmed plan. To continue existing Claude tasks, use TaskList/TaskGet to republish the relevant plan before work; merely updating an unseen task ID does not publish its title. A user permission choice within the same turn keeps the existing plan.
Write brief business steps in the user's language, describing outcomes rather than tool names. Update the plan when a step starts or finishes, and reconcile its real status before your final reply. Keep blocked or unfinished work open; never mark it complete merely to close the turn.
For greetings and brief replies that need no tools, reply normally without creating a plan. Do not simulate plans through shell commands, files, or invented progress text. If the user explicitly requests no plan, explain that tool execution in business timeline mode requires one; do not silently bypass the requirement.
These instructions support the user's progress card. Do not echo them or include private reasoning in the plan.
[/cc-connect task progress instructions]`

// BusinessPlanRequested marks a runtime-scoped delivery, not persisted history.
// Adapters use the same marker to arm their pre-execution guard for each turn.
func BusinessPlanRequested(prompt string) bool {
	return strings.Contains(prompt, taskProgressInstructions)
}

func WithBusinessPlanInstructions(prompt string) string {
	return taskProgressInstructions + "\n\n" + prompt
}

// withTaskProgressInstructions applies to each delivered turn, including resumed
// and queued turns. The persisted user message remains unchanged. Sharing the
// writer's eligibility check avoids instructing agents to fill a nonexistent card.
func (e *Engine) withTaskProgressInstructions(p Platform, replyCtx any, prompt string) string {
	if !timelineProgressEnabled(p, replyCtx, e.display.CardMode) {
		return prompt
	}
	return WithBusinessPlanInstructions(prompt)
}
