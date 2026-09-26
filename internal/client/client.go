// Package client owns one long-lived ACP process and its active conversation.
package client

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	acp "github.com/BrokkAi/acp-go"
	"github.com/BrokkAi/acp-go/clienthost"
	"github.com/BrokkAi/acp-go/schema"
	"github.com/BrokkAi/acp-go/schema/unstable"
	"github.com/BrokkAi/micro-acp/internal/config"
	"github.com/BrokkAi/micro-acp/internal/store"
)

type Permission struct {
	Request schema.RequestPermissionRequest
	Reply   chan schema.RequestPermissionOutcome
	Done    <-chan struct{}
}

type Client struct {
	Agent, Cwd       string
	Init             acp.Initialization
	CanFork          bool
	Permissions      chan Permission
	Elicitations     chan Elicitation
	urlElicitations  map[schema.ElicitationId]bool
	command          config.Command
	store            store.Store
	ctx              context.Context
	cancel           context.CancelFunc
	conn             *acp.Connection
	host             *clienthost.Host
	hosts            map[string]*clienthost.Host
	terminalHosts    map[string]terminalOwner
	terminalSequence uint64
	options          acp.NewSessionOptions
	cmd              *exec.Cmd
	wait             chan struct{}
	closeOnce        sync.Once
	shutdownOnce     sync.Once
	shutdownErr      error
	op               sync.Mutex
	mu               sync.Mutex
	current          store.Session
	wire             acp.Session
	revision         uint64
	replaying        bool
	turnCancel       context.CancelFunc
	turnDone         chan struct{}
	permissionCtx    context.Context
	permissionCancel context.CancelFunc
	cancelRequested  bool
	stderr           tail
}

// Open starts an argv command directly; no shell interpretation is performed.
func Open(parent context.Context, agent, cwd string, command config.Command, sessions store.Store, settings ...config.SessionOptions) (*Client, error) {
	return OpenInteractive(parent, agent, cwd, command, sessions, Interactions{}, settings...)
}

// Interactions lets a UI handle callbacks even before initialization completes.
type Interactions struct {
	Permissions  chan Permission
	Elicitations chan Elicitation
}

