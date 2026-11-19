package claudecode

import (
	"fmt"
	"path/filepath"
	"runtime"
	"strings"
)

// Claude anchors a single-slash Read or Edit pattern at the settings file's
// directory. Express those patterns as // absolute rules before moving the
// settings to the private merged file.
func anchorSettingsPermissionRules(settings map[string]any, source string) error {
	permissions, _ := settings["permissions"].(map[string]any)
	var absolutePrefix string
	for _, decision := range []string{"allow", "ask", "deny"} {
		rules, _ := permissions[decision].([]any)
		for i, value := range rules {
			rule, ok := value.(string)
			if !ok {
				continue
			}
			tool, argument, found := strings.Cut(rule, "(")
			if !found || (tool != "Read" && tool != "Edit") || !strings.HasSuffix(argument, ")") {
				continue
			}
			pattern := strings.TrimSuffix(argument, ")")
			if runtime.GOOS == "windows" && strings.HasPrefix(pattern, `\`) &&
				!strings.HasPrefix(pattern, `\!`) && !strings.HasPrefix(pattern, `\#`) {
				pattern = strings.ReplaceAll(pattern, `\`, "/")
			}
			if !strings.HasPrefix(pattern, "/") || strings.HasPrefix(pattern, "//") || pattern == "/" {
				continue
			}
			if absolutePrefix == "" {
				// Claude resolves the settings file itself, including a symlink
				// to a file in another directory, before selecting the base.
				resolved, err := filepath.EvalSymlinks(source)
				if err != nil {
					return fmt.Errorf("cannot resolve the original permission rule directory")
				}
				base := filepath.Dir(resolved)
				path := filepath.ToSlash(base)
				volume := filepath.VolumeName(base)
				if len(volume) == 2 && volume[1] == ':' {
					// Claude uses /c/... in absolute rules on Windows.
					path = "/" + strings.ToLower(volume[:1]) + path[2:]
				}
				// The base is a literal directory, while the original suffix is
				// a gitignore pattern. Escape only the base and preserve the
				// suffix (including its slashes, glob characters and escapes).
				absolutePrefix = "/" + permissionPathEscaper.Replace(strings.TrimSuffix(path, "/"))
			}
			if pattern == "/**" {
				// Keep the match on descendants. Claude treats an absolute
				// pattern ending in /** as matching the parent directory too.
				pattern = "/**/*"
			}
			rules[i] = tool + "(" + absolutePrefix + pattern + ")"
		}
	}
	return nil
}

// A literal backslash needs escaping for both the tool-rule parser and its
// path matcher. Parentheses are escaped for the rule parser only.
var permissionPathEscaper = strings.NewReplacer(
	`\`, `\\\\`, `(`, `\(`, `)`, `\)`, `*`, `\*`, `?`, `\?`, `[`, `\[`, `]`, `\]`,
)
