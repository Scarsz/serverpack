// Package runtime implements `serverpack run`, the container entrypoint.
package runtime

import (
	"regexp"
	"slices"
)

// placeholder matches "$${" (an escaped literal "${") or ${NAME} / ${NAME:-default}.
var placeholder = regexp.MustCompile(`\$\$\{|\$\{([A-Za-z_][A-Za-z0-9_]*)(:-([^}]*))?\}`)

// Expand substitutes environment placeholders in content. ${NAME:-default} uses default when NAME
// is unset or empty. Unset names without a default are returned in missing (and left untouched).
// Anything that doesn't look like a placeholder is left as-is.
func Expand(content string, lookup func(string) (string, bool)) (string, []string) {
	var missing []string
	out := placeholder.ReplaceAllStringFunc(content, func(m string) string {
		if m == "$${" {
			return "${"
		}
		sub := placeholder.FindStringSubmatch(m)
		name, hasDefault, def := sub[1], sub[2] != "", sub[3]
		v, ok := lookup(name)
		if ok && (v != "" || !hasDefault) {
			return v
		}
		if hasDefault {
			return def
		}
		if !slices.Contains(missing, name) {
			missing = append(missing, name)
		}
		return m
	})
	return out, missing
}
