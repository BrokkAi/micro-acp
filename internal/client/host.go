package client

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"time"

	acp "github.com/BrokkAi/acp-go"
	"github.com/BrokkAi/acp-go/clienthost"
	"github.com/BrokkAi/acp-go/schema"
	"github.com/BrokkAi/micro-acp/internal/store"
)

type terminalOwner struct {
	host    *clienthost.Host
	id      schema.TerminalId
	session schema.SessionId
	title   string
}

func (c *Client) bindSetupSession(raw json.RawMessage) {
	var scope struct {
		SessionID schema.SessionId `json:"sessionId"`
	}
	if json.Unmarshal(raw, &scope) != nil || scope.SessionID == "" {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.current.ID != "" && c.current.RemoteID == "" {
		c.current.RemoteID = string(scope.SessionID)
		c.wire.SessionID = scope.SessionID
		for _, host := range c.hosts {
			host.SetSession(scope.SessionID)
		}
	}
}

func (c *Client) mcpServers() []schema.McpServer {
	if c.options.MCPServers == nil {
		return []schema.McpServer{}
	}
	return c.options.MCPServers
}
func (c *Client) prepareRoots(roots []string) error {
	for _, root := range roots {
		c.mu.Lock()
		_, exists := c.hosts[root]
		c.mu.Unlock()
		if exists {
			continue
		}
		host, err := clienthost.Open(c.ctx, clienthost.Config{Directory: root, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
		if err != nil {
			return err
		}
		c.mu.Lock()
		if c.ctx.Err() != nil {
			c.mu.Unlock()
			host.Close()
			return c.ctx.Err()
		}
		c.hosts[root] = host
		c.mu.Unlock()
	}
	return nil
}
func (c *Client) hostFor(path string) *clienthost.Host {
	c.mu.Lock()
	defer c.mu.Unlock()
	best := c.host
	length := 0
	roots := append([]string{c.Cwd}, c.current.AdditionalDirectories...)
	for _, root := range roots {
		rel, err := filepath.Rel(root, path)
		if err == nil && (rel == "." || filepath.IsLocal(rel)) && len(root) > length {
			best = c.hosts[root]
			length = len(root)
		}
	}
	return best
}
func (c *Client) hostRequest(ctx context.Context, method string, raw json.RawMessage) (any, error) {
	var scope struct {
		SessionID  schema.SessionId `json:"sessionId"`
		Path       string           `json:"path"`
		Cwd        *string          `json:"cwd"`
		TerminalID string           `json:"terminalId"`
		Command    string           `json:"command"`
		Args       []string         `json:"args"`
	}
	if err := json.Unmarshal(raw, &scope); err != nil {
		return nil, err
	}
	host := c.host
	if scope.Path != "" {
		host = c.hostFor(scope.Path)
	}
	if scope.Cwd != nil {
		host = c.hostFor(*scope.Cwd)
	}
	if scope.TerminalID != "" {
		c.mu.Lock()
		owner, ok := c.terminalHosts[scope.TerminalID]
		c.mu.Unlock()
		if !ok || owner.session != scope.SessionID {
			return nil, &acp.RPCError{Code: -32602, Message: "unknown terminal"}
		}
		host = owner.host
		var fields map[string]json.RawMessage
		_ = json.Unmarshal(raw, &fields)
		fields["terminalId"], _ = json.Marshal(owner.id)
		raw, _ = json.Marshal(fields)
		if method == schema.TerminalReleaseMethodName {
			c.updateTerminal(scope.TerminalID, owner)
		}
	}
	result, err := host.Request(ctx, method, raw)
	if err != nil {
		return nil, err
	}
	if method == schema.TerminalCreateMethodName {
		created := result.(schema.CreateTerminalResponse)
		c.mu.Lock()
		c.terminalSequence++
		id := fmt.Sprintf("terminal-%d-%s", c.terminalSequence, created.TerminalID)
		owner := terminalOwner{host, created.TerminalID, scope.SessionID, strings.Join(append([]string{scope.Command}, scope.Args...), " ")}
		c.terminalHosts[id] = owner
		c.mu.Unlock()
		c.updateTerminal(id, owner)
		go c.pollTerminal(id, owner)
		return schema.CreateTerminalResponse{TerminalID: schema.TerminalId(id)}, nil
	}
	if method == schema.TerminalReleaseMethodName {
		c.mu.Lock()
		delete(c.terminalHosts, scope.TerminalID)
		c.mu.Unlock()
	}
	return result, nil
}
func (c *Client) pollTerminal(id string, owner terminalOwner) {
	timer := time.NewTicker(150 * time.Millisecond)
	defer timer.Stop()
	for {
		select {
		case <-c.ctx.Done():
			return
		case <-timer.C:
			if c.updateTerminal(id, owner) {
				return
			}
		}
	}
}
func (c *Client) updateTerminal(id string, owner terminalOwner) bool {
	raw, _ := json.Marshal(schema.TerminalOutputRequest{SessionID: owner.session, TerminalID: owner.id})
	result, err := owner.host.Request(c.ctx, schema.TerminalOutputMethodName, raw)
	if err != nil {
		return true
	}
	output := result.(schema.TerminalOutputResponse)
	text := owner.title + "\n" + output.Output
	if output.Truncated {
		text += "\n[earlier output truncated]"
	}
	if output.ExitStatus != nil {
		if output.ExitStatus.ExitCode != nil {
			text += fmt.Sprintf("\nExit: %d", *output.ExitStatus.ExitCode)
		}
		if output.ExitStatus.Signal != nil {
			text += "\nSignal: " + *output.ExitStatus.Signal
		}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.current.RemoteID != string(owner.session) {
		return true
	}
	for i := range c.current.Messages {
		m := &c.current.Messages[i]
		if m.Role == "terminal" && m.ID == id {
			if m.Text != text {
				m.Text = text
				c.revision++
			}
			return output.ExitStatus != nil
		}
	}
	c.current.Messages = append(c.current.Messages, store.Message{Role: "terminal", ID: id, Text: text})
	c.revision++
	return output.ExitStatus != nil
}

func (c *Client) CloseSession() error {
	c.op.Lock()
	defer c.op.Unlock()
	w := c.session()
	if err := c.Save(); err != nil {
		return err
	}
	ctx, cancel := c.operation()
	defer cancel()
	if err := c.conn.CloseSession(ctx, c.Init, w.SessionID); err != nil {
		return err
	}
	c.set(store.Session{}, acp.Session{})
	return nil
}
func (c *Client) Logout() error {
	c.op.Lock()
	defer c.op.Unlock()
	if err := c.Save(); err != nil {
		return err
	}
	ctx, cancel := c.operation()
	defer cancel()
	if err := c.conn.Logout(ctx, c.Init); err != nil {
		return err
	}
	c.set(store.Session{}, acp.Session{})
	return nil
}
