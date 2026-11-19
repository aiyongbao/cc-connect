package planguard

import (
	"encoding/json"
	"fmt"
	"runtime"
	"strings"
)

func quote(s string) string {
	if runtime.GOOS == "windows" {
		return `"` + strings.ReplaceAll(s, `"`, `\"`) + `"`
	}
	return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'"
}

func (g *Guard) hookCommand() string {
	command := quote(g.Executable) + " _cc-plan-hook " + quote(g.Path)
	if runtime.GOOS != "windows" {
		command += " || exit 2"
	}
	return command
}

// Keep the session guard callable even when ordinary settings are hot-reloaded.
// RunHook checks the original policy files before every tool and refuses work
// when hooks are disabled there. This does not grant any tool permission.
func (g *Guard) ClaudeArgs() []string {
	hooks := map[string]any{}
	for _, event := range []string{"PreToolUse", "PostToolUse"} {
		hooks[event] = []any{map[string]any{"matcher": ".*", "hooks": []any{map[string]any{"type": "command", "command": g.hookCommand(), "timeout": 10}}}}
	}
	settings, _ := json.Marshal(map[string]any{"hooks": hooks, "disableAllHooks": false, "allowManagedHooksOnly": false})
	mcp, _ := json.Marshal(map[string]any{"mcpServers": map[string]any{ServerName: map[string]any{"command": g.Executable, "args": []string{"_cc-plan-mcp", g.Path}}}})
	return []string{"--settings", string(settings), "--mcp-config", string(mcp)}
}

func (g *Guard) CodexArgs() []string {
	args := []string{"-c", "features.hooks=true"}
	for _, event := range []string{"PreToolUse", "PostToolUse"} {
		group := fmt.Sprintf(`[{matcher=".*",hooks=[{type="command",command=%s,timeout=10}]}]`, tomlString(g.hookCommand()))
		args = append(args, "-c", "hooks."+event+"="+group)
	}
	args = append(args, "-c", "mcp_servers."+ServerName+".command="+tomlString(g.Executable))
	args = append(args, "-c", "mcp_servers."+ServerName+".args=["+tomlString("_cc-plan-mcp")+","+tomlString(g.Path)+"]")
	return args
}

// JSON string escaping is also valid for the ordinary paths/commands used in
// TOML basic strings. Do not shell-interpolate any of these argv values.
func tomlString(s string) string { b, _ := json.Marshal(s); return string(b) }
