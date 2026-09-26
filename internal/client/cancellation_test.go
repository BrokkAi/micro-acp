package client

import (
	"context"
	"encoding/json"
	"io"
	"testing"

	acp "github.com/BrokkAi/acp-go"
	"github.com/BrokkAi/acp-go/schema"
	"github.com/BrokkAi/micro-acp/internal/store"
)

func TestCancelMarksOnlyCurrentUnfinishedToolsAndAcceptsLateResults(t *testing.T) {
	input, agentOutput := io.Pipe()
	agentInput, output := io.Pipe()
	conn := acp.Connect(input, output, nil, nil)
	done := make(chan struct{})
	t.Cleanup(func() {
		close(done)
		_ = conn.Close()
		_ = input.Close()
		_ = output.Close()
		_ = agentInput.Close()
		_ = agentOutput.Close()
	})
	session := store.NewSession("test", "remote", t.TempDir())
	c := &Client{ctx: context.Background(), conn: conn, current: session, wire: acp.Session{SessionID: "remote"}, turnDone: done, turnCancel: func() {}, store: store.Store{Directory: t.TempDir()}}
	update := func(body string) {
		t.Helper()
		if err := c.notification(schema.SessionUpdateMethodName, json.RawMessage(`{"sessionId":"remote","update":`+body+`}`)); err != nil {
			t.Fatal(err)
		}
	}
	update(`{"sessionUpdate":"tool_call","toolCallId":"previous","title":"Previous turn","status":"in_progress"}`)
	c.turnStart = len(c.current.Messages)
	for _, tool := range []struct{ id, status string }{{"default", ""}, {"pending", "pending"}, {"running", "in_progress"}, {"done", "completed"}, {"failed", "failed"}} {
		status := ""
		if tool.status != "" {
			status = `,"status":"` + tool.status + `"`
		}
		update(`{"sessionUpdate":"tool_call","toolCallId":"` + tool.id + `","title":"Work"` + status + `}`)
	}
	before, _ := c.Snapshot()
	wire := make(chan string, 1)
	go func() {
		var notice struct{ Method string }
		_ = json.NewDecoder(agentInput).Decode(&notice)
		wire <- notice.Method
	}()
	if err := c.Cancel(); err != nil {
		t.Fatal(err)
	}
	if method := <-wire; method != schema.SessionCancelMethodName {
		t.Fatalf("wrong cancellation method %q", method)
	}
	assertCancelled := func(want map[string]bool) {
		t.Helper()
		snapshot, _ := c.Snapshot()
		for _, message := range snapshot.Messages {
			if message.Cancelled != want[message.ID] {
				t.Errorf("%s: cancelled=%v, want %v", message.ID, message.Cancelled, want[message.ID])
			}
		}
	}
	assertCancelled(map[string]bool{"default": true, "pending": true, "running": true})
	for _, message := range before.Messages {
		if message.Cancelled {
			t.Fatal("cancellation mutated an existing snapshot")
		}
	}
	if got := *c.current.Messages[3].Tool.Status; got != schema.ToolCallStatusInProgress {
		t.Fatalf("local cancellation changed wire status to %q", got)
	}
	update(`{"sessionUpdate":"tool_call_update","toolCallId":"running","title":"Still finishing"}`)
	update(`{"sessionUpdate":"tool_call_update","toolCallId":"default","status":"completed"}`)
	update(`{"sessionUpdate":"tool_call_update","toolCallId":"pending","status":"failed"}`)
	update(`{"sessionUpdate":"tool_call","toolCallId":"late","title":"Late tool","status":"in_progress"}`)
	update(`{"sessionUpdate":"tool_call_update","toolCallId":"orphan","status":"pending"}`)
	assertCancelled(map[string]bool{"running": true, "late": true, "orphan": true})
	if err := c.Save(); err != nil {
		t.Fatal(err)
	}
	saved, err := c.store.Load(session.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !saved.Messages[3].Cancelled || saved.Messages[1].Cancelled {
		t.Fatal("local cancellation and final agent results did not survive persistence")
	}
}
