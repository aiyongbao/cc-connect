package config

import (
	"testing"

	"github.com/BurntSushi/toml"
)

// Timeline can be selected independently per project without changing global
// rich-mode behavior for other projects.
func TestTimelineCardMode_ProjectOverride(t *testing.T) {
	rich, timeline := "rich", "timeline"
	cfg := &Config{Display: DisplayConfig{CardMode: &rich}}
	proj := &ProjectConfig{Display: &DisplayConfig{CardMode: &timeline}}
	if EffectiveCardMode(cfg, proj) != "timeline" || EffectiveCardMode(cfg, nil) != "rich" {
		t.Fatal("card mode override is not isolated")
	}
	if err := validateDisplayConfig("projects[0].display", proj.Display); err != nil {
		t.Fatal(err)
	}
}

func TestShutdownInterruptionNotice_ProjectOption(t *testing.T) {
	var cfg Config
	if _, err := toml.Decode(`[[projects]]
name = "demo"
shutdown_interruption_notice = true
`, &cfg); err != nil {
		t.Fatal(err)
	}
	if len(cfg.Projects) != 1 || cfg.Projects[0].ShutdownInterruptionNotice == nil || !*cfg.Projects[0].ShutdownInterruptionNotice {
		t.Fatalf("shutdown_interruption_notice was not parsed: %+v", cfg.Projects)
	}
}
