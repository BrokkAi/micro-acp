package store

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/BrokkAi/acp-go/schema"
	"github.com/BrokkAi/micro-acp/internal/config"
)

type Message struct {
	Role    string                `json:"role"`
	Text    string                `json:"text"`
	ID      string                `json:"id,omitempty"`
	Content []schema.ContentBlock `json:"content,omitempty"`
	Tool    *schema.ToolCall      `json:"tool,omitempty"`
}

type Session struct {
	ID                    string                    `json:"id"`
	Agent                 string                    `json:"agent"`
	RemoteID              string                    `json:"remote_id"`
	Cwd                   string                    `json:"cwd"`
	Title                 string                    `json:"title"`
	CreatedAt             time.Time                 `json:"created_at"`
	UpdatedAt             time.Time                 `json:"updated_at"`
	ParentID              string                    `json:"parent_id,omitempty"`
	ForkKind              string                    `json:"fork_kind,omitempty"`
	PendingContext        string                    `json:"pending_context,omitempty"`
	Messages              []Message                 `json:"messages"`
	Commands              []schema.AvailableCommand `json:"commands,omitempty"`
	Usage                 *schema.UsageUpdate       `json:"usage,omitempty"`
	Plan                  *schema.Plan              `json:"plan,omitempty"`
	AdditionalDirectories []string                  `json:"additional_directories,omitempty"`
}

type Store struct{ Directory string }

func NewSession(agent, remoteID, cwd string) Session {
	id := make([]byte, 8)
	_, _ = rand.Read(id)
	now := time.Now().UTC()
	return Session{ID: hex.EncodeToString(id), Agent: agent, RemoteID: remoteID, Cwd: cwd, Title: "New session", CreatedAt: now, UpdatedAt: now, Messages: []Message{}}
}

func (s Store) path(id string) (string, error) {
	if len(id) != 16 {
		return "", fmt.Errorf("invalid local session ID %q", id)
	}
	if _, err := hex.DecodeString(id); err != nil {
		return "", fmt.Errorf("invalid local session ID %q", id)
	}
	return filepath.Join(s.Directory, "sessions", id+".json"), nil
}

func (s Store) Save(session Session) error {
	path, err := s.path(session.ID)
	if err != nil {
		return err
	}
	b, err := json.MarshalIndent(session, "", "  ")
	if err != nil {
		return err
	}
	return config.AtomicWrite(path, append(b, '\n'))
}

func (s Store) Load(id string) (Session, error) {
	var session Session
	path, err := s.path(id)
	if err != nil {
		return session, err
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return session, err
	}
	if err := json.Unmarshal(b, &session); err != nil {
		return session, fmt.Errorf("session %s: %w", id, err)
	}
	if session.ID != id || session.RemoteID == "" {
		return session, fmt.Errorf("invalid session metadata in %s", path)
	}
	return session, nil
}

func (s Store) Delete(id string) error {
	path, err := s.path(id)
	if err != nil {
		return err
	}
	return os.Remove(path)
}

func (s Store) List() ([]Session, error) {
	entries, err := os.ReadDir(filepath.Join(s.Directory, "sessions"))
	if errors.Is(err, os.ErrNotExist) {
		return []Session{}, nil
	}
	if err != nil {
		return nil, err
	}
	sessions := make([]Session, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		session, err := s.Load(strings.TrimSuffix(entry.Name(), ".json"))
		if err != nil {
			return nil, err
		}
		sessions = append(sessions, session)
	}
	sort.Slice(sessions, func(i, j int) bool { return sessions[i].UpdatedAt.After(sessions[j].UpdatedAt) })
	return sessions, nil
}
