package client_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	acp "github.com/BrokkAi/acp-go"
	"github.com/BrokkAi/acp-go/schema"
	"github.com/BrokkAi/micro-acp/internal/store"
)

// subagentWire builds one agent's dialect of the subagent updates.
type subagentWire struct {
	announce func(id, name, task, prompt string) map[string]any
	finish   func(id, outcome string) map[string]any
}

var subagentDialects = map[string]subagentWire{
	// claude-agent-acp and codex-acp: the first ACP draft.
	"draft": {
		announce: func(id, name, task, prompt string) map[string]any {
			update := map[string]any{"sessionUpdate": "subagent_spawned", "subagentSessionId": id, "name": name, "task": task, "capabilities": map[string]any{}}
			if prompt != "" {
				update["prompt"] = prompt
			}
			return update
		},
		finish: func(id, outcome string) map[string]any {
			return map[string]any{"sessionUpdate": "subagent_state_update", "subagentSessionId": id, "state": outcome}
		},
	},
	// codegraff: the new update name with the old fields.
	"graff": {
		announce: func(id, name, task, _ string) map[string]any {
			return map[string]any{"sessionUpdate": "subagent_update", "subagentSessionId": id, "name": name, "task": task, "_meta": map[string]any{"graff/parentToolCallId": "call-1"}}
		},
		finish: func(id, outcome string) map[string]any {
			return map[string]any{"sessionUpdate": "subagent_update", "subagentSessionId": id, "state": outcome}
		},
	},
	// The reworked RFD, as acp-go models it in schema/unstable.
	"final": {
		announce: func(id, name, task, _ string) map[string]any {
			return map[string]any{"sessionUpdate": "subagent_update", "sessionId": id, "title": name, "description": task, "state": map[string]any{"state": "running"}, "capabilities": map[string]any{"cancel": map[string]any{}}}
		},
		finish: func(id, outcome string) map[string]any {
			state := map[string]any{"state": "idle", "stopReason": "end_turn"}
			switch outcome {
			case "cancelled":
				state["stopReason"] = "cancelled"
			case "failed":
				delete(state, "stopReason")
			case "disconnected":
				state = map[string]any{"state": "unknown"}
			}
			return map[string]any{"sessionUpdate": "subagent_update", "sessionId": id, "state": state}
		},
	},
}

