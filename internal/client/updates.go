package client

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/BrokkAi/acp-go/schema"
	"github.com/BrokkAi/micro-acp/internal/store"
)

func (c *Client) request(ctx context.Context, method string, raw json.RawMessage) (any, error) {
	c.bindSetupSession(raw)
	if method == schema.ElicitationCreateMethodName {
		return c.elicit(ctx, raw)
	}
	if method != schema.SessionRequestPermissionMethodName {
		return c.hostRequest(ctx, method, raw)
	}
	var request schema.RequestPermissionRequest
	if err := json.Unmarshal(raw, &request); err != nil {
		return nil, err
	}
	subagent, err := c.requestSession(request.SessionID)
	if err != nil {
		return nil, err
	}
	c.mu.Lock()
	permissionCtx := c.permissionCtx
	c.mu.Unlock()
	if permissionCtx == nil {
		permissionCtx = c.ctx
	}
	requestCtx, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(permissionCtx, cancel)
	defer stop()
	defer cancel()
	permissionCtx = requestCtx
	p := Permission{Request: request, Subagent: subagent, Reply: make(chan schema.RequestPermissionOutcome, 1), Done: permissionCtx.Done()}
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
	if method == schema.ElicitationCompleteMethodName {
		return c.elicitationComplete(raw)
	}
	if method != schema.SessionUpdateMethodName {
		return nil
	}
	var envelope struct {
		SessionID string          `json:"sessionId"`
		Update    json.RawMessage `json:"update"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return err
	}
	var kind struct {
		Kind string `json:"sessionUpdate"`
	}
	_ = json.Unmarshal(envelope.Update, &kind)
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.current.ID == "" {
		return nil
	}
	root := c.current.RemoteID == "" || c.current.RemoteID == envelope.SessionID
	if !root && c.subagent(envelope.SessionID) == nil {
		// Updates for sessions this tree never announced are dropped.
		return nil
	}
	if !root {
		c.childUpdate(envelope.SessionID, kind.Kind, envelope.Update)
		return nil
	}
	switch kind.Kind {
	case "subagent_spawned", "subagent_state_update", "subagent_update":
		if c.current.RemoteID != "" {
			c.subagentUpdate(c.current.RemoteID, kind.Kind, envelope.Update)
			c.revision++
		}
		return nil
	case "session_message", "session_message_chunk":
		c.sessionMessage(&c.current.Messages, kind.Kind, envelope.Update)
		c.revision++
		return nil
	}
	// Unknown extension notifications must not terminate a healthy connection.
	var update schema.SessionNotification
	if err := json.Unmarshal(raw, &update); err != nil {
		return nil
	}
	u := update.Update
	if c.turnDone != nil && !c.steering.ready && (u.AgentMessageChunk != nil || u.AgentThoughtChunk != nil || u.ToolCall != nil || u.Plan != nil) {
		c.steering.ready = true
		c.wakeSteering()
	}
	if c.current.RemoteID == "" && update.SessionID != "" {
		c.current.RemoteID = string(update.SessionID)
		c.wire.SessionID = update.SessionID
		for _, host := range c.hosts {
			host.SetSession(update.SessionID)
		}
	}
	marking := c.turnDone != nil && c.cancelRequested
	switch {
	case u.UserMessageChunk != nil:
		if c.replaying {
			applyMessage(&c.current.Messages, u, marking)
		}
	case u.Plan != nil:
		c.current.Plan = u.Plan
		applyMessage(&c.current.Messages, u, marking)
	case u.SessionInfoUpdate != nil:
		c.steeringStatus(u.SessionInfoUpdate.Meta)
		if u.SessionInfoUpdate.Title != nil {
			c.current.Title = *u.SessionInfoUpdate.Title
		}
		if u.SessionInfoUpdate.UpdatedAt != nil {
			if stamp, err := time.Parse(time.RFC3339, *u.SessionInfoUpdate.UpdatedAt); err == nil {
				c.current.UpdatedAt = stamp
			}
		}
	case u.AvailableCommandsUpdate != nil:
		c.current.Commands = u.AvailableCommandsUpdate.AvailableCommands
	case u.UsageUpdate != nil:
		c.current.Usage = u.UsageUpdate
	case u.ConfigOptionUpdate != nil:
		c.wire.ConfigOptions = u.ConfigOptionUpdate.ConfigOptions
	case u.CurrentModeUpdate != nil:
		if c.wire.Modes != nil {
			modes := *c.wire.Modes
			modes.CurrentModeID = u.CurrentModeUpdate.CurrentModeID
			c.wire.Modes = &modes
		}
	default:
		applyMessage(&c.current.Messages, u, marking)
	}
	c.revision++
	return nil
}

// childUpdate applies an update addressed to an announced subagent. Session
// settings belong to the root, so a child only keeps its transcript and its
// own children. Called with mu held.
func (c *Client) childUpdate(id, kind string, raw json.RawMessage) {
	switch kind {
	case "subagent_spawned", "subagent_state_update", "subagent_update":
		c.subagentUpdate(id, kind, raw)
	case "session_message", "session_message_chunk":
		c.sessionMessage(&c.subagent(id).Messages, kind, raw)
	default:
		var u schema.SessionUpdate
		if json.Unmarshal(raw, &u) != nil {
			return
		}
		// A child never echoes this client's prompts, so its user messages
		// are the instructions it received.
		applyMessage(&c.subagent(id).Messages, u, false)
	}
	c.revision++
}

// applyMessage adds transcript content from one update: message chunks, tool
// calls and plans. cancelled marks unfinished tools from a stopping turn.
func applyMessage(messages *[]store.Message, u schema.SessionUpdate, cancelled bool) {
	chunk := func(role string, ch *schema.ContentChunk) {
		text := ContentText(ch.Content)
		id := ""
		if ch.MessageID != nil {
			id = string(*ch.MessageID)
		}
		last := len(*messages) - 1
		if last >= 0 && (*messages)[last].Role == role && (*messages)[last].ID == id {
			(*messages)[last].Text += text
		} else {
			*messages = append(*messages, store.Message{Role: role, Text: text, ID: id})
		}
		if ch.Content.Text == nil {
			i := len(*messages) - 1
			(*messages)[i].Content = append(append([]schema.ContentBlock(nil), (*messages)[i].Content...), ch.Content)
		}
	}
	switch {
	case u.AgentMessageChunk != nil:
		chunk("assistant", u.AgentMessageChunk)
	case u.UserMessageChunk != nil:
		chunk("user", u.UserMessageChunk)
	case u.AgentThoughtChunk != nil:
		chunk("thought", u.AgentThoughtChunk)
	case u.ToolCall != nil:
		t := u.ToolCall
		*messages = append(*messages, store.Message{Role: "tool", ID: string(t.ToolCallID), Text: ToolText(*t), Tool: t, Cancelled: cancelled && unfinishedTool(t)})
	case u.ToolCallUpdate != nil:
		t := u.ToolCallUpdate
		for i := len(*messages) - 1; i >= 0; i-- {
			m := &(*messages)[i]
			if m.Role == "tool" && m.ID == string(t.ToolCallID) {
				previous := schema.ToolCall{ToolCallID: t.ToolCallID, Title: m.Text}
				if m.Tool != nil {
					previous = *m.Tool
				}
				updated := mergeTool(previous, *t)
				m.Tool = &updated
				m.Text = ToolText(updated)
				m.Cancelled = m.Cancelled && unfinishedTool(&updated)
				return
			}
		}
		updated := mergeTool(schema.ToolCall{ToolCallID: t.ToolCallID}, *t)
		*messages = append(*messages, store.Message{Role: "tool", ID: string(t.ToolCallID), Text: ToolText(updated), Tool: &updated, Cancelled: cancelled && unfinishedTool(&updated)})
	case u.Plan != nil:
		var lines []string
		for _, entry := range u.Plan.Entries {
			lines = append(lines, string(entry.Status)+"  ["+string(entry.Priority)+"] "+entry.Content)
		}
		*messages = append(*messages, store.Message{Role: "plan", Text: strings.Join(lines, "\n")})
	}
}
