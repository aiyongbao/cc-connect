package planguard

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"github.com/chenhg5/cc-connect/core"
	"os/exec"
	"strings"
	"time"
)

type codexHook struct {
	Key     string `json:"key"`
	Hash    string `json:"currentHash"`
	Event   string `json:"eventName"`
	Command string `json:"command"`
	Source  string `json:"source"`
	Trusted string `json:"trustStatus"`
	Enabled bool   `json:"enabled"`
}

// PrepareCodex verifies the CLI actually loaded our pre/post guards before any
// model turn. Trust is granted only to these exact session-local hook hashes,
// via argv overrides, never to unrelated user hooks or on-disk configuration.
func (g *Guard) PrepareCodex(ctx context.Context, bin string, extra []string, workDir string, env []string) (prepared []string, setupErr error) {
	defer func() {
		if setupErr != nil {
			setupErr = &core.BusinessPlanSetupError{Err: setupErr}
		}
	}()
	for i, arg := range extra {
		if strings.HasPrefix(arg, "hooks.") || strings.HasPrefix(arg, "--config=hooks.") || strings.HasPrefix(arg, "-c=hooks.") {
			return nil, fmt.Errorf("business planning cannot replace custom CLI hook overrides")
		}
		if strings.Contains(arg, "features.hooks=false") || strings.Contains(arg, "features.hooks.enabled=false") || (arg == "--disable" && i+1 < len(extra) && extra[i+1] == "hooks") {
			return nil, fmt.Errorf("business planning cannot run with explicitly disabled execution hooks")
		}
	}
	args := g.CodexArgs()
	hooks, err := g.probeCodex(ctx, bin, extra, args, workDir, env)
	if err != nil {
		return nil, err
	}
	var trusted []string
	for _, hook := range hooks {
		if !hook.Enabled || hook.Hash == "" || hook.Key == "" {
			return nil, fmt.Errorf("Codex planning hooks are disabled or unavailable")
		}
		trusted = append(trusted, tomlString(hook.Key)+"={trusted_hash="+tomlString(hook.Hash)+"}")
	}
	// CLI dotted-key parsing does not preserve dots inside quoted hook keys
	// (config.toml is part of the key). Set the table value as one TOML value.
	args = append(args, "-c", "hooks.state={"+strings.Join(trusted, ",")+"}")
	verified, err := g.probeCodex(ctx, bin, extra, args, workDir, env)
	if err != nil {
		return nil, err
	}
	for _, hook := range verified {
		if !hook.Enabled || (hook.Trusted != "trusted" && hook.Trusted != "managed") {
			return nil, fmt.Errorf("Codex did not activate the planning barrier; work has not started")
		}
	}
	return args, nil
}

func (g *Guard) probeCodex(ctx context.Context, bin string, extra, args []string, workDir string, env []string) ([]codexHook, error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	argv := append(append(append([]string{}, extra...), args...), "app-server")
	cmd := exec.CommandContext(ctx, bin, argv...)
	cmd.Dir = workDir
	cmd.Env = env
	in, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err = cmd.Start(); err != nil {
		return nil, fmt.Errorf("verify Codex planning hooks: %w", err)
	}
	defer func() { _ = in.Close(); _ = cmd.Process.Kill(); _ = cmd.Wait() }()
	enc := json.NewEncoder(in)
	if err = enc.Encode(map[string]any{"id": 1, "method": "initialize", "params": map[string]any{"clientInfo": map[string]any{"name": "cc-connect-plan-check", "version": "1"}, "capabilities": map[string]any{"experimentalApi": true}}}); err != nil {
		return nil, err
	}
	scanner := bufio.NewScanner(out)
	scanner.Buffer(make([]byte, 4096), 4<<20)
	for scanner.Scan() {
		var r struct {
			ID     int             `json:"id"`
			Result json.RawMessage `json:"result"`
			Error  json.RawMessage `json:"error"`
		}
		if json.Unmarshal(scanner.Bytes(), &r) != nil {
			continue
		}
		if r.ID == 0 {
			continue
		}
		if len(r.Error) > 0 {
			return nil, fmt.Errorf("Codex cannot verify planning hooks; use a CLI supporting PreToolUse and hooks/list")
		}
		if r.ID == 1 {
			if err = enc.Encode(map[string]any{"method": "initialized", "params": map[string]any{}}); err != nil {
				return nil, err
			}
			if err = enc.Encode(map[string]any{"id": 2, "method": "hooks/list", "params": map[string]any{"cwds": []string{workDir}}}); err != nil {
				return nil, err
			}
			continue
		}
		if r.ID != 2 {
			continue
		}
		var result struct {
			Data []struct {
				Hooks []codexHook `json:"hooks"`
			} `json:"data"`
		}
		if err = json.Unmarshal(r.Result, &result); err != nil {
			return nil, err
		}
		found := map[string]codexHook{}
		for _, entry := range result.Data {
			for _, hook := range entry.Hooks {
				if hook.Command == g.hookCommand() && hook.Source == "sessionFlags" {
					found[hook.Event] = hook
				}
			}
		}
		pre, okPre := found["preToolUse"]
		post, okPost := found["postToolUse"]
		if !okPre || !okPost {
			return nil, fmt.Errorf("Codex planning hooks were not registered; work has not started")
		}
		return []codexHook{pre, post}, nil
	}
	return nil, fmt.Errorf("Codex planning verification did not finish; work has not started")
}
