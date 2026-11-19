package core

func interactiveSessionNeedsRefresh(session AgentSession, p Platform, replyCtx any, cardMode string) bool {
	if refresher, ok := session.(SessionRefreshReporter); ok && refresher.NeedsSessionRefresh() {
		return true
	}
	scoped, ok := session.(BusinessPlanScopeReporter)
	return ok && scoped.BusinessPlanningScoped() != timelineProgressEnabled(p, replyCtx, cardMode)
}
