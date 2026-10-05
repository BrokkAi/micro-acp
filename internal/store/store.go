package store

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
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
	// Cancelled is a local display state; ACP v1 has no cancelled tool status.
	Cancelled bool `json:"cancelled,omitempty"`
	// Pending steering is withheld from scrollback and saved history until accepted.
	Pending bool `json:"-"`
	// Sender and Recipient name the sessions of an inter-session message.
	Sender    string `json:"sender,omitempty"`
	Recipient string `json:"recipient,omitempty"`
}

// Subagent is a child session an agent started. Its row in the parent
// transcript is a message with role "subagent" and the child's ID.
type Subagent struct {
	ID          string    `json:"id"`
	ParentID    string    `json:"parent_id"`
	Title       string    `json:"title,omitempty"`
	Description string    `json:"description,omitempty"`
	Prompt      string    `json:"prompt,omitempty"`
	State       string    `json:"state,omitempty"`
	StopReason  string    `json:"stop_reason,omitempty"`
	CanCancel   bool      `json:"can_cancel,omitempty"`
	Messages    []Message `json:"messages"`
}

// Subagent states as reported: the final draft's running, idle,
// requires_action and unknown, and the earlier drafts' outcomes.
const (
	SubagentRunning        = "running"
	SubagentIdle           = "idle"
	SubagentRequiresAction = "requires_action"
	SubagentUnknown        = "unknown"
	SubagentCompleted      = "completed"
	SubagentFailed         = "failed"
	SubagentCancelled      = "cancelled"
	SubagentDisconnected   = "disconnected"
)

// Active reports whether the child is still working or waiting on the user.
func (s Subagent) Active() bool {
	return s.State == SubagentRunning || s.State == SubagentRequiresAction
}

// StateLabel is the short state shown beside a child's name.
func (s Subagent) StateLabel() string {
	switch s.State {
	case SubagentRunning:
		return "running"
	case SubagentRequiresAction:
		return "waiting on you"
	case SubagentIdle:
		switch s.StopReason {
		case "":
			return "idle"
		case "end_turn":
			return "done"
		case "cancelled":
			return "cancelled"
		default:
			return "stopped · " + s.StopReason
		}
	case SubagentCompleted:
		return "done"
	case "":
		return "started"
	default:
		return s.State
	}
}

// Name is the child's title, or a fallback from its ID.
func (s Subagent) Name() string {
	if s.Title != "" {
		return s.Title
	}
	if s.Description != "" {
		return s.Description
	}
	return "Subagent " + s.ID
}

// Generation is the run number encoded in a resumed child's ID
// (`<id>:generation:<n>`), or 0 for a first run.
func (s Subagent) Generation() int {
	i := strings.LastIndex(s.ID, ":generation:")
	if i < 0 {
		return 0
	}
	n := 0
	for _, r := range s.ID[i+len(":generation:"):] {
		if r < '0' || r > '9' {
			return 0
		}
		n = n*10 + int(r-'0')
	}
	return n
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
	Subagents             []Subagent                `json:"subagents,omitempty"`
}

// Clone copies the message and subagent slices so the copy can be read while
// the original keeps changing.
func (s Session) Clone() Session {
	s.Messages = slices.Clone(s.Messages)
	s.Subagents = slices.Clone(s.Subagents)
	for i := range s.Subagents {
		s.Subagents[i].Messages = slices.Clone(s.Subagents[i].Messages)
	}
	return s
}

// Subagent returns the child with the given session ID.
func (s Session) Subagent(id string) (Subagent, bool) {
	for _, child := range s.Subagents {
		if child.ID == id {
			return child, true
		}
	}
	return Subagent{}, false
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
	return s.loadEntries(entries)
}

func (s Store) loadEntries(entries []os.DirEntry) ([]Session, error) {
	sessions := make([]Session, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		session, err := s.Load(strings.TrimSuffix(entry.Name(), ".json"))
		// Another client may delete a session after the directory snapshot.
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		sessions = append(sessions, session)
	}
	sort.Slice(sessions, func(i, j int) bool { return sessions[i].UpdatedAt.After(sessions[j].UpdatedAt) })
	return sessions, nil
}
