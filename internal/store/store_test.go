package store

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func TestDurableSessionAndIndependentFork(t *testing.T) {
	s := Store{Directory: t.TempDir()}
	parent := NewSession("agent", "../../remote-id", t.TempDir())
	parent.Messages = []Message{{Role: "user", Text: "hello"}}
	if err := s.Save(parent); err != nil {
		t.Fatal(err)
	}
	fork := NewSession(parent.Agent, "branch", parent.Cwd)
	fork.ParentID = parent.ID
	fork.Messages = append([]Message(nil), parent.Messages...)
	fork.Messages[0].Text = "branch"
	fork.UpdatedAt = parent.UpdatedAt.Add(time.Second)
	if err := s.Save(fork); err != nil {
		t.Fatal(err)
	}
	all, err := s.List()
	if err != nil || len(all) != 2 || all[0].ID != fork.ID {
		t.Fatalf("list %v: %v", all, err)
	}
	loaded, err := s.Load(parent.ID)
	if err != nil || loaded.Messages[0].Text != "hello" {
		t.Fatalf("parent modified: %+v %v", loaded, err)
	}
	if err := s.Delete(fork.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Load(parent.ID); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(filepath.Join(s.Directory, "sessions", parent.ID+".json"))
	if err != nil || runtime.GOOS != "windows" && info.Mode().Perm() != 0600 {
		t.Fatalf("private transcript: %v %v", info, err)
	}
}
func TestRejectLocalTraversalAndCorruptData(t *testing.T) {
	s := Store{Directory: t.TempDir()}
	for _, id := range []string{"../../secret", "/tmp/session", "", "not-hex-12345678"} {
		if err := s.Delete(id); err == nil {
			t.Fatalf("accepted %q", id)
		}
	}
	p := NewSession("agent", "remote", t.TempDir())
	if err := s.Save(p); err != nil {
		t.Fatal(err)
	}
	path, _ := s.path(p.ID)
	os.WriteFile(path, []byte("broken JSON"), 0600)
	if _, err := s.List(); err == nil {
		t.Fatal("corrupt session silently ignored")
	}
}

func TestListSkipsSessionDeletedAfterDirectoryRead(t *testing.T) {
	s := Store{Directory: t.TempDir()}
	kept := NewSession("agent", "kept", t.TempDir())
	deleted := NewSession("agent", "deleted", kept.Cwd)
	for _, session := range []Session{kept, deleted} {
		if err := s.Save(session); err != nil {
			t.Fatal(err)
		}
	}
	entries, err := os.ReadDir(filepath.Join(s.Directory, "sessions"))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Delete(deleted.ID); err != nil {
		t.Fatal(err)
	}
	listed, err := s.loadEntries(entries)
	if err != nil || len(listed) != 1 || listed[0].ID != kept.ID {
		t.Fatalf("listing raced with deletion: %+v, %v", listed, err)
	}
}
