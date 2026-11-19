package claudecode

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/chenhg5/cc-connect/core"
)

type settingsRefreshStart struct {
	resume  string
	source  string
	session *claudeSession
}

type settingsRefreshAgent struct {
	workDir, configDir, dataDir, link string
	starts                            chan settingsRefreshStart
}

func (a *settingsRefreshAgent) Name() string { return "claudecode" }
func (a *settingsRefreshAgent) ListSessions(context.Context) ([]core.AgentSessionInfo, error) {
	return nil, nil
}
func (a *settingsRefreshAgent) Stop() error { return nil }
func (a *settingsRefreshAgent) StartSession(ctx context.Context, resume string) (core.AgentSession, error) {
	cs, err := newClaudeSession(ctx, a.workDir, os.Args[0], []string{"_cc-plan-test-cli", "--settings", a.link}, "", "test-model", "", resume, "default", "", "", nil, nil, nil, []string{"CLAUDE_CONFIG_DIR=" + a.configDir}, "", false, core.SpawnOptions{}, 0, 0, a.dataDir, core.LangEnglish)
	if err != nil {
		return nil, err
	}
	for i, arg := range cs.cmd.Args {
		if arg == "--settings" {
			a.starts <- settingsRefreshStart{resume, cs.cmd.Args[i+1], cs}
			break
		}
	}
	return cs, nil
}

type settingsRefreshPlatform struct{ replies chan string }

func (p *settingsRefreshPlatform) Name() string                                     { return "fixture" }
func (p *settingsRefreshPlatform) ProgressStyle() string                            { return "timeline" }
func (p *settingsRefreshPlatform) SupportsTimelineProgress() bool                   { return true }
func (p *settingsRefreshPlatform) SupportsProgressCardPayload() bool                { return true }
func (p *settingsRefreshPlatform) Start(core.MessageHandler) error                  { return nil }
func (p *settingsRefreshPlatform) Stop() error                                      { return nil }
func (p *settingsRefreshPlatform) UpdateMessage(context.Context, any, string) error { return nil }
func (p *settingsRefreshPlatform) SendPreviewStart(context.Context, any, string) (any, error) {
	return "preview", nil
}
func (p *settingsRefreshPlatform) Reply(ctx context.Context, target any, text string) error {
	return p.Send(ctx, target, text)
}
func (p *settingsRefreshPlatform) Send(_ context.Context, _ any, text string) error {
	p.replies <- text
	return nil
}

// Exercise a real Engine and adapter across three user turns. The CLI fixture
// handles the protocol locally, while the settings change is a real symlink.
func TestClaudeBusinessPlan_SymlinkTargetChangeResumesSavedSession(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("requires atomic symlink replacement")
	}
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	policy := []byte(`{"permissions":{"deny":["Edit(/private.txt)"]}}`)
	var sources []string
	for _, name := range []string{"first", "second"} {
		base := filepath.Join(dir, name)
		if err := os.Mkdir(base, 0700); err != nil {
			t.Fatal(err)
		}
		source := filepath.Join(base, "settings.json")
		if err := os.WriteFile(source, policy, 0600); err != nil {
			t.Fatal(err)
		}
		sources = append(sources, source)
	}
	link := filepath.Join(dir, "settings-link.json")
	if err := os.Symlink(sources[0], link); err != nil {
		t.Fatal(err)
	}
	agent := &settingsRefreshAgent{dir, t.TempDir(), t.TempDir(), link, make(chan settingsRefreshStart, 8)}
	platform := &settingsRefreshPlatform{replies: make(chan string, 16)}
	engine := core.NewEngine("settings-refresh", agent, []core.Platform{platform}, filepath.Join(dir, "sessions.json"), core.LangEnglish)
	defer engine.Stop()
	const key = "fixture:chat"
	chat := engine.GetSessions().GetOrCreateActive(key)
	turn := func(id string) {
		t.Helper()
		engine.ReceiveMessage(platform, &core.Message{SessionKey: key, UserID: "reader", MessageID: id, Content: "hello", ReplyCtx: id})
		select {
		case reply := <-platform.replies:
			if !strings.HasPrefix(reply, "hello") {
				t.Fatalf("unexpected reply for %s: %q", id, reply)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("no reply for %s", id)
		}
		deadline := time.Now().Add(2 * time.Second)
		for chat.Busy() && time.Now().Before(deadline) {
			time.Sleep(5 * time.Millisecond)
		}
		if chat.Busy() {
			t.Fatal("turn stayed busy")
		}
	}
	turn("first")
	var first settingsRefreshStart
	select {
	case first = <-agent.starts:
	default:
		t.Fatal("initial turn did not start a settings transport")
	}
	saved := chat.GetAgentSessionID()
	if saved == "" {
		t.Fatal("initial conversation ID was not saved")
	}
	newLink := filepath.Join(dir, "next-settings-link.json")
	if err := os.Symlink(sources[1], newLink); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(newLink, link); err != nil {
		t.Fatal(err)
	}
	if !first.session.NeedsSessionRefresh() {
		t.Fatal("identical policy at different path did not request a refresh")
	}
	if first.source != sources[0] {
		t.Fatal("initial conversation did not use the first settings target")
	}
	turn("second")
	select {
	case next := <-agent.starts:
		if next.resume != saved || next.source != sources[1] || first.session.Alive() {
			t.Fatal("refresh did not replace the transport with the new settings target and saved session")
		}
	default:
		t.Fatal("next turn did not replace the transport")
	}
	turn("third")
	if len(agent.starts) != 0 || chat.GetAgentSessionID() != saved {
		t.Fatal("unchanged second target did not reuse the resumed conversation")
	}
}
