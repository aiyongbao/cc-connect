package claudecode

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/chenhg5/cc-connect/core"
)

const planningSettingsPrefix = "cc-connect-plan-settings-"
const planningApplyPrefix = "cc-connect-plan-apply-"

// A policy failure is a prerequisite failure, never evidence that the saved
// conversation is invalid. Do not send a user turn until the CLI confirms both
// hooks are present in its actual effective settings.
func (cs *claudeSession) verifyPlanningSettings() error {
	if err := cs.planGuard.CheckHookPolicy(); err != nil {
		return &core.BusinessPlanSetupError{Err: err}
	}
	if cs.planningOverlay != nil && !cs.planningOverlayApplied.Load() {
		if err := cs.requestPlanningSettings(planningApplyPrefix, map[string]any{
			"subtype": "apply_flag_settings", "settings": cs.planningOverlay,
		}); err != nil {
			return err
		}
		cs.planningOverlayApplied.Store(true)
	}
	return cs.requestPlanningSettings(planningSettingsPrefix, map[string]any{"subtype": "get_settings"})
}

func (cs *claudeSession) requestPlanningSettings(prefix string, request map[string]any) error {
	ctx, cancel := context.WithTimeout(cs.ctx, 5*time.Second)
	defer cancel()
	cs.planningMu.Lock()
	cs.planningSequence++
	id := fmt.Sprintf("%s%d", prefix, cs.planningSequence)
	result := make(chan error, 1)
	if cs.planningWaiters == nil {
		cs.planningWaiters = map[string]chan error{}
	}
	cs.planningWaiters[id] = result
	cs.planningMu.Unlock()
	defer func() { cs.planningMu.Lock(); delete(cs.planningWaiters, id); cs.planningMu.Unlock() }()
	if err := cs.writeJSON(map[string]any{"type": "control_request", "request_id": id, "request": request}); err != nil {
		return &core.BusinessPlanSetupError{Err: fmt.Errorf("cannot verify active planning hooks")}
	}
	select {
	case err := <-result:
		if err != nil {
			return &core.BusinessPlanSetupError{Err: err}
		}
		return nil
	case <-ctx.Done():
		return &core.BusinessPlanSetupError{Err: fmt.Errorf("CLI did not verify active planning hooks; work has not started")}
	}
}

func (cs *claudeSession) handlePlanningSettings(raw map[string]any) bool {
	response, _ := raw["response"].(map[string]any)
	id, _ := response["request_id"].(string)
	applying := strings.HasPrefix(id, planningApplyPrefix)
	if !applying && !strings.HasPrefix(id, planningSettingsPrefix) {
		return false
	}
	cs.planningMu.Lock()
	waiter := cs.planningWaiters[id]
	cs.planningMu.Unlock()
	if waiter == nil {
		return true
	}
	err := fmt.Errorf("CLI cannot verify active planning hooks; work has not started")
	if applying {
		if response["subtype"] == "success" {
			err = nil
		} else {
			err = fmt.Errorf("CLI cannot install the planning hooks without replacing the original settings file")
		}
	} else if response["subtype"] == "success" {
		payload, _ := response["response"].(map[string]any)
		if settings, ok := payload["effective"].(map[string]any); ok {
			data, marshalErr := json.Marshal(settings)
			if marshalErr == nil {
				err = cs.planGuard.CheckClaudeSettings(data)
			}
		}
	}
	select {
	case waiter <- err:
	default:
	}
	return true
}
