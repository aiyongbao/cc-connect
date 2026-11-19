package planguard

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"

	"github.com/chenhg5/cc-connect/core"
)

// CheckHookPolicy inspects the user's original policy, not the session-local
// settings used to keep the guard itself callable during a CLI hot reload.
func CheckHookPolicy(data []byte) error {
	var settings struct {
		Disabled    bool `json:"disableAllHooks"`
		ManagedOnly bool `json:"allowManagedHooksOnly"`
	}
	if err := json.Unmarshal(core.StripJSONC(data), &settings); err != nil {
		return fmt.Errorf("cannot parse planning hook policy; work is paused")
	}
	if settings.Disabled || settings.ManagedOnly {
		return fmt.Errorf("business planning requires execution hooks, but the existing settings disable them; work has not started")
	}
	return nil
}

func CheckHookPolicyFiles(paths []string) error {
	for _, path := range paths {
		data, err := os.ReadFile(path)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return fmt.Errorf("cannot verify planning hook policy; work is paused")
		}
		if err := CheckHookPolicy(data); err != nil {
			return err
		}
	}
	return nil
}

func (g *Guard) SetHookPolicyFiles(paths []string) error {
	return withState(g.Path, func(s *state) error {
		s.HookPolicyFiles = append([]string(nil), paths...)
		return nil
	})
}

func (g *Guard) CheckHookPolicy() error {
	s, err := readState(g.Path)
	if err != nil {
		return fmt.Errorf("cannot verify planning hook policy; work is paused")
	}
	return checkHookPolicyState(s)
}

// CheckClaudeSettings consumes only the necessary parts of get_settings; the
// full response may contain credentials and must never be logged or persisted.
func (g *Guard) CheckClaudeSettings(data []byte) error {
	if err := g.CheckHookPolicy(); err != nil {
		return err
	}
	if err := CheckHookPolicy(data); err != nil {
		return err
	}
	var settings struct {
		Hooks map[string][]struct {
			Matcher string `json:"matcher"`
			Hooks   []struct {
				Type    string `json:"type"`
				Command string `json:"command"`
			} `json:"hooks"`
		} `json:"hooks"`
	}
	if json.Unmarshal(data, &settings) != nil {
		return fmt.Errorf("cannot verify active planning hooks")
	}
	for _, event := range []string{"PreToolUse", "PostToolUse"} {
		found := false
		for _, group := range settings.Hooks[event] {
			if group.Matcher != ".*" {
				continue
			}
			for _, hook := range group.Hooks {
				if hook.Type == "command" && hook.Command == g.hookCommand() {
					found = true
				}
			}
		}
		if !found {
			return fmt.Errorf("CLI did not activate the planning barrier; work has not started")
		}
	}
	return nil
}

// Source settings are merged into a private file. If an explicit source file
// changes, stop using its old permission snapshot. The engine reconnects the
// next turn using the saved conversation ID and merges the new settings.
func (g *Guard) SetSettingsSource(path string, settings []byte) error {
	if path == "" {
		return nil
	}
	return withState(g.Path, func(s *state) error {
		s.SettingsSource = path
		s.SettingsHash = fmt.Sprintf("%x", sha256.Sum256(settings))
		return nil
	})
}
func settingsSourceChanged(s *state) bool {
	if s.SettingsSource == "" {
		return false
	}
	data, err := os.ReadFile(s.SettingsSource)
	if err != nil {
		return true
	}
	var settings map[string]any
	if json.Unmarshal(core.StripJSONC(data), &settings) != nil || settings == nil {
		return true
	}
	normalized, err := json.Marshal(settings)
	return err != nil || fmt.Sprintf("%x", sha256.Sum256(normalized)) != s.SettingsHash
}
func checkHookPolicyState(s *state) error {
	if err := CheckHookPolicyFiles(s.HookPolicyFiles); err != nil {
		return err
	}
	if settingsSourceChanged(s) {
		return fmt.Errorf("CLI settings changed; reconnect the saved conversation before work")
	}
	return nil
}
func (g *Guard) SettingsSourceChanged() bool {
	s, err := readState(g.Path)
	return err != nil || settingsSourceChanged(s)
}
