package client

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	acp "github.com/BrokkAi/acp-go"
	"github.com/BrokkAi/acp-go/schema"
	"github.com/BrokkAi/micro-acp/internal/store"
)

func (c *Client) request(ctx context.Context, method string, raw json.RawMessage) (any, error) {
	if method != schema.SessionRequestPermissionMethodName {
		return c.host.Request(ctx, method, raw)
	}
	var request schema.RequestPermissionRequest
	if err := json.Unmarshal(raw, &request); err != nil {
		return nil, err
	}
	c.mu.Lock()
	id := c.wire.SessionID
	permissionCtx := c.permissionCtx
	c.mu.Unlock()
	if id == "" || id != request.SessionID {
		return nil, &acp.RPCError{Code: -32602, Message: "unknown session"}
	}
	if permissionCtx == nil {
		permissionCtx = c.ctx
	}
	requestCtx, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(permissionCtx, cancel)
	defer stop()
	defer cancel()
	permissionCtx = requestCtx
	p := Permission{Request: request, Reply: make(chan schema.RequestPermissionOutcome, 1), Done: permissionCtx.Done()}
	cancelled := schema.RequestPermissionResponse{Outcome: schema.RequestPermissionOutcome{Cancelled: &schema.RequestPermissionOutcomeCancelled{}}}
	select {
	case c.Permissions <- p:
	case <-ctx.Done():
		return cancelled, nil
	case <-permissionCtx.Done():
		return cancelled, nil
	}
	select {
	case outcome := <-p.Reply:
		return schema.RequestPermissionResponse{Outcome: outcome}, nil
	case <-ctx.Done():
		return cancelled, nil
	case <-permissionCtx.Done():
		return cancelled, nil
	}
}

func (c *Client) notification(method string, raw json.RawMessage) error {
	if method != schema.SessionUpdateMethodName {
		return nil
	}
	var envelope struct {
		SessionID string `json:"sessionId"`
		Update    struct {
			Kind string `json:"sessionUpdate"`
		} `json:"update"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return err
	}
	// Unknown extension notifications must not terminate a healthy connection.
	var update schema.SessionNotification
	if err := json.Unmarshal(raw, &update); err != nil {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.current.ID == "" || (c.current.RemoteID != "" && c.current.RemoteID != envelope.SessionID) {
		return nil
	}
	u := update.Update
	chunk := func(role string, ch *schema.ContentChunk) {
		text := ""
		if ch.Content.Text != nil {
			text = ch.Content.Text.Text
		} else {
			text = "[non-text content]"
		}
		id := ""
		if ch.MessageID != nil {
			id = string(*ch.MessageID)
		}
		last := len(c.current.Messages) - 1
		if last >= 0 && c.current.Messages[last].Role == role && c.current.Messages[last].ID == id {
			c.current.Messages[last].Text += text
		} else {
			c.current.Messages = append(c.current.Messages, store.Message{Role: role, Text: text, ID: id})
		}
	}
	switch {
	case u.AgentMessageChunk != nil:
		chunk("assistant", u.AgentMessageChunk)
	case u.UserMessageChunk != nil:
		if c.replaying {
			chunk("user", u.UserMessageChunk)
		}
	case u.AgentThoughtChunk != nil:
		chunk("thought", u.AgentThoughtChunk)
	case u.ToolCall != nil:
		t := u.ToolCall
		status := "pending"
		if t.Status != nil {
			status = string(*t.Status)
		}
		c.current.Messages = append(c.current.Messages, store.Message{Role: "tool", ID: string(t.ToolCallID), Text: fmt.Sprintf("%s · %s", t.Title, status)})
	case u.ToolCallUpdate != nil:
		t := u.ToolCallUpdate
		for i := len(c.current.Messages) - 1; i >= 0; i-- {
			m := &c.current.Messages[i]
			if m.Role == "tool" && m.ID == string(t.ToolCallID) {
				if t.Title != nil {
					m.Text = *t.Title
				}
				if t.Status != nil {
					m.Text = strings.Split(m.Text, " · ")[0] + " · " + string(*t.Status)
				}
				break
			}
		}
	case u.Plan != nil:
		var lines []string
		for _, entry := range u.Plan.Entries {
			lines = append(lines, string(entry.Status)+"  "+entry.Content)
		}
		c.current.Messages = append(c.current.Messages, store.Message{Role: "plan", Text: strings.Join(lines, "\n")})
	case u.SessionInfoUpdate != nil:
		if u.SessionInfoUpdate.Title != nil {
			c.current.Title = *u.SessionInfoUpdate.Title
		}
	case u.ConfigOptionUpdate != nil:
		c.wire.ConfigOptions = u.ConfigOptionUpdate.ConfigOptions
	case u.CurrentModeUpdate != nil:
		if c.wire.Modes != nil {
			modes := *c.wire.Modes
			modes.CurrentModeID = u.CurrentModeUpdate.CurrentModeID
			c.wire.Modes = &modes
		}
	}
	c.revision++
	return nil
}
