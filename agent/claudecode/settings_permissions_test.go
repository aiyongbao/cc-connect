package claudecode

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
)

func TestAnchorSettingsPermissionRules(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	base := filepath.Join(root, "policy [local] (test)")
	if err := os.Mkdir(base, 0700); err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(base, "settings.json")
	var settings map[string]any
	input := `{"permissions":{"allow":["Edit(/output/**/*.csv)","Edit(//etc/ready)","Edit(/)","Edit(/**)"],"ask":["Read(/manual (draft).md)","Read(./working.md)"],"deny":["Read(/private/**)","Read(!/cwd-only)","Read(~/secret)","Bash(/opt/bin/script *)","Read"]}}`
	if err := json.Unmarshal([]byte(input), &settings); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(source, []byte(input), 0600); err != nil {
		t.Fatal(err)
	}
	if err := anchorSettingsPermissionRules(settings, source); err != nil {
		t.Fatal(err)
	}
	prefix := filepath.ToSlash(root)
	if runtime.GOOS == "windows" {
		prefix = "/" + strings.ToLower(prefix[:1]) + prefix[2:]
	}
	prefix = "/" + prefix + `/policy \[local\] \(test\)`
	want := map[string]any{
		"allow": []any{"Edit(" + prefix + "/output/**/*.csv)", "Edit(//etc/ready)", "Edit(/)", "Edit(" + prefix + "/**/*)"},
		"ask":   []any{"Read(" + prefix + "/manual (draft).md)", "Read(./working.md)"},
		"deny":  []any{"Read(" + prefix + "/private/**)", "Read(!/cwd-only)", "Read(~/secret)", "Bash(/opt/bin/script *)", "Read"},
	}
	if !reflect.DeepEqual(settings["permissions"], want) {
		t.Fatalf("permissions = %#v, want %#v", settings["permissions"], want)
	}
}
