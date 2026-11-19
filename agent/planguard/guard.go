// Package planguard supplies a turn-scoped, pre-execution planning barrier.
// The model authors every step. Neither tool names nor private reasoning are
// converted into business progress. Hook/MCP subprocesses share an atomic state
// file; only successfully completed planning tools can open the barrier.
package planguard

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/chenhg5/cc-connect/core"
)

const ServerName = "cc_connect_plan"

type state struct {
	SettingsSource  string                           `json:"settings_source,omitempty"`
	SettingsHash    string                           `json:"settings_hash,omitempty"`
	HookPolicyFiles []string                         `json:"hook_policy_files,omitempty"`
	Enabled         bool                             `json:"enabled"`
	Ready           bool                             `json:"ready"`
	Blocked         bool                             `json:"blocked"`
	Attempted       bool                             `json:"attempted"`
	Revision        uint64                           `json:"revision"`
	Turn            uint64                           `json:"turn"`
	Tasks           map[string]core.TaskProgressStep `json:"tasks,omitempty"`
	Order           []string                         `json:"order,omitempty"`
	Plan            *core.TaskProgress               `json:"plan,omitempty"`
	Pending         map[string]pendingCall           `json:"pending,omitempty"`
}

type pendingCall struct {
	Name string `json:"name"`
}

type Guard struct {
	Path         string
	Executable   string
	mu           sync.Mutex
	lastTurn     uint64
	lastRevision uint64
	enabled      bool
}

func New() (*Guard, error) {
	dir, err := os.MkdirTemp("", "cc-connect-plan-")
	if err != nil {
		return nil, &core.BusinessPlanSetupError{Err: fmt.Errorf("create planning barrier: %w", err)}
	}
	executable, err := os.Executable()
	if err != nil {
		_ = os.RemoveAll(dir)
		return nil, &core.BusinessPlanSetupError{Err: err}
	}
	g := &Guard{Path: filepath.Join(dir, "state.json"), Executable: executable}
	if err := writeState(g.Path, &state{}); err != nil {
		g.Close()
		return nil, &core.BusinessPlanSetupError{Err: err}
	}
	return g, nil
}

func (g *Guard) Close() {
	if g != nil {
		_ = os.RemoveAll(filepath.Dir(g.Path))
	}
}

// Begin invalidates the last turn's authorization, including resumed and queued
// sends. Continuing native tasks must be republished by TaskList/TaskGet;
// unrelated tasks from an earlier user request never enter this turn's card.
func (g *Guard) Begin(prompt string) error {
	if g == nil {
		return fmt.Errorf("planning barrier is unavailable")
	}
	// Supplements (/ps, /btw) and native /compact commands are unmarked.
	// They belong to the active turn and must never disable or reset its gate.
	if !core.BusinessPlanRequested(prompt) {
		return nil
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	g.enabled = core.BusinessPlanRequested(prompt)
	return withState(g.Path, func(s *state) error {
		s.Enabled = core.BusinessPlanRequested(prompt)
		s.Ready, s.Blocked, s.Plan = false, false, nil
		s.Attempted = false
		s.Pending = make(map[string]pendingCall)
		s.Tasks = make(map[string]core.TaskProgressStep)
		s.Order = nil
		s.Turn++
		s.Revision++
		g.lastTurn, g.lastRevision = s.Turn, s.Revision
		return nil
	})
}

// Progress is called before transport events are forwarded, preserving plan →
// business tool ordering even when a code-mode wrapper hides nested MCP events.
func (g *Guard) Progress() *core.TaskProgress {
	if g == nil {
		return nil
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	s, err := readState(g.Path)
	if err != nil || s.Turn != g.lastTurn || s.Revision == g.lastRevision || !s.Enabled || s.Plan == nil {
		return nil
	}
	g.lastRevision = s.Revision
	return s.Plan
}

func (g *Guard) Blocked() bool {
	if g == nil {
		return false
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	s, err := readState(g.Path)
	if err != nil {
		return g.enabled
	}
	return s.Enabled && !s.Ready && (s.Blocked || s.Attempted && (s.Plan == nil || len(s.Plan.Steps) == 0))
}

func (g *Guard) Enabled() bool {
	if g == nil {
		return false
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.enabled
}

func readState(path string) (*state, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var s *state
	if err := json.Unmarshal(data, &s); err != nil {
		return nil, err
	}
	if s == nil {
		return nil, fmt.Errorf("planning barrier state is empty")
	}
	return s, nil
}

func writeState(path string, s *state) error {
	data, err := json.Marshal(s)
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".state-")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(data); err != nil {
		_ = f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), path)
}

// An atomic directory lock serializes concurrent TaskCreate results and resets
// across processes. A dead hook never opens the barrier: lock errors fail shut.
func withState(path string, fn func(*state) error) error {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	lock := path + ".lock"
	for {
		if err := os.Mkdir(lock, 0o700); err == nil {
			break
		} else if !os.IsExist(err) {
			return err
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("planning barrier lock unavailable")
		case <-time.After(5 * time.Millisecond):
		}
	}
	defer os.Remove(lock)
	s, err := readState(path)
	if err != nil {
		return err
	}
	if err := fn(s); err != nil {
		return err
	}
	return writeState(path, s)
}

func planningTool(name string) string {
	name = strings.TrimSpace(name)
	if ownPlanningTool(name) {
		return "update_plan"
	}
	return name
}

func ownPlanningTool(name string) bool {
	return name == "mcp__"+ServerName+"__update_plan" || name == ServerName+"/update_plan"
}

func acceptPlan(s *state, plan *core.TaskProgress) bool {
	if plan == nil || len(plan.Steps) > 12 {
		return false
	}
	data, err := json.Marshal(plan)
	if err != nil {
		return false
	}
	plan = core.ParseTaskProgress(plan.Source, json.RawMessage(data))
	if plan == nil {
		return false
	}
	open := false
	for _, step := range plan.Steps {
		if step.Status != "completed" {
			open = true
		}
	}
	if s.Plan == nil && !open {
		return false
	} // A fresh turn cannot start with a fabricated completed plan.
	// A completed plan still updates the card, but does not authorize new work.
	s.Plan, s.Ready, s.Blocked = plan, open, false
	s.Revision++
	return true
}
