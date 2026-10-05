package client

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"time"

	acp "github.com/BrokkAi/acp-go"
	"github.com/BrokkAi/acp-go/schema"
	"github.com/BrokkAi/acp-go/schema/unstable"
	"github.com/BrokkAi/micro-acp/internal/store"
)

// Agents send subagent sessions in three shapes:
//   - the first ACP draft (claude-agent-acp, codex-acp): subagent_spawned
//     with subagentSessionId/name/task, then one subagent_state_update with
//     completed, failed, cancelled or disconnected;
//   - codegraff: subagent_update carrying those same old fields;
//   - the reworked draft (acp-go schema/unstable): subagent_update with
//     sessionId/title/description and a state object, plus session_message
//     and session_message_chunk for messages between sessions.
//
// All map to store.Subagent. Each update arrives on the child's immediate
// parent, which is the root session or another child.

// staleSubagents copies children whose state came from an earlier process:
// work still marked active can no longer be confirmed.
func staleSubagents(children []store.Subagent) []store.Subagent {
	children = slices.Clone(children)
	for i := range children {
		if children[i].Active() {
			children[i].State = store.SubagentUnknown
		}
	}
	return children
}

// subagent returns the announced child with this session ID. Called with mu held.
func (c *Client) subagent(id string) *store.Subagent {
	if id == "" {
		return nil
	}
	for i := range c.current.Subagents {
		if c.current.Subagents[i].ID == id {
			return &c.current.Subagents[i]
		}
	}
	return nil
}

// transcript returns the messages of the root session or an announced child.
// Called with mu held.
func (c *Client) transcript(id string) *[]store.Message {
	if id == c.current.RemoteID {
		return &c.current.Messages
	}
	if child := c.subagent(id); child != nil {
		return &child.Messages
	}
	return nil
}

// knownSession reports whether id is the active session or one of its
// announced children, returning the child's display name. Called with mu held.
func (c *Client) knownSession(id schema.SessionId) (name string, ok bool) {
	if id == "" || c.wire.SessionID == "" {
		return "", false
	}
	if id == c.wire.SessionID {
		return "", true
	}
	if child := c.subagent(string(id)); child != nil {
		return child.Name(), true
	}
	return "", false
}

type subagentFields map[string]json.RawMessage

func (f subagentFields) text(key string) (value string, present bool) {
	raw, ok := f[key]
	if !ok {
		return "", false
	}
	_ = json.Unmarshal(raw, &value)
	return value, true
}

// subagentUpdate applies one announcement or patch received on parent.
// Called with mu held.
func (c *Client) subagentUpdate(parent, kind string, raw json.RawMessage) {
	fields := subagentFields{}
	if json.Unmarshal(raw, &fields) != nil {
		return
	}
	id, legacy := fields.text("subagentSessionId")
	if !legacy {
		id, _ = fields.text("sessionId")
	}
	if id == "" || id == c.current.RemoteID || id == parent {
		return
	}
	child := c.subagent(id)
	if child == nil {
		if kind == "subagent_state_update" {
			// The first draft always announces before it reports an outcome.
			return
		}
		c.current.Subagents = append(c.current.Subagents, store.Subagent{ID: id, ParentID: parent, Messages: []store.Message{}})
		child = &c.current.Subagents[len(c.current.Subagents)-1]
		if legacy {
			child.State = store.SubagentRunning
		}
		if rows := c.transcript(parent); rows != nil {
			*rows = append(*rows, store.Message{Role: "subagent", ID: id})
		}
	} else if child.ParentID != parent {
		// Ownership never moves; only the immediate parent may patch a child.
		return
	}
	if legacy {
		if name, ok := fields.text("name"); ok {
			child.Title = name
		}
		if task, ok := fields.text("task"); ok {
			child.Description = task
		}
		if prompt, ok := fields.text("prompt"); ok && prompt != "" && child.Prompt == "" {
			child.Prompt = prompt
			child.Messages = append(child.Messages, store.Message{Role: "message", Text: prompt, Sender: parent, Recipient: id})
		}
		if state, ok := fields.text("state"); ok && state != "" {
			child.State, child.StopReason = state, ""
		}
	} else {
		if title, ok := fields.text("title"); ok {
			child.Title = title
		}
		if description, ok := fields.text("description"); ok {
			child.Description = description
		}
		if raw, ok := fields["state"]; ok {
			child.State, child.StopReason = "", ""
			var state unstable.StateUpdate
			if hasJSONValue(raw) && json.Unmarshal(raw, &state) == nil {
				switch {
				case state.Running != nil:
					child.State = store.SubagentRunning
				case state.RequiresAction != nil:
					child.State = store.SubagentRequiresAction
				case state.Idle != nil:
					child.State = store.SubagentIdle
					if state.Idle.StopReason != nil {
						child.StopReason = string(*state.Idle.StopReason)
					}
				default:
					child.State = store.SubagentUnknown
				}
			}
		}
	}
	if raw, ok := fields["capabilities"]; ok {
		var capabilities struct {
			Cancel json.RawMessage `json:"cancel"`
		}
		_ = json.Unmarshal(raw, &capabilities)
		// The final draft grants cancel with an object; the first used true.
		child.CanCancel = hasJSONValue(capabilities.Cancel) && !bytes.Equal(bytes.TrimSpace(capabilities.Cancel), []byte("false"))
	}
	if rows := c.transcript(parent); rows != nil {
		for i := len(*rows) - 1; i >= 0; i-- {
			if row := &(*rows)[i]; row.Role == "subagent" && row.ID == id {
				row.Text = child.Name()
				break
			}
		}
	}
}