func serveSubagentAgent(dialect string) {
	wire := subagentDialects[dialect]
	var conn *acp.Connection
	ready := make(chan struct{})
	cancelled := make(chan struct{}, 1)
	notify := func(ctx context.Context, session string, update map[string]any) {
		_ = conn.Notify(ctx, "session/update", map[string]any{"sessionId": session, "update": update})
	}
	chunk := func(text string) map[string]any {
		return map[string]any{"sessionUpdate": "agent_message_chunk", "content": map[string]any{"type": "text", "text": text}}
	}
	conn = acp.Connect(os.Stdin, os.Stdout, func(ctx context.Context, method string, raw json.RawMessage) (any, error) {
		<-ready
		switch method {
		case "initialize":
			var p struct {
				ClientCapabilities struct {
					Subagents map[string]any `json:"subagents"`
				} `json:"clientCapabilities"`
			}
			if err := json.Unmarshal(raw, &p); err != nil || p.ClientCapabilities.Subagents == nil {
				return nil, fmt.Errorf("client did not advertise subagents: %s", raw)
			}
			return map[string]any{"protocolVersion": 1, "agentCapabilities": map[string]any{"loadSession": true, "sessionCapabilities": map[string]any{"subagents": map[string]any{}}}}, nil
		case "session/new":
			return map[string]any{"sessionId": "root"}, nil
		case "session/load":
			// Codex rebuilds the tree from history and reports unproven outcomes as disconnected.
			notify(ctx, "root", map[string]any{"sessionUpdate": "user_message_chunk", "content": map[string]any{"type": "text", "text": "build it"}})
			notify(ctx, "root", wire.announce("task-1", "Fix the build", "Make the build pass", ""))
			notify(ctx, "task-1", chunk("Replayed child output."))
			notify(ctx, "root", wire.finish("task-1", "disconnected"))
			return map[string]any{}, nil
		case "session/prompt":
			notify(ctx, "root", wire.announce("task-1", "Fix the build", "Make the build pass", "Make the build pass"))
			if dialect == "final" {
				notify(ctx, "task-1", map[string]any{"sessionUpdate": "session_message", "messageId": "m-1", "senderSessionId": "root", "recipientSessionId": "task-1", "content": []any{map[string]any{"type": "text", "text": "Make the build"}}})
				notify(ctx, "task-1", map[string]any{"sessionUpdate": "session_message_chunk", "messageId": "m-1", "content": map[string]any{"type": "text", "text": " pass"}})
			}
			notify(ctx, "task-1", chunk("Running the build."))
			notify(ctx, "ghost", chunk("leaked"))
			notify(ctx, "task-1", wire.announce("task-2", "Read the spec", "Read spec.md", ""))
			notify(ctx, "task-2", map[string]any{"sessionUpdate": "tool_call", "toolCallId": "read-1", "title": "Read spec.md", "kind": "read", "status": "completed"})
			permission := func(session string) (schema.RequestPermissionResponse, error) {
				var response schema.RequestPermissionResponse
				err := conn.Call(ctx, "session/request_permission", map[string]any{"sessionId": session, "toolCall": map[string]any{"toolCallId": "make", "title": "make"}, "options": []any{map[string]any{"optionId": "allow", "name": "Allow", "kind": "allow_once"}}}, &response)
				return response, err
			}
			if response, err := permission("task-1"); err != nil || response.Outcome.Selected == nil || response.Outcome.Selected.OptionID != "allow" {
				return nil, fmt.Errorf("child permission was not answered: %+v %v", response, err)
			}
			if _, err := permission("ghost"); err == nil {
				return nil, errors.New("permission for an unannounced session was accepted")
			}
			var form schema.CreateElicitationResponse
			if err := conn.Call(ctx, "elicitation/create", map[string]any{"sessionId": "task-2", "mode": "form", "message": "Which spec?", "requestedSchema": map[string]any{"type": "object", "properties": map[string]any{"name": map[string]any{"type": "string"}}}}, &form); err != nil || form.Accept == nil {
				return nil, fmt.Errorf("child form was not answered: %+v %v", form, err)
			}
			var ignored schema.CreateElicitationResponse
			if err := conn.Call(ctx, "elicitation/create", map[string]any{"sessionId": "ghost", "mode": "form", "message": "Leak?", "requestedSchema": map[string]any{"type": "object", "properties": map[string]any{}}}, &ignored); err == nil {
				return nil, errors.New("form for an unannounced session was accepted")
			}
			notify(ctx, "task-1", wire.finish("task-2", "completed"))
			outcome := "failed"
			if dialect == "final" {
				// Only the final draft lets the client cancel one child.
				select {
				case <-cancelled:
					outcome = "cancelled"
				case <-time.After(5 * time.Second):
					return nil, errors.New("child was not cancelled")
				}
			}
			notify(ctx, "root", wire.finish("task-1", outcome))
			// A finished child that is resumed comes back as a new generation.
			notify(ctx, "root", wire.announce("task-1:generation:2", "Fix the build", "Try again", ""))
			notify(ctx, "task-1:generation:2", chunk("Fixed."))
			notify(ctx, "root", wire.finish("task-1:generation:2", "completed"))
			notify(ctx, "root", chunk("All done."))
			return schema.PromptResponse{StopReason: schema.StopReasonEndTurn}, nil
		}
		return nil, &acp.RPCError{Code: -32601, Message: "not supported"}
	}, func(method string, raw json.RawMessage) error {
		var p schema.CancelNotification
		if method == "session/cancel" && json.Unmarshal(raw, &p) == nil && p.SessionID == "task-1" {
			select {
			case cancelled <- struct{}{}:
			default:
			}
		}
		return nil
	})
	close(ready)
	<-conn.Done()
}

