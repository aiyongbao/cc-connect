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

type planningSettings struct {
	extraArgs      []string
	values         map[string]any
	source         string
	resolvedSource string
}

// Claude treats --settings as a scalar (last value wins). Resolve a file once
// so the fingerprint and the CLI use the same source, even through a symlink.
func splitPlanningSettings(workDir string, args []string) (planningSettings, error) {
	result := planningSettings{values: map[string]any{}}
	var value string
	hasSettings := false
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "--settings":
			hasSettings = true
			if i+1 >= len(args) {
				return result, fmt.Errorf("missing CLI settings value")
			}
			i++
			value = args[i]
		case strings.HasPrefix(arg, "--settings="):
			hasSettings = true
			value = strings.TrimPrefix(arg, "--settings=")
		default:
			result.extraArgs = append(result.extraArgs, arg)
		}
	}
	if !hasSettings {
		return result, nil
	}
	if value == "" {
		return result, fmt.Errorf("empty CLI settings value")
	}
	data := []byte(value)
	if !strings.HasPrefix(strings.TrimSpace(value), "{") {
		path := value
		if !filepath.IsAbs(path) {
			path = filepath.Join(workDir, path)
		}
		var err error
		result.source, err = filepath.Abs(path)
		if err != nil {
			return result, fmt.Errorf("cannot resolve CLI planning settings")
		}
		result.resolvedSource, err = filepath.EvalSymlinks(result.source)
		if err != nil {
			return result, fmt.Errorf("cannot resolve CLI planning settings")
		}
		data, err = os.ReadFile(result.resolvedSource)
		if err != nil {
			return result, fmt.Errorf("cannot read CLI planning settings")
		}
	}
	if err := json.Unmarshal(stripJSONC(data), &result.values); err != nil || result.values == nil {
		return result, fmt.Errorf("cannot parse CLI planning settings")
	}
	return result, nil
}

type planningArgs struct {
	extra   []string
	inner   []string
	overlay map[string]any
}

func preparePlanningArgs(g *planguard.Guard, workDir string, env, args []string) (planningArgs, error) {
	settings, err := splitPlanningSettings(workDir, args)
	if err != nil {
		return planningArgs{}, err
	}
	paths := planningPolicyFiles(workDir, env)
	if err := planguard.CheckHookPolicyFiles(paths); err != nil {
		return planningArgs{}, err
	}
	original, err := json.Marshal(settings.values)
	if err != nil {
		return planningArgs{}, err
	}
	if err := planguard.CheckHookPolicy(original); err != nil {
		return planningArgs{}, err
	}
	if err := g.SetHookPolicyFiles(paths); err != nil {
		return planningArgs{}, err
	}
	own := g.ClaudeArgs()
	var injected map[string]any
	if err := json.Unmarshal([]byte(own[1]), &injected); err != nil {
		return planningArgs{}, err
	}
	result := planningArgs{extra: settings.extraArgs}
	if settings.source != "" {
		if err := g.SetSettingsSource(settings.source, settings.resolvedSource, original); err != nil {
			return planningArgs{}, err
		}
		// Preserve Claude's source directory for both its permission matcher and
		// Bash sandbox. Install only the guard settings over stdio before Send.
		result.inner = append([]string{"--settings", settings.resolvedSource}, own[2:]...)
		result.overlay = injected
		return result, nil
	}
	merged, err := mergeInlinePlanningSettings(settings.values, injected)
	if err != nil {
		return planningArgs{}, err
	}
	// The private file keeps any inline credentials out of argv and logs.
	mergedPath := filepath.Join(filepath.Dir(g.Path), "claude-settings.json")
	if err := os.WriteFile(mergedPath, merged, 0600); err != nil {
		return planningArgs{}, fmt.Errorf("cannot write private planning settings")
	}
	result.inner = append([]string{"--settings", mergedPath}, own[2:]...)
	return result, nil
}

func mergeInlinePlanningSettings(settings, injected map[string]any) ([]byte, error) {
	hooks, ok := settings["hooks"].(map[string]any)
	if settings["hooks"] != nil && !ok {
		return nil, fmt.Errorf("cannot merge CLI planning hooks")
	}
	if hooks == nil {
		hooks = map[string]any{}
	}
	for event, groups := range injected["hooks"].(map[string]any) {
		existing, ok := hooks[event].([]any)
		if hooks[event] != nil && !ok {
			return nil, fmt.Errorf("cannot merge CLI planning hooks")
		}
		hooks[event] = append(existing, groups.([]any)...)
	}
	settings["hooks"] = hooks
	settings["disableAllHooks"] = false
	settings["allowManagedHooksOnly"] = false
	return json.Marshal(settings)
}
