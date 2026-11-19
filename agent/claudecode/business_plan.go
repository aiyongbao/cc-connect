package claudecode

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Honor a user's/administrator's disabled-hook policy rather than starting a
// supposedly guarded session whose CLI would silently skip its planning hook.
func checkBusinessPlanningHooks(workDir string, env, extraArgs []string) error {
	home := claudeConfigHomeDir()
	for _, value := range env {
		if strings.HasPrefix(value, "CLAUDE_CONFIG_DIR=") {
			home = strings.TrimPrefix(value, "CLAUDE_CONFIG_DIR=")
		}
	}
	paths := []string{filepath.Join(home, "settings.json"), filepath.Join(home, "settings.local.json"), filepath.Join(workDir, ".claude", "settings.json"), filepath.Join(workDir, ".claude", "settings.local.json")}
	for _, path := range paths {
		data, err := os.ReadFile(path)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return fmt.Errorf("cannot verify planning hook policy")
		}
		if err := checkPlanningSettings(data); err != nil {
			return err
		}
	}
	for i, arg := range extraArgs {
		if arg != "--settings" || i+1 >= len(extraArgs) {
			continue
		}
		data := []byte(extraArgs[i+1])
		if !strings.HasPrefix(strings.TrimSpace(string(data)), "{") {
			var err error
			data, err = os.ReadFile(string(data))
			if err != nil {
				return fmt.Errorf("cannot verify CLI planning hook policy")
			}
		}
		if err := checkPlanningSettings(data); err != nil {
			return err
		}
	}
	return nil
}

func checkPlanningSettings(data []byte) error {
	var settings struct {
		Disabled    bool `json:"disableAllHooks"`
		ManagedOnly bool `json:"allowManagedHooksOnly"`
	}
	if err := json.Unmarshal(stripJSONC(data), &settings); err != nil {
		return fmt.Errorf("cannot parse planning hook policy")
	}
	if settings.Disabled || settings.ManagedOnly {
		return fmt.Errorf("business planning requires execution hooks, but the existing settings disable them; work has not started")
	}
	return nil
}
