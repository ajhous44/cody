package cli

import (
	"testing"

	"github.com/ajhous44/cody/internal/session"
)

func TestParseOneShotPrompt(t *testing.T) {
	o, err := parseArgs([]string{"--model", "qwen2.5-coder:7b", "fix", "the", "bug"})
	if err != nil {
		t.Fatal(err)
	}
	if o.command != "" || o.model != "qwen2.5-coder:7b" {
		t.Fatalf("opts = %+v", o)
	}
	if len(o.prompt) != 3 {
		t.Fatalf("prompt = %v", o.prompt)
	}
}

func TestParseRunCommand(t *testing.T) {
	o, err := parseArgs([]string{"run", "hello world"})
	if err != nil {
		t.Fatal(err)
	}
	if o.command != "run" || len(o.prompt) != 1 || o.prompt[0] != "hello world" {
		t.Fatalf("opts = %+v", o)
	}
}

func TestParseBenchFlags(t *testing.T) {
	o, err := parseArgs([]string{"bench", "--runs", "3", "--task", "fix-test", "--json"})
	if err != nil {
		t.Fatal(err)
	}
	if o.command != "bench" || o.runs != 3 || o.task != "fix-test" || !o.jsonOut {
		t.Fatalf("opts = %+v", o)
	}
}

func TestParseYoloAndResume(t *testing.T) {
	o, err := parseArgs([]string{"--yolo", "--resume", "last"})
	if err != nil {
		t.Fatal(err)
	}
	if o.approval != "yolo" || o.resume != "last" {
		t.Fatalf("opts = %+v", o)
	}
}

func TestParseResumeCommand(t *testing.T) {
	o, err := parseArgs([]string{"resume"})
	if err != nil {
		t.Fatal(err)
	}
	if o.command != "resume" || o.resume != "last" || len(o.prompt) != 0 {
		t.Fatalf("opts = %+v", o)
	}

	o, err = parseArgs([]string{"resume", "20260101-120000-abcd"})
	if err != nil {
		t.Fatal(err)
	}
	if o.resume != "20260101-120000-abcd" || len(o.prompt) != 0 {
		t.Fatalf("opts = %+v", o)
	}

	o, err = parseArgs([]string{"resume", "last", "fix it"})
	if err != nil {
		t.Fatal(err)
	}
	if o.resume != "last" || len(o.prompt) != 1 || o.prompt[0] != "fix it" {
		t.Fatalf("opts = %+v", o)
	}
}

func TestParseReplayDefaultsToLast(t *testing.T) {
	o, err := parseArgs([]string{"replay"})
	if err != nil {
		t.Fatal(err)
	}
	if o.command != "replay" || len(o.prompt) != 1 || o.prompt[0] != "last" {
		t.Fatalf("opts = %+v", o)
	}
}

func TestFindMeta(t *testing.T) {
	recs := []session.Record{
		{Type: "message", Role: "user", Text: "hi"},
		{Type: "meta", Meta: map[string]string{"model": "m"}},
	}
	if got := findMeta(recs)["model"]; got != "m" {
		t.Fatalf("meta model = %q", got)
	}
	if findMeta(nil) != nil {
		t.Fatal("expected nil meta")
	}
}

func TestParseIndexCommand(t *testing.T) {
	o, err := parseArgs([]string{"index", "--rebuild"})
	if err != nil {
		t.Fatal(err)
	}
	if o.command != "index" || !o.rebuild {
		t.Fatalf("opts = %+v", o)
	}
}

func TestParseShowThinking(t *testing.T) {
	o, err := parseArgs([]string{"--show-thinking", "hi"})
	if err != nil {
		t.Fatal(err)
	}
	if !o.showThinking {
		t.Fatalf("opts = %+v", o)
	}
}

func TestParseUnknownFlag(t *testing.T) {
	if _, err := parseArgs([]string{"--nope"}); err == nil {
		t.Fatal("expected error")
	}
}

func TestParseDoubleDash(t *testing.T) {
	o, err := parseArgs([]string{"--", "--not-a-flag"})
	if err != nil {
		t.Fatal(err)
	}
	if len(o.prompt) != 1 || o.prompt[0] != "--not-a-flag" {
		t.Fatalf("opts = %+v", o)
	}
}

func TestParseSessionsCommand(t *testing.T) {
	o, err := parseArgs([]string{"sessions"})
	if err != nil {
		t.Fatal(err)
	}
	if o.command != "sessions" {
		t.Fatalf("opts = %+v", o)
	}
}
