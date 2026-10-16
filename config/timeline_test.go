package config

import "testing"

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
