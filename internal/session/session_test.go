package session

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestStoreRoundTrip(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CODY_CONFIG_DIR", home)
	root := filepath.Join(home, "project")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	store := New(root, "")
	if err := store.Append(Record{Type: "meta", Meta: map[string]string{"model": "m"}}); err != nil {
		t.Fatal(err)
	}
	if err := store.Append(Record{Type: "message", Role: "user", Text: "hello"}); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	id, recs, err := Load(root, store.ID())
	if err != nil {
		t.Fatal(err)
	}
	if id != store.ID() || len(recs) != 2 {
		t.Fatalf("id=%s recs=%d", id, len(recs))
	}
	if recs[1].Text != "hello" || recs[1].Role != "user" {
		t.Fatalf("rec = %+v", recs[1])
	}

	infos, err := List(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(infos) != 1 || infos[0].ID != store.ID() || infos[0].First != "hello" {
		t.Fatalf("infos = %+v", infos)
	}

	lastID, _, err := Load(root, "last")
	if err != nil {
		t.Fatal(err)
	}
	if lastID != store.ID() {
		t.Fatalf("last = %s, want %s", lastID, store.ID())
	}

	if _, err := os.Stat(store.Path()); err != nil {
		t.Fatal(err)
	}
}

func TestAppendAfterPartialRecord(t *testing.T) {
	t.Setenv("CODY_CONFIG_DIR", t.TempDir())
	store := New(t.TempDir(), "")
	if err := os.MkdirAll(filepath.Dir(store.Path()), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(store.Path(), []byte(`{"type":"message","text":"partial`), 0600); err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.Append(Record{Type: "message", Role: "user", Text: "recover"}); err != nil {
		t.Fatal(err)
	}
	recs, err := loadFile(store.Path(), 0)
	if err != nil || len(recs) != 1 || recs[0].Text != "recover" {
		t.Fatalf("recovery: %+v %v", recs, err)
	}
}

func TestProjectDirIsStableAndSeparate(t *testing.T) {
	t.Setenv("CODY_CONFIG_DIR", t.TempDir())
	a := ProjectDir("/tmp/one")
	b := ProjectDir("/tmp/two")
	if a == b {
		t.Fatal("different workspaces must map to different dirs")
	}
	if a != ProjectDir("/tmp/one") {
		t.Fatal("project dir must be stable")
	}
}

func TestRenameAndDelete(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CODY_CONFIG_DIR", home)
	root := filepath.Join(home, "project")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	store := New(root, "")
	if err := store.Append(Record{Type: "meta", Meta: map[string]string{"model": "m"}}); err != nil {
		t.Fatal(err)
	}
	if err := store.Append(Record{Type: "message", Role: "user", Text: "original first message"}); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	if err := Rename(root, store.ID(), "my renamed thread"); err != nil {
		t.Fatalf("Rename: %v", err)
	}
	infos, err := List(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(infos) != 1 || infos[0].First != "my renamed thread" {
		t.Fatalf("infos = %+v", infos)
	}

	if err := Delete(root, store.ID()); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	infos, err = List(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(infos) != 0 {
		t.Fatalf("session not deleted: %+v", infos)
	}

	if err := Rename(root, "does-not-exist", "x"); err == nil {
		t.Fatal("expected error renaming a missing session")
	}
}

func TestListEmpty(t *testing.T) {
	t.Setenv("CODY_CONFIG_DIR", t.TempDir())
	infos, err := List("/tmp/nonexistent-project")
	if err != nil {
		t.Fatal(err)
	}
	if len(infos) != 0 {
		t.Fatalf("infos = %+v", infos)
	}
}

func TestSessionIDFormat(t *testing.T) {
	id := NewID()
	if len(id) != len(time.Now().Format("20060102-150405"))+1+6 {
		t.Fatalf("unexpected id %q", id)
	}
}

func TestSessionIDsRejectTraversal(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CODY_CONFIG_DIR", home)
	root := filepath.Join(home, "project")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"../evil", "..", "a/b", "/abs", "a\\b", ""} {
		if _, _, err := Load(root, id); err == nil {
			t.Fatalf("Load(%q) accepted", id)
		}
		if err := Delete(root, id); err == nil {
			t.Fatalf("Delete(%q) accepted", id)
		}
		if err := Rename(root, id, "x"); err == nil {
			t.Fatalf("Rename(%q) accepted", id)
		}
	}
}

func TestAppendBatchWritesAllRecordsInOrder(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CODY_CONFIG_DIR", home)
	root := filepath.Join(home, "project")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	store := New(root, "")
	recs := []Record{
		{Type: "usage"},
		{Type: "message", Role: "user", Text: "one"},
		{Type: "message", Role: "assistant", Text: "two"},
	}
	if err := store.AppendBatch(recs); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	_, got, err := Load(root, store.ID())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != len(recs) {
		t.Fatalf("records = %d, want %d", len(got), len(recs))
	}
	for i := range recs {
		if got[i].Type != recs[i].Type || got[i].Text != recs[i].Text {
			t.Fatalf("record %d = %+v, want %+v", i, got[i], recs[i])
		}
	}
}

func TestConcurrentRenamesKeepEveryTitle(t *testing.T) {
	t.Setenv("CODY_CONFIG_DIR", t.TempDir())
	root := t.TempDir()
	var ids []string
	for range 8 {
		store := New(root, "")
		if err := store.Append(Record{Type: "meta"}); err != nil {
			t.Fatal(err)
		}
		store.Close()
		ids = append(ids, store.ID())
	}
	var wg sync.WaitGroup
	for i, id := range ids {
		wg.Go(func() {
			if err := Rename(root, id, fmt.Sprint("title ", i)); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	for i, id := range ids {
		if got := Title(root, id); got != fmt.Sprint("title ", i) {
			t.Fatalf("%s title = %q", id, got)
		}
	}
	if err := Rename(root, ids[0], "renamed"); err != nil || Title(root, ids[0]) != "renamed" {
		t.Fatalf("cached title not refreshed: %q %v", Title(root, ids[0]), err)
	}
}

func TestForkCopiesHistoryBeforeTurn(t *testing.T) {
	t.Setenv("CODY_CONFIG_DIR", t.TempDir())
	root := t.TempDir()
	src := New(root, "")
	recs := []Record{
		{Type: "meta", Meta: map[string]string{"model": "m"}},
		{Type: "message", Role: "user", Text: "first ask"},
		{Type: "message", Role: "assistant", Text: "", ToolCalls: []ToolCall{{ID: "c1", Name: "read", Args: "{}"}}},
		{Type: "message", Role: "tool", ToolCallID: "c1", Text: "file"},
		{Type: "usage"},
		{Type: "message", Role: "assistant", Text: "first answer"},
		{Type: "message", Role: "user", Text: "second ask"},
		{Type: "message", Role: "assistant", Text: "second answer"},
	}
	if err := src.AppendBatch(recs); err != nil {
		t.Fatal(err)
	}
	src.Close()
	before, _ := os.ReadFile(src.Path())

	id, prefill, err := Fork(root, src.ID(), 2)
	if err != nil || prefill != "second ask" || id == src.ID() {
		t.Fatalf("fork: %q %q %v", id, prefill, err)
	}
	_, got, err := Load(root, id)
	if err != nil {
		t.Fatal(err)
	}
	var texts []string
	for _, r := range got {
		if r.Type == "message" {
			texts = append(texts, r.Role+":"+r.Text)
		}
	}
	if len(got) != 5 || got[0].Type != "meta" || got[0].Meta["forked_from"] != src.ID() || got[0].Meta["model"] != "m" ||
		fmt.Sprint(texts) != "[user:first ask assistant: tool:file assistant:first answer]" {
		t.Fatalf("fork records: %+v", got)
	}
	if Title(root, id) != "first ask (fork)" {
		t.Fatalf("title %q", Title(root, id))
	}
	if after, _ := os.ReadFile(src.Path()); string(after) != string(before) {
		t.Fatal("source session changed")
	}

	whole, prefill, err := Fork(root, id, 0)
	if _, all, _ := Load(root, whole); err != nil || prefill != "" || len(all) != 5 || all[0].Meta["forked_from"] != id {
		t.Fatalf("whole-thread fork of a fork: %v %q %+v", err, prefill, all)
	}
	if _, _, err := Fork(root, src.ID(), 3); err == nil {
		t.Fatal("forked past the last turn")
	}
}
