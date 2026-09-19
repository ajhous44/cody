package session

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestStoreRoundTrip(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
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

func TestProjectDirIsStableAndSeparate(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	a := ProjectDir("/tmp/one")
	b := ProjectDir("/tmp/two")
	if a == b {
		t.Fatal("different workspaces must map to different dirs")
	}
	if a != ProjectDir("/tmp/one") {
		t.Fatal("project dir must be stable")
	}
}

func TestListEmpty(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
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
