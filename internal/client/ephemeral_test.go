package client_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/BrokkAi/micro-acp/internal/client"
	"github.com/BrokkAi/micro-acp/internal/store"
)

func sessionFiles(t *testing.T, directory string) int {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(directory, "sessions"))
	if os.IsNotExist(err) {
		return 0
	}
	if err != nil {
		t.Fatal(err)
	}
	return len(entries)
}

func runTurn(t *testing.T, c *client.Client) {
	t.Helper()
	if err := c.New(); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Prompt("hello"); err != nil {
		t.Fatal(err)
	}
}

// Headless runs without --save must leave the local session store untouched
// while the agent still sees an ordinary session.
func TestEphemeralSessionsAreNotPersisted(t *testing.T) {
	clientData := t.TempDir()
	st := store.Store{Directory: clientData}
	c := openTest(t, "demo", t.TempDir(), t.TempDir(), st)
	c.Ephemeral = true

	runTurn(t, c)

	snapshot, _ := c.Snapshot()
	if snapshot.RemoteID == "" {
		t.Fatal("ephemeral run did not establish a session")
	}
	if len(snapshot.Messages) < 2 {
		t.Fatalf("ephemeral run saved no conversation: %d messages", len(snapshot.Messages))
	}
	if sessions, err := st.List(); err != nil {
		t.Fatal(err)
	} else if len(sessions) != 0 {
		t.Fatalf("ephemeral run persisted %d sessions", len(sessions))
	}
	if files := sessionFiles(t, clientData); files != 0 {
		t.Fatalf("ephemeral run wrote %d session files", files)
	}
}

// The same flow persists normally, so the test above cannot pass merely because
// saving is broken.
func TestSessionsPersistWhenNotEphemeral(t *testing.T) {
	clientData := t.TempDir()
	st := store.Store{Directory: clientData}
	c := openTest(t, "demo", t.TempDir(), t.TempDir(), st)

	runTurn(t, c)

	sessions, err := st.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 1 {
		t.Fatalf("persistent run stored %d sessions, want 1", len(sessions))
	}
	if len(sessions[0].Messages) < 2 {
		t.Fatalf("persistent run stored %d messages, want the whole turn", len(sessions[0].Messages))
	}
	if files := sessionFiles(t, clientData); files != 1 {
		t.Fatalf("persistent run wrote %d session files, want 1", files)
	}
}
