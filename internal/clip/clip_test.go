package clip

import (
	"testing"
	"unicode/utf8"
)

func TestBytesKeepsRunesWhole(t *testing.T) {
	s := "写一个应用" // 3 bytes per rune
	for max := 0; max <= len(s)+1; max++ {
		got := Bytes(s, max)
		if !utf8.ValidString(got) || len(got) > max {
			t.Fatalf("Bytes(%q, %d) = %q", s, max, got)
		}
	}
	if Bytes("hello", 3) != "hel" || Bytes("hi", 5) != "hi" {
		t.Fatal("ascii clip")
	}
}

func TestTildeShortensHomePathsForDisplay(t *testing.T) {
	t.Setenv("HOME", "/Users/ada")
	for in, want := range map[string]string{
		"/Users/ada/code/shop":            "~/code/shop",
		"cd /Users/ada/code/shop && make": "cd ~/code/shop && make",
		"/Users/ada":                      "~",
		"/Users/adam/code":                "/Users/adam/code",
	} {
		if got := Tilde(in); got != want {
			t.Errorf("Tilde(%q) = %q, want %q", in, got, want)
		}
	}
}
