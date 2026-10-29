// Package clip shortens text without splitting a UTF-8 character.
package clip

import "unicode/utf8"

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
