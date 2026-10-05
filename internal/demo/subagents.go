package demo

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/BrokkAi/acp-go/agent"
	"github.com/BrokkAi/acp-go/schema"
	"github.com/BrokkAi/micro-acp/internal/store"
)

// The demo's subagents use the reworked ACP subagent draft: subagent_update
// on the parent announces and patches a child, and the child's own updates
// carry its session ID.

func subagentUpdate(id schema.SessionId, fields map[string]any) map[string]any {
	update := map[string]any{"sessionUpdate": "subagent_update", "sessionId": id}
	for key, value := range fields {
		update[key] = value
	}
	return update
}

func idleState(reason schema.StopReason) map[string]any {
	return map[string]any{"state": "idle", "stopReason": reason}
}

func textChunk(kind, text string) map[string]any {
	return map[string]any{"sessionUpdate": kind, "content": map[string]any{"type": "text", "text": text}}
}

// CancelSession stops one demo subagent. The runtime cancels prompts itself.
func (a *Agent) CancelSession(_ context.Context, n schema.CancelNotification) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if cancel := a.children[n.SessionID]; cancel != nil {
		cancel()
	}
	return nil
}

// runSubagent starts a child that asks for permission, starts a nested child,
// and streams a report. It returns the parent's answer and the saved child.
func (a *Agent) runSubagent(ctx context.Context, c agent.Client, parent schema.SessionId, stream bool) (string, []store.Subagent, error) {
	a.mu.Lock()
	if a.children == nil {
		a.children = map[schema.SessionId]context.CancelFunc{}
	}
	a.sequence++
	id := schema.SessionId(fmt.Sprintf("%s-subagent-%d", parent, a.sequence))
	childCtx, cancel := context.WithCancel(ctx)
	a.children[id] = cancel
	a.mu.Unlock()
	defer func() {
		a.mu.Lock()
		delete(a.children, id)
		a.mu.Unlock()
		cancel()
	}()
	// Keep reporting after a stop so the client sees the child's final state.
	notifyCtx := context.WithoutCancel(ctx)
	notify := func(session schema.SessionId, update map[string]any) error {
		return c.Notify(notifyCtx, schema.SessionUpdateMethodName, map[string]any{"sessionId": session, "update": update})
	}
	child := store.Subagent{ID: string(id), ParentID: string(parent), Title: "Survey the workspace", Description: "Look around and report back", Messages: []store.Message{}}
	nested := store.Subagent{ID: string(id) + "-readme", ParentID: string(id), Title: "Check the README", Messages: []store.Message{{Role: "assistant", Text: "The README explains how to build and run micro-acp."}}}
	steps := []struct {
		session schema.SessionId
		update  map[string]any
	}{
		{parent, subagentUpdate(id, map[string]any{"title": child.Title, "description": child.Description, "state": map[string]any{"state": "running"}, "capabilities": map[string]any{"cancel": map[string]any{}}})},
		{id, map[string]any{"sessionUpdate": "session_message", "messageId": "brief", "senderSessionId": parent, "recipientSessionId": id, "content": []any{map[string]any{"type": "text", "text": "Look around the workspace and summarize it."}}}},
		{id, textChunk("agent_thought_chunk", "Planning a quick look around. This is demo data; nothing is read.")},
		{id, map[string]any{"sessionUpdate": "tool_call", "toolCallId": "survey", "title": "List workspace files", "kind": "search", "status": "in_progress"}},
		{id, subagentUpdate(schema.SessionId(nested.ID), map[string]any{"title": nested.Title, "state": map[string]any{"state": "running"}})},
		{schema.SessionId(nested.ID), textChunk("agent_message_chunk", nested.Messages[0].Text)},
		{id, subagentUpdate(schema.SessionId(nested.ID), map[string]any{"state": idleState(schema.StopReasonEndTurn)})},
		{parent, subagentUpdate(id, map[string]any{"state": map[string]any{"state": "requires_action"}})},
	}
	for _, step := range steps {
		if err := notify(step.session, step.update); err != nil {
			return "", nil, err
		}
	}
	nested.State, nested.StopReason = store.SubagentIdle, string(schema.StopReasonEndTurn)
	title := "Let the subagent list file names (demo; nothing is read)"
	response, err := c.RequestPermission(childCtx, schema.RequestPermissionRequest{SessionID: id, ToolCall: schema.ToolCallUpdate{ToolCallID: "survey", Title: &title}, Options: []schema.PermissionOption{{OptionID: "allow", Name: "Allow once", Kind: schema.PermissionOptionKindAllowOnce}, {OptionID: "reject", Name: "Reject", Kind: schema.PermissionOptionKindRejectOnce}}})
	if err != nil && childCtx.Err() == nil {
		return "", nil, err
	}
	allowed := err == nil && response.Outcome.Selected != nil && response.Outcome.Selected.OptionID == "allow"
	if err := notify(parent, subagentUpdate(id, map[string]any{"state": map[string]any{"state": "running"}})); err != nil {
		return "", nil, err
	}
	status, report := "failed", "Permission was not granted, so there is nothing to report."
	if allowed {
		status = "completed"
		report = strings.Repeat("Demo report: README.md, main.go and go.mod would be listed here. ", 6) + "Done."
	}
	if err := notify(id, map[string]any{"sessionUpdate": "tool_call_update", "toolCallId": "survey", "status": status}); err != nil {
		return "", nil, err
	}
	delay := time.Duration(0)
	if stream {
		delay = 20 * time.Millisecond
	}
	reason := schema.StopReasonEndTurn
	var written strings.Builder
	for _, word := range strings.SplitAfter(report, " ") {
		select {
		case <-childCtx.Done():
			reason = schema.StopReasonCancelled
		case <-time.After(delay):
		}
		if reason == schema.StopReasonCancelled {
			break
		}
		if err := notify(id, textChunk("agent_message_chunk", word)); err != nil {
			return "", nil, err
		}
		written.WriteString(word)
	}
	if err := notify(parent, subagentUpdate(id, map[string]any{"state": idleState(reason)})); err != nil {
		return "", nil, err
	}
	child.State, child.StopReason = store.SubagentIdle, string(reason)
	child.Messages = append(child.Messages, store.Message{Role: "subagent", ID: nested.ID}, store.Message{Role: "assistant", Text: written.String()})
	answer := "The subagent finished. Open `/subagents` to read its transcript."
	if reason == schema.StopReasonCancelled {
		answer = "The subagent was stopped. Open `/subagents` to read what it wrote."
	}
	return answer, []store.Subagent{child, nested}, nil
}

// replaySubagent sends a saved child, and its own children, during load.
func replaySubagent(ctx context.Context, c agent.Client, s store.Session, id string) error {
	child, ok := s.Subagent(id)
	if !ok {
		return nil
	}
	notify := func(session string, update map[string]any) error {
		return c.Notify(ctx, schema.SessionUpdateMethodName, map[string]any{"sessionId": session, "update": update})
	}
	if err := notify(child.ParentID, subagentUpdate(schema.SessionId(id), map[string]any{"title": child.Title, "description": child.Description})); err != nil {
		return err
	}
	for _, m := range child.Messages {
		var err error
		switch m.Role {
		case "subagent":
			err = replaySubagent(ctx, c, s, m.ID)
		case "assistant":
			err = notify(id, textChunk("agent_message_chunk", m.Text))
		}
		if err != nil {
			return err
		}
	}
	return notify(child.ParentID, subagentUpdate(schema.SessionId(id), map[string]any{"state": idleState(schema.StopReason(child.StopReason))}))
}
