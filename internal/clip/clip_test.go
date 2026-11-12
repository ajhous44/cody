package clip

import (
	"path/filepath"
	"testing"
	"unicode/utf8"
)

// setHome points os.UserHomeDir at home on every OS: Windows reads
// USERPROFILE, everything else HOME.
func setHome(t *testing.T, home string) {
	home = filepath.FromSlash(home)
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
}

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
	setHome(t, "/Users/ada")
	for in, want := range map[string]string{
		"/Users/ada/code/shop":            "~/code/shop",
		"cd /Users/ada/code/shop && make": "cd ~/code/shop && make",
		"/Users/ada":                      "~",
		"/Users/adam/code":                "/Users/adam/code",
	} {
		// Native separators, so Windows (`\`) is exercised the same way.
		in, want = filepath.FromSlash(in), filepath.FromSlash(want)
		if got := Tilde(in); got != want {
			t.Errorf("Tilde(%q) = %q, want %q", in, got, want)
		}
	}
}
