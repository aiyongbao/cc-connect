package claudecode

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/chenhg5/cc-connect/agent/planguard"
)

func planningPolicyFiles(workDir string, env []string) []string {
	if absolute, err := filepath.Abs(workDir); err == nil {
		workDir = absolute
	}
	home := claudeConfigHomeDir()
	for _, value := range env {
		if strings.HasPrefix(value, "CLAUDE_CONFIG_DIR=") {
			home = strings.TrimPrefix(value, "CLAUDE_CONFIG_DIR=")
		}
	}
	if !filepath.IsAbs(home) {
		home = filepath.Join(workDir, home)
	}
	paths := []string{filepath.Join(home, "settings.json"), filepath.Join(home, "settings.local.json"), filepath.Join(workDir, ".claude", "settings.json"), filepath.Join(workDir, ".claude", "settings.local.json")}
	switch runtime.GOOS {
	case "darwin":
		paths = append(paths, "/Library/Application Support/ClaudeCode/managed-settings.json")
	case "windows":
		if dir := os.Getenv("ProgramFiles"); dir != "" {
			paths = append(paths, filepath.Join(dir, "ClaudeCode", "managed-settings.json"))
		}
	default:
		paths = append(paths, "/etc/claude-code/managed-settings.json")
	}
	return paths
}

// Claude treats --settings as a scalar (last value wins). Consume it once,
// resolving files against the CLI's working directory, then merge our hooks
// into that value instead of appending a second flag that drops user settings.
func splitPlanningSettings(workDir string, args []string) ([]string, map[string]any, string, error) {
	var clean []string
	var value string
	hasSettings := false
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--settings" {
			hasSettings = true
			if i+1 >= len(args) {
				return nil, nil, "", fmt.Errorf("missing CLI settings value")
			}
			i++
			value = args[i]
		} else if strings.HasPrefix(arg, "--settings=") {
			hasSettings = true
			value = strings.TrimPrefix(arg, "--settings=")
		} else {
			clean = append(clean, arg)
		}
	}
	if hasSettings && value == "" {
		return nil, nil, "", fmt.Errorf("empty CLI settings value")
	}
	settings := map[string]any{}
	var path string
	if value != "" {
		data := []byte(value)
		if !strings.HasPrefix(strings.TrimSpace(value), "{") {
			path = value
			if !filepath.IsAbs(path) {
				path = filepath.Join(workDir, path)
			}
			var err error
			path, err = filepath.Abs(path)
			if err != nil {
				return nil, nil, "", fmt.Errorf("cannot resolve CLI planning settings")
			}
			data, err = os.ReadFile(path)
			if err != nil {
				return nil, nil, "", fmt.Errorf("cannot read CLI planning settings")
			}
		}
		if err := json.Unmarshal(stripJSONC(data), &settings); err != nil || settings == nil {
			return nil, nil, "", fmt.Errorf("cannot parse CLI planning settings")
		}
	}
	return clean, settings, path, nil
}

func checkBusinessPlanningHooks(workDir string, env, args []string) error {
	_, settings, path, err := splitPlanningSettings(workDir, args)
	if err != nil {
		return err
	}
	paths := planningPolicyFiles(workDir, env)
	if path != "" {
		paths = append(paths, path)
	}
	if err := planguard.CheckHookPolicyFiles(paths); err != nil {
		return err
	}
	data, _ := json.Marshal(settings)
	return checkPlanningSettings(data)
}

func checkPlanningSettings(data []byte) error { return planguard.CheckHookPolicy(data) }

func preparePlanningArgs(g *planguard.Guard, workDir string, env, args []string) ([]string, []string, error) {
	clean, settings, path, err := splitPlanningSettings(workDir, args)
	if err != nil {
		return nil, nil, err
	}
	paths := planningPolicyFiles(workDir, env)
	if path != "" {
		paths = append(paths, path)
	}
	if err := g.SetHookPolicyFiles(paths); err != nil {
		return nil, nil, err
	}
	original, err := json.Marshal(settings)
	if err != nil {
		return nil, nil, err
	}
	if err := g.SetSettingsSource(path, original); err != nil {
		return nil, nil, err
	}
	if path != "" {
		if err := anchorSettingsPermissionRules(settings, path); err != nil {
			return nil, nil, err
		}
	}
	own := g.ClaudeArgs()
	var injected map[string]any
	if err := json.Unmarshal([]byte(own[1]), &injected); err != nil {
		return nil, nil, err
	}
	hooks, ok := settings["hooks"].(map[string]any)
	if settings["hooks"] != nil && !ok {
		return nil, nil, fmt.Errorf("cannot merge CLI planning hooks")
	}
	if hooks == nil {
		hooks = map[string]any{}
	}
	for event, groups := range injected["hooks"].(map[string]any) {
		existing, ok := hooks[event].([]any)
		if hooks[event] != nil && !ok {
			return nil, nil, fmt.Errorf("cannot merge CLI planning hooks")
		}
		hooks[event] = append(existing, groups.([]any)...)
	}
	settings["hooks"] = hooks
	// The guard remains callable across hot reloads. It checks original policy
	// files before every tool and denies work if the user disables hooks there.
	settings["disableAllHooks"] = false
	settings["allowManagedHooksOnly"] = false
	merged, err := json.Marshal(settings)
	if err != nil {
		return nil, nil, err
	}
	// A merged file may contain credentials from the user settings. Keep its
	// contents out of argv, debug logs, and the process list. Guard.Close owns
	// cleanup of this private directory.
	mergedPath := filepath.Join(filepath.Dir(g.Path), "claude-settings.json")
	if err := os.WriteFile(mergedPath, merged, 0600); err != nil {
		return nil, nil, fmt.Errorf("cannot write private planning settings")
	}
	injectedArgs := append([]string{"--settings", mergedPath}, own[2:]...)
	return clean, injectedArgs, nil
}