func OpenInteractive(parent context.Context, agent, cwd string, command config.Command, sessions store.Store, interactions Interactions, settings ...config.SessionOptions) (*Client, error) {
	ctx, cancel := context.WithCancel(parent)
	c := &Client{Agent: agent, Cwd: cwd, store: sessions, ctx: ctx, cancel: cancel, Permissions: make(chan Permission, 32), wait: make(chan struct{})}
	c.command = command
	c.Elicitations = make(chan Elicitation, 32)
	if interactions.Permissions != nil {
		c.Permissions = interactions.Permissions
	}
	if interactions.Elicitations != nil {
		c.Elicitations = interactions.Elicitations
	}
	c.urlElicitations = map[schema.ElicitationId]bool{}
	host, err := clienthost.Open(ctx, clienthost.Config{Directory: cwd, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	if err != nil {
		cancel()
		return nil, err
	}
	c.host = host
	c.hosts = map[string]*clienthost.Host{cwd: host}
	c.terminalHosts = map[string]terminalOwner{}
	if len(settings) > 0 {
		c.options.MCPServers = settings[0].MCPServers
		for _, root := range settings[0].AdditionalDirectories {
			if !filepath.IsAbs(root) {
				root = filepath.Join(cwd, root)
			}
			c.options.AdditionalDirectories = append(c.options.AdditionalDirectories, filepath.Clean(root))
		}
	}
	c.cmd = exec.CommandContext(ctx, command.Command, command.Args...)
	c.cmd.Dir = cwd
	c.cmd.Env = os.Environ()
	for key, value := range command.Env {
		c.cmd.Env = append(c.cmd.Env, key+"="+value)
	}
	configureProcess(c.cmd)
	c.cmd.Stderr = &c.stderr
	stdin, err := c.cmd.StdinPipe()
	if err != nil {
		host.Close()
		cancel()
		return nil, err
	}
	stdout, stdoutWriter, err := os.Pipe()
	if err != nil {
		stdin.Close()
		host.Close()
		cancel()
		return nil, err
	}
	c.cmd.Stdout = stdoutWriter
	if err := c.cmd.Start(); err != nil {
		stdin.Close()
		stdout.Close()
		stdoutWriter.Close()
		host.Close()
		cancel()
		return nil, err
	}
	_ = stdoutWriter.Close()
	c.conn = acp.Connect(stdout, stdin, c.request, c.notification)
	go func() { _ = c.cmd.Wait(); close(c.wait) }()
	go func() { <-ctx.Done(); c.Close() }()
	setup, stop := context.WithTimeout(ctx, 45*time.Second)
	defer stop()
	// Keep the raw handshake to inspect the opt-in fork capability, which the stable
	// facade intentionally omits. Both representations come from acp-go's schemas.
	var raw json.RawMessage
	caps := acp.WorkspaceCapabilities(true, true, true)
	caps.Session = acp.ConfigOptionsClientCapabilities(true)
	caps.Elicitation = acp.ElicitationClientCapabilities(true, true)
	terminalAuth := true
	caps.Auth = &schema.AuthCapabilities{Terminal: &terminalAuth}
	err = c.conn.Call(setup, schema.InitializeMethodName, schema.InitializeRequest{
		ProtocolVersion: acp.Version, ClientCapabilities: &caps,
		ClientInfo: &schema.Implementation{Name: "micro-acp", Version: "0.1.0"},
	}, &raw)
	if err == nil {
		err = json.Unmarshal(raw, &c.Init)
	}
	if err == nil && c.Init.ProtocolVersion != acp.Version {
		err = fmt.Errorf("agent selected unsupported ACP version %d", c.Init.ProtocolVersion)
	}
	if err != nil {
		c.Close()
		return nil, fmt.Errorf("initialize %s: %w%s", agent, err, c.Diagnostics())
	}
	var extended unstable.InitializeResponse
	if json.Unmarshal(raw, &extended) == nil && extended.AgentCapabilities != nil && extended.AgentCapabilities.SessionCapabilities != nil {
		c.CanFork = extended.AgentCapabilities.SessionCapabilities.Fork != nil
	}
	return c, nil
}

func (c *Client) Close() {
	c.closeOnce.Do(func() {
		c.cancel()
		killProcess(c.cmd)
		_ = c.conn.Close()
		c.host.Close()
		c.mu.Lock()
		hosts := make([]*clienthost.Host, 0, len(c.hosts))
		for _, host := range c.hosts {
			if host != c.host {
				hosts = append(hosts, host)
			}
		}
		c.mu.Unlock()
		for _, host := range hosts {
			host.Close()
		}
		<-c.wait
	})
}

func (c *Client) Diagnostics() string {
	s := strings.TrimSpace(c.stderr.String())
	if s == "" {
		return ""
	}
	return "\nAgent stderr: " + s
}

func (c *Client) Snapshot() (store.Session, uint64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	s := c.current
	s.Messages = append([]store.Message(nil), s.Messages...)
	return s, c.revision
}
func (c *Client) Save() error {
	s, _ := c.Snapshot()
	if s.ID == "" || s.RemoteID == "" {
		return nil
	}
	return c.store.Save(s)
}

// Shutdown joins the active operation after disconnecting, including its final save.
func (c *Client) Shutdown() error {
	c.shutdownOnce.Do(func() {
		c.Close()
		c.op.Lock()
		defer c.op.Unlock()
		c.shutdownErr = c.Save()
	})
	return c.shutdownErr
}
func (c *Client) session() acp.Session {
	c.mu.Lock()
	defer c.mu.Unlock()
	b, _ := json.Marshal(c.wire)
	var snapshot acp.Session
	_ = json.Unmarshal(b, &snapshot)
	return snapshot
}
func (c *Client) set(s store.Session, w acp.Session) {
	c.mu.Lock()
	changing := c.wire.SessionID != w.SessionID
	c.mu.Unlock()
	if changing {
		c.releaseTerminals()
	}
	c.mu.Lock()
	c.current = s
	c.wire = w
	c.revision++
	c.mu.Unlock()
	c.mu.Lock()
	for _, host := range c.hosts {
		host.SetSession(w.SessionID)
	}
	c.mu.Unlock()
}
func (c *Client) operation() (context.Context, context.CancelFunc) {
	return context.WithTimeout(c.ctx, 45*time.Second)
}

func (c *Client) New() error {
	c.op.Lock()
	defer c.op.Unlock()
	if err := c.Save(); err != nil {
		return err
	}
	ctx, cancel := c.operation()
	defer cancel()
	previous, _ := c.Snapshot()
	previousWire := c.session()
	if err := c.prepareRoots(c.options.AdditionalDirectories); err != nil {
		return err
	}
	next := store.NewSession(c.Agent, "", c.Cwd)
	next.AdditionalDirectories = append([]string(nil), c.options.AdditionalDirectories...)
	c.set(next, acp.Session{})
	w, err := c.conn.NewSessionWithOptions(ctx, c.Init, c.Cwd, c.options)
	if err != nil {
		c.set(previous, previousWire)
		return err
	}
	s, _ := c.Snapshot()
	s.RemoteID = string(w.SessionID)
	c.set(s, w)
	return c.Save()
}

func (c *Client) Load(s store.Session) error {
	c.op.Lock()
	defer c.op.Unlock()
	if err := c.Save(); err != nil {
		return err
	}
	if s.Agent != c.Agent || s.Cwd != c.Cwd {
		return errors.New("session belongs to another agent or workspace; reopen with its --agent and --cwd")
	}
	ctx, cancel := c.operation()
	defer cancel()
	previous, _ := c.Snapshot()
	previousWire := c.session()
	backup := s
	if err := c.prepareRoots(s.AdditionalDirectories); err != nil {
		return err
	}
	w := acp.Session{SessionID: schema.SessionId(s.RemoteID)}
	cap := c.sessionCapabilities()
	resume := cap != nil && cap.Resume != nil
	// Remote listings carry no transcript. Prefer history replay when there is
	// no local history and the agent supports both load and resume.
	if len(s.Messages) == 0 && c.Init.AgentCapabilities != nil && c.Init.AgentCapabilities.LoadSession != nil && *c.Init.AgentCapabilities.LoadSession {
		resume = false
	}
	if !resume {
		s.Messages = nil
	}
	c.set(s, w)
	c.mu.Lock()
	c.replaying = true
	c.mu.Unlock()
	defer func() { c.mu.Lock(); c.replaying = false; c.mu.Unlock() }()
	var err error
	if resume {
		var response schema.ResumeSessionResponse
		response, err = c.conn.ResumeSession(ctx, c.Init, schema.ResumeSessionRequest{SessionID: w.SessionID, Cwd: c.Cwd, MCPServers: c.mcpServers(), AdditionalDirectories: s.AdditionalDirectories})
		w.Modes = response.Modes
		w.ConfigOptions = response.ConfigOptions
	} else {
		var response schema.LoadSessionResponse
		response, err = c.conn.LoadSession(ctx, c.Init, schema.LoadSessionRequest{SessionID: w.SessionID, Cwd: c.Cwd, MCPServers: c.mcpServers(), AdditionalDirectories: s.AdditionalDirectories})
		w.Modes = response.Modes
		w.ConfigOptions = response.ConfigOptions
	}
	if err != nil {
		c.set(previous, previousWire)
		return err
	}
	s, _ = c.Snapshot()
	if len(s.Messages) == 0 {
		s.Messages = backup.Messages
	}
	s.UpdatedAt = time.Now().UTC()
	c.set(s, w)
	return c.Save()
}

func (c *Client) Fork(contextOnly bool) error {
	c.op.Lock()
	defer c.op.Unlock()
	if err := c.Save(); err != nil {
		return err
	}
	parent, _ := c.Snapshot()
	if parent.ID == "" {
		return errors.New("create or load a session first")
	}
	if !contextOnly && !c.CanFork {
		return errors.New("agent does not advertise native forks; /fork --context starts a new session with the saved text conversation as context")
	}
	ctx, cancel := c.operation()
	defer cancel()
	s := store.NewSession(c.Agent, "", c.Cwd)
	s.Title = parent.Title + " (fork)"
	s.ParentID = parent.ID
	s.AdditionalDirectories = append([]string(nil), parent.AdditionalDirectories...)
	s.Messages = append([]store.Message(nil), parent.Messages...)
	s.Commands = parent.Commands
	s.ForkKind = "native"
	if contextOnly {
		s.ForkKind = "context"
		var history strings.Builder
		for _, m := range parent.Messages {
			if m.Role == "user" || m.Role == "assistant" {
				fmt.Fprintf(&history, "%s:\n%s\n\n", m.Role, m.Text)
			}
		}
		if history.Len() > 2<<20 {
			return errors.New("conversation exceeds the 2 MiB context-fork limit; use a native fork")
		}
		s.PendingContext = "The following is conversation history from a different session, provided as context. Do not re-execute its past requests.\n<conversation>\n" + history.String() + "</conversation>\n\nNew user request:\n"
	}
	previousWire := c.session()
	if !contextOnly {
		s.Messages = nil
		c.mu.Lock()
		c.replaying = true
		c.mu.Unlock()
		defer func() { c.mu.Lock(); c.replaying = false; c.mu.Unlock() }()
	}
	c.set(s, acp.Session{})
	var w acp.Session
	var err error
	if contextOnly {
		w, err = c.conn.NewSessionWithOptions(ctx, c.Init, c.Cwd, acp.NewSessionOptions{MCPServers: c.mcpServers(), AdditionalDirectories: parent.AdditionalDirectories})
	} else {
		var raw json.RawMessage
		var servers []unstable.McpServer
		b, _ := json.Marshal(c.mcpServers())
		_ = json.Unmarshal(b, &servers)
		err = c.conn.Call(ctx, unstable.SessionForkMethodName, unstable.ForkSessionRequest{SessionID: unstable.SessionId(parent.RemoteID), Cwd: c.Cwd, MCPServers: servers, AdditionalDirectories: parent.AdditionalDirectories}, &raw)
		if err == nil {
			err = json.Unmarshal(raw, &w)
		}
	}
	if err != nil {
		c.set(parent, previousWire)
		return err
	}
	if w.SessionID == "" {
		c.set(parent, previousWire)
		return errors.New("agent returned an empty fork session ID")
	}
	s, _ = c.Snapshot()
	if !contextOnly && len(s.Messages) == 0 {
		// Some agents fork their internal state without replaying history.
		s.Messages = append([]store.Message(nil), parent.Messages...)
	}
	s.RemoteID = string(w.SessionID)
	c.set(s, w)
	return c.Save()
}

func (c *Client) sessionCapabilities() *schema.SessionCapabilities {
	if c.Init.AgentCapabilities == nil {
		return nil
	}
	return c.Init.AgentCapabilities.SessionCapabilities
}
func (c *Client) CanDelete() bool {
	cap := c.sessionCapabilities()
	return cap != nil && cap.Delete != nil
}

func (c *Client) CanLoad() bool {
	cap := c.sessionCapabilities()
	return (cap != nil && cap.Resume != nil) || (c.Init.AgentCapabilities != nil && c.Init.AgentCapabilities.LoadSession != nil && *c.Init.AgentCapabilities.LoadSession)
}

func (c *Client) Sessions() ([]store.Session, error) {
	c.op.Lock()
	defer c.op.Unlock()
	all, err := c.store.List()
	if err != nil {
		return nil, err
	}
	var sessions []store.Session
	seen := map[string]bool{}
	for _, s := range all {
		if s.Agent == c.Agent && s.Cwd == c.Cwd {
			sessions = append(sessions, s)
			seen[s.RemoteID] = true
		}
	}
	cap := c.sessionCapabilities()
	if cap == nil || cap.List == nil {
		return sessions, nil
	}
	ctx, cancel := c.operation()
	defer cancel()
	var cursor *string
	cursors := map[string]bool{}
	for page := 0; page < 100; page++ {
		response, err := c.conn.ListSessions(ctx, c.Init, schema.ListSessionsRequest{Cwd: &c.Cwd, Cursor: cursor})
		if err != nil {
			return sessions, err
		}
		for _, remote := range response.Sessions {
			id := string(remote.SessionID)
			if seen[id] || remote.Cwd != c.Cwd || id == "" {
				continue
			}
			s := store.NewSession(c.Agent, id, remote.Cwd)
			s.AdditionalDirectories = remote.AdditionalDirectories
			if remote.Title != nil {
				s.Title = *remote.Title
			}
			if remote.UpdatedAt != nil {
				if t, err := time.Parse(time.RFC3339, *remote.UpdatedAt); err == nil {
					s.UpdatedAt = t
				}
			}
			sessions = append(sessions, s)
			seen[id] = true
		}
		cursor = response.NextCursor
		if cursor == nil || *cursor == "" {
			break
		}
		if cursors[*cursor] {
			return sessions, errors.New("agent repeated a session pagination cursor")
		}
		cursors[*cursor] = true
		if page == 99 {
			return sessions, errors.New("agent session list exceeds 100 pages")
		}
	}
	sort.Slice(sessions, func(i, j int) bool { return sessions[i].UpdatedAt.After(sessions[j].UpdatedAt) })
	return sessions, nil
}

func (c *Client) Delete(s store.Session, localOnly bool) error {
	c.op.Lock()
	defer c.op.Unlock()
	if s.Agent != c.Agent || s.Cwd != c.Cwd {
		return errors.New("session belongs to another agent or workspace")
	}
	if !localOnly {
		ctx, cancel := c.operation()
		defer cancel()
		if err := c.conn.DeleteSession(ctx, c.Init, schema.SessionId(s.RemoteID)); err != nil {
			return err
		}
	}
	err := c.store.Delete(s.ID)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	active, _ := c.Snapshot()
	if active.ID == s.ID {
		c.set(store.Session{}, acp.Session{})
	}
	return nil
}

func (c *Client) Prompt(text string) (schema.StopReason, error) {
	return c.PromptContent([]acp.Content{acp.NewTextContent(text)})
}

func (c *Client) PromptContent(blocks []acp.Content) (reason schema.StopReason, err error) {
	c.op.Lock()
	defer c.op.Unlock()
	if err := c.validateContent(blocks); err != nil {
		return "", err
	}
	var display []string
	for _, block := range blocks {
		display = append(display, ContentText(block))
	}
	text := strings.Join(display, "\n")
	s, _ := c.Snapshot()
	if s.ID == "" {
		return "", errors.New("create or load a session first with /new or /sessions")
	}
	ctx, cancel := context.WithCancel(c.ctx)
	permissions, stopPermissions := context.WithCancel(c.ctx)
	done := make(chan struct{})
	c.mu.Lock()
	c.turnCancel = cancel
	c.turnDone = done
	c.permissionCtx = permissions
	c.permissionCancel = stopPermissions
	c.cancelRequested = false
	c.current.Messages = append(c.current.Messages, store.Message{Role: "user", Text: text, Content: blocks})
	if c.current.Title == "New session" {
		title := []rune(strings.Join(strings.Fields(text), " "))
		c.current.Title = string(title[:min(len(title), 70)])
	}
	c.current.UpdatedAt = time.Now().UTC()
	c.revision++
	c.mu.Unlock()
	defer func() {
		cancel()
		stopPermissions()
		close(done)
		c.mu.Lock()
		c.turnCancel = nil
		c.turnDone = nil
		c.permissionCtx = nil
		c.permissionCancel = nil
		c.current.UpdatedAt = time.Now().UTC()
		c.revision++
		c.mu.Unlock()
		if saveErr := c.Save(); saveErr != nil {
			err = errors.Join(err, saveErr)
		}
	}()
	if err = c.Save(); err != nil {
		return "", err
	}
	if s.PendingContext != "" {
		blocks = append([]acp.Content{acp.NewTextContent(s.PendingContext)}, blocks...)
	}
	reason, err = c.conn.PromptContent(ctx, c.Init, c.session(), blocks)
	c.mu.Lock()
	cancelled := c.cancelRequested
	c.mu.Unlock()
	var rpcErr *acp.RPCError
	if cancelled && errors.As(err, &rpcErr) && rpcErr.Code == -32800 {
		reason, err = schema.StopReasonCancelled, nil
	}
	if err == nil {
		c.mu.Lock()
		c.current.PendingContext = ""
		c.mu.Unlock()
	}
	return reason, err
}

// Cancel waits for the protocol's turn completion. An unresponsive agent is
// disconnected after a grace period, so another prompt cannot overlap it.
func (c *Client) Cancel() error {
	c.mu.Lock()
	done := c.turnDone
	stop := c.turnCancel
	stopPermissions := c.permissionCancel
	if c.turnDone != nil {
		c.cancelRequested = true
	}
	id := c.wire.SessionID
	c.mu.Unlock()
	if done == nil {
		return nil
	}
	if stopPermissions != nil {
		stopPermissions()
	}
	ctx, cancel := context.WithTimeout(c.ctx, time.Second)
	defer cancel()
	err := c.conn.CancelSession(ctx, id)
	go func() {
		timer := time.NewTimer(3 * time.Second)
		defer timer.Stop()
		select {
		case <-done:
		case <-timer.C:
			stop()
			c.Close()
		}
	}()
	return err
}

func (c *Client) Authenticate(method string) error {
	c.op.Lock()
	defer c.op.Unlock()
	ctx, cancel := context.WithTimeout(c.ctx, 3*time.Minute)
	defer cancel()
	return c.conn.Authenticate(ctx, c.Init, method)
}
func (c *Client) Details() string {
	w := c.session()
	info := struct {
		Agent        *schema.Implementation    `json:"agent"`
		Capabilities *schema.AgentCapabilities `json:"capabilities"`
		NativeFork   bool                      `json:"native_fork"`
		Auth         []schema.AuthMethod       `json:"authentication"`
		Session      acp.Session               `json:"session"`
	}{c.Init.AgentInfo, c.Init.AgentCapabilities, c.CanFork, c.Init.AuthMethods, w}
	b, _ := json.MarshalIndent(info, "", "  ")
	return string(b)
}

type tail struct {
	mu   sync.Mutex
	text string
}

func (t *tail) Write(b []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.text += string(b)
	if len(t.text) > 8192 {
		t.text = t.text[len(t.text)-8192:]
	}
	return len(b), nil
}
func (t *tail) String() string { t.mu.Lock(); defer t.mu.Unlock(); return t.text }
