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
