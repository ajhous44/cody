// Package clip shortens text for display.
package clip

import (
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

// Tilde shortens every path under the home folder in s to ~/..., as shells
// show them. It is for display only: the model and the tools keep real paths.
func Tilde(s string) string {
	home, err := os.UserHomeDir()
	sep := string(filepath.Separator)
	if err != nil || home == "" || home == sep {
		return s
	}
	s = strings.ReplaceAll(s, home+sep, "~"+sep)
	if rest, ok := strings.CutSuffix(s, home); ok {
		s = rest + "~"
	}
	return s
}

// Bytes returns s cut to at most max bytes, backing up to a rune boundary.
func Bytes(s string, max int) string {
	if len(s) <= max {
		return s
	}
	for max > 0 && !utf8.RuneStart(s[max]) {
		max--
	}
	return s[:max]
}