// sessionMessage applies the reworked draft's messages between sessions to the
// transcript of the session that carries them. Called with mu held.
func (c *Client) sessionMessage(messages *[]store.Message, kind string, raw json.RawMessage) {
	var fields map[string]json.RawMessage
	var update struct {
		Content   json.RawMessage   `json:"content"`
		MessageID schema.MessageId  `json:"messageId"`
		Sender    *schema.SessionId `json:"senderSessionId"`
		Recipient *schema.SessionId `json:"recipientSessionId"`
	}
	if json.Unmarshal(raw, &fields) != nil || json.Unmarshal(raw, &update) != nil || update.MessageID == "" {
		return
	}
	var message *store.Message
	for i := len(*messages) - 1; i >= 0; i-- {
		if m := &(*messages)[i]; m.Role == "message" && m.ID == string(update.MessageID) {
			message = m
			break
		}
	}
	if message == nil {
		*messages = append(*messages, store.Message{Role: "message", ID: string(update.MessageID)})
		message = &(*messages)[len(*messages)-1]
	}
	if update.Sender != nil {
		message.Sender = string(*update.Sender)
	}
	if update.Recipient != nil {
		message.Recipient = string(*update.Recipient)
	}
	var blocks []schema.ContentBlock
	if kind == "session_message_chunk" {
		var block schema.ContentBlock
		if json.Unmarshal(update.Content, &block) != nil {
			return
		}
		blocks = append(append([]schema.ContentBlock(nil), message.Content...), block)
	} else {
		if _, present := fields["content"]; !present {
			return
		}
		// Null or [] clears; a non-empty array replaces all content.
		_ = json.Unmarshal(update.Content, &blocks)
	}
	message.Content = blocks
	var text []string
	for _, block := range blocks {
		text = append(text, ContentText(block))
	}
	message.Text = strings.Join(text, "")
}

// SessionName is the display name for a session ID in the active tree.
func (c *Client) SessionName(id string) string {
	c.mu.Lock()
	defer c.mu.Unlock()
	if id == "" || id == c.current.RemoteID {
		return c.Agent
	}
	if child := c.subagent(id); child != nil {
		return child.Name()
	}
	return id
}

// CancelSubagent stops one child's current work. Agents must grant this per
// child; cancelling the parent turn is the only other way to stop children.
func (c *Client) CancelSubagent(id string) error {
	c.mu.Lock()
	child := c.subagent(id)
	allowed := child != nil && child.CanCancel
	c.mu.Unlock()
	if child == nil {
		return errors.New("unknown subagent")
	}
	if !allowed {
		return errors.New("agent does not allow stopping this subagent on its own; Esc stops the whole turn")
	}
	ctx, cancel := context.WithTimeout(c.ctx, 5*time.Second)
	defer cancel()
	return c.conn.CancelSession(ctx, schema.SessionId(id))
}

// requestSession checks the session of a permission or form request. It
// returns the child's name when a subagent asks.
func (c *Client) requestSession(id schema.SessionId) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	name, ok := c.knownSession(id)
	if !ok {
		return "", &acp.RPCError{Code: -32602, Message: "unknown session"}
	}
	return name, nil
}