func TestSubagentSessions(t *testing.T) {
	for _, dialect := range []string{"draft", "graff", "final"} {
		t.Run(dialect, func(t *testing.T) {
			data := t.TempDir()
			sessions := store.Store{Directory: data}
			c := openTest(t, "subagents-"+dialect, t.TempDir(), data, sessions)
			if !c.Subagents {
				t.Fatal("agent capability not recorded")
			}
			require(t, c.New())
			stop := make(chan struct{})
			defer close(stop)
			asked := make(chan string, 4)
			go func() {
				for {
					select {
					case p := <-c.Permissions:
						asked <- "permission:" + p.Subagent
						p.Reply <- schema.RequestPermissionOutcome{Selected: &schema.SelectedPermissionOutcome{OptionID: "allow"}}
					case e := <-c.Elicitations:
						asked <- "form:" + e.Subagent
						e.Reply <- schema.CreateElicitationResponse{Accept: &schema.ElicitationAcceptAction{}}
					case <-stop:
						return
					}
				}
			}()
			if dialect == "final" {
				go func() {
					deadline := time.Now().Add(5 * time.Second)
					for time.Now().Before(deadline) {
						if s, _ := c.Snapshot(); len(s.Subagents) > 0 {
							if err := c.CancelSubagent("task-1"); err != nil {
								t.Error(err)
							}
							return
						}
						time.Sleep(10 * time.Millisecond)
					}
				}()
			}
			reason, err := c.Prompt("build it")
			require(t, err)
			if reason != schema.StopReasonEndTurn {
				t.Fatalf("parent turn ended with %q", reason)
			}
			for _, want := range []string{"permission:Fix the build", "form:Read the spec"} {
				if got := <-asked; got != want {
					t.Fatalf("interaction %q, want %q", got, want)
				}
			}

			s, _ := c.Snapshot()
			if len(s.Subagents) != 3 {
				t.Fatalf("subagents = %+v", s.Subagents)
			}
			first, _ := s.Subagent("task-1")
			nested, _ := s.Subagent("task-2")
			again, _ := s.Subagent("task-1:generation:2")
			if first.ParentID != "root" || first.Title != "Fix the build" || first.Description != "Make the build pass" || nested.ParentID != "task-1" || again.ParentID != "root" || again.Generation() != 2 {
				t.Fatalf("tree = %+v", s.Subagents)
			}
			wantFirst := "failed"
			if dialect == "final" {
				wantFirst = "cancelled"
			}
			if first.StateLabel() != wantFirst || nested.StateLabel() != "done" || again.StateLabel() != "done" || first.Active() {
				t.Fatalf("states = %q %q %q", first.StateLabel(), nested.StateLabel(), again.StateLabel())
			}
			if (dialect == "final") != first.CanCancel {
				t.Fatalf("cancel capability = %v", first.CanCancel)
			}
			if dialect != "final" {
				if err := c.CancelSubagent("task-1"); err == nil {
					t.Fatal("cancelled a child without the capability")
				}
			}
			// Child output lands in the child, never the parent; unannounced
			// sessions are dropped.
			var rows, parentText []string
			for _, m := range s.Messages {
				if m.Role == "subagent" {
					rows = append(rows, m.ID+"="+m.Text)
				}
				if m.Role == "assistant" {
					parentText = append(parentText, m.Text)
				}
			}
			if strings.Join(rows, ",") != "task-1=Fix the build,task-1:generation:2=Fix the build" || strings.Join(parentText, "") != "All done." {
				t.Fatalf("parent transcript = %+v", s.Messages)
			}
			var childText []string
			for _, m := range first.Messages {
				childText = append(childText, m.Role+":"+m.Text)
			}
			want := "message:Make the build pass|assistant:Running the build.|subagent:Read the spec"
			if dialect == "graff" {
				// codegraff sends no prompt for the child.
				want = strings.TrimPrefix(want, "message:Make the build pass|")
			}
			if strings.Join(childText, "|") != want {
				t.Fatalf("child transcript = %q", childText)
			}
			if len(nested.Messages) != 1 || nested.Messages[0].Tool == nil || nested.Messages[0].Tool.Title != "Read spec.md" {
				t.Fatalf("nested transcript = %+v", nested.Messages)
			}
			for _, child := range s.Subagents {
				for _, m := range child.Messages {
					if strings.Contains(m.Text, "leaked") {
						t.Fatal("unannounced session output was routed")
					}
				}
			}

			saved, err := sessions.Load(s.ID)
			require(t, err)
			if len(saved.Subagents) != 3 || len(saved.Subagents[1].Messages) != 1 {
				t.Fatalf("saved subagents = %+v", saved.Subagents)
			}

			// Loading replays the child tree in place of the saved one.
			other := openTest(t, "subagents-"+dialect, s.Cwd, data, sessions)
			saved.Messages = nil
			require(t, other.Load(saved))
			loaded, _ := other.Snapshot()
			replayed, ok := loaded.Subagent("task-1")
			if !ok || len(loaded.Subagents) != 1 || len(replayed.Messages) != 1 || replayed.Messages[0].Text != "Replayed child output." {
				t.Fatalf("replayed tree = %+v", loaded.Subagents)
			}
			if want := map[string]string{"draft": "disconnected", "graff": "disconnected", "final": "unknown"}[dialect]; replayed.StateLabel() != want {
				t.Fatalf("replayed state = %q, want %q", replayed.StateLabel(), want)
			}
		})
	}
}

func TestSavedActiveSubagentsBecomeUnknown(t *testing.T) {
	data := t.TempDir()
	sessions := store.Store{Directory: data}
	c := openTest(t, "subagents-draft", t.TempDir(), data, sessions)
	saved := store.NewSession("demo", "root", c.Cwd)
	saved.Messages = []store.Message{{Role: "user", Text: "build it"}, {Role: "subagent", ID: "task-1", Text: "Fix the build"}}
	saved.Subagents = []store.Subagent{{ID: "task-1", ParentID: "root", Title: "Fix the build", State: store.SubagentRunning, Messages: []store.Message{}}}
	require(t, sessions.Save(saved))
	forked := saved.Clone()
	if forked.Subagents[0].Messages == nil {
		t.Fatal("clone dropped child messages")
	}
	// The agent replays no history here, so the saved tree is kept, but a
	// child that was running in an earlier process cannot still be confirmed.
	require(t, c.Load(store.Session{ID: saved.ID, Agent: saved.Agent, RemoteID: "empty", Cwd: saved.Cwd, Messages: saved.Messages, Subagents: saved.Subagents}))
	s, _ := c.Snapshot()
	if child, ok := s.Subagent("task-1"); !ok || child.State != store.SubagentUnknown {
		t.Fatalf("saved child = %+v", s.Subagents)
	}
	if saved.Subagents[0].State != store.SubagentRunning {
		t.Fatal("load changed the caller's session")
	}
}
