package tui

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	acp "github.com/BrokkAi/acp-go"
	"github.com/BrokkAi/acp-go/acptest"
	"github.com/BrokkAi/acp-go/schema"
	schema2 "github.com/BrokkAi/acp-go/schema/v2"
	agent2 "github.com/BrokkAi/acp-go/v2/agent"
	"github.com/BrokkAi/micro-acp/internal/client"
	"github.com/BrokkAi/micro-acp/internal/config"
	"github.com/BrokkAi/micro-acp/internal/store"
	"github.com/charmbracelet/x/ansi"
)

// tuiV2Agent is acptest's draft-v2 fake agent with the turns these tests need.
type tuiV2Agent struct {
	acptest.V2Agent
	sequence atomic.Int64
}

func (a *tuiV2Agent) Initialize(context.Context, agent2.Client, schema2.InitializeRequest) (schema2.InitializeResponse, error) {
	var response schema2.InitializeResponse
	err := json.Unmarshal([]byte(`{"protocolVersion":2,"info":{"name":"tui-v2","version":"1"},"capabilities":{"session":{}}}`), &response)
	return response, err
}

func (a *tuiV2Agent) Prompt(_ context.Context, c agent2.Client, r schema2.PromptRequest, updates agent2.SessionUpdater) (schema2.PromptResponse, error) {
	send := func(update string) {
		var u schema2.SessionUpdate
		if json.Unmarshal([]byte(update), &u) == nil {
			_ = updates.Update(u)
		}
	}
	text := r.Prompt[0].Text.Text
	send(`{"sessionUpdate":"state_update","state":"running"}`)
	go func() {
		switch text {
		case "edit":
			send(`{"sessionUpdate":"state_update","state":"requires_action"}`)
			var request schema2.RequestPermissionRequest
			_ = json.Unmarshal([]byte(`{"sessionId":"`+string(r.SessionID)+`","title":"Edit app.go","options":[{"optionId":"allow","name":"Allow","kind":"allow_once"}]}`), &request)
			_, _ = c.RequestPermission(context.Background(), request)
			send(`{"sessionUpdate":"state_update","state":"running"}`)
			send(`{"sessionUpdate":"tool_call_update","toolCallId":"edit","title":"Edit app.go","kind":"edit","status":"completed","content":[{"type":"diff","changes":[{"operation":"modify","path":"/w/app.go"}],"patch":{"format":"git_patch","text":"-a\n+b\n"}}]}`)
			send(`{"sessionUpdate":"state_update","state":"idle","stopReason":"end_turn"}`)
		case "mail":
			send(`{"sessionUpdate":"state_update","state":"idle","stopReason":"end_turn"}`)
			time.Sleep(50 * time.Millisecond)
			send(`{"sessionUpdate":"state_update","state":"running"}`)
			send(`{"sessionUpdate":"user_message","messageId":"mail","content":[{"type":"text","text":"peer mail"}]}`)
			// Stay busy until the test has seen the turn.
			awaitLateGate()
			send(`{"sessionUpdate":"agent_message_chunk","messageId":"reply","content":{"type":"text","text":"Got mail."}}`)
			send(`{"sessionUpdate":"state_update","state":"idle","stopReason":"end_turn"}`)
		}
	}()
	return schema2.PromptResponse{MessageID: schema2.MessageId(fmt.Sprintf("prompt-%d", a.sequence.Add(1)))}, nil
}

func TestTUIV2AgentProcess(t *testing.T) {
	if os.Getenv("MICRO_ACP_TUI_V2") == "" {
		return
	}
	_ = agent2.New(&tuiV2Agent{}).Serve(context.Background(), os.Stdin, os.Stdout)
	os.Exit(0)
}

// v2Composer connects a composer to the v2 fake agent. release lets the
// agent finish a turn it started on its own.
func v2Composer(t *testing.T) (m *model, release func()) {
	t.Helper()
	m = composer(t)
	m.width, m.height = 100, 40
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	t.Cleanup(cancel)
	gate := filepath.Join(t.TempDir(), "release")
	c, err := client.Open(ctx, "tui-v2", m.options.Cwd, config.Command{
		Command: os.Args[0], Args: []string{"-test.run=^TestTUIV2AgentProcess$"},
		Env: map[string]string{"MICRO_ACP_TUI_V2": "1", "MICRO_ACP_LATE_GATE": gate}, Protocol: config.ProtocolV2,
	}, store.Store{Directory: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Shutdown() })
	if err := c.New(); err != nil {
		t.Fatal(err)
	}
	m.client = c
	return m, func() {
		if err := os.WriteFile(gate, nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

func TestV2WaitingOnYouAndFileChanges(t *testing.T) {
	m, _ := v2Composer(t)
	s, _ := m.client.Snapshot()
	run := m.startPrompt(queuedPrompt{text: "edit", blocks: []acp.Content{acp.NewTextContent("edit")}, sessionID: s.ID})
	results := make(chan tea.Msg, 1)
	go func() { results <- run() }()
	p := <-m.client.Permissions
	// The busy line and the status line both show that the agent waits on the user.
	view := ansi.Strip(m.View().Content)
	if !strings.Contains(view, "Waiting on you") || !strings.Contains(view, "waiting on you") {
		t.Fatalf("waiting state not shown:\n%s", view)
	}
	p.Reply <- schema.RequestPermissionOutcome{Selected: &schema.SelectedPermissionOutcome{OptionID: "allow"}}
	if result := (<-results).(resultMsg); result.err != nil || result.reason != schema.StopReasonEndTurn {
		t.Fatalf("turn = %+v", result)
	}
	m.busy, m.prompting = false, false
	m.syncTranscript()
	printed := ansi.Strip(strings.Join(m.printQueue, "\n"))
	if !strings.Contains(printed, "✓ Edit app.go") || !strings.Contains(printed, "/w/app.go · updated") {
		t.Fatalf("file change row missing:\n%s", printed)
	}
	if view := ansi.Strip(m.View().Content); !strings.Contains(view, "idle") {
		t.Fatalf("idle state not shown:\n%s", view)
	}
	details := ansi.Strip(m.messageView(func() store.Message { s, _ := m.client.Snapshot(); return s.Messages[len(s.Messages)-1] }(), true, false))
	if !strings.Contains(details, "Changes") || !strings.Contains(details, "+b") {
		t.Fatalf("details lack the patch:\n%s", details)
	}
}

func TestV2AgentStartedTurnShowsAsWork(t *testing.T) {
	m, release := v2Composer(t)
	s, _ := m.client.Snapshot()
	run := m.startPrompt(queuedPrompt{text: "mail", blocks: []acp.Content{acp.NewTextContent("mail")}, sessionID: s.ID})
	if result := run().(resultMsg); result.err != nil {
		t.Fatal(result.err)
	}
	m.busy, m.prompting = false, false
	deadline := time.Now().Add(5 * time.Second)
	for !m.agentTurn {
		if time.Now().After(deadline) {
			t.Fatal("agent-started turn never shown")
		}
		m.followAgentTurn()
		time.Sleep(5 * time.Millisecond)
	}
	if !m.busy || !m.prompting || !strings.Contains(ansi.Strip(m.View().Content), "Agent working") {
		t.Fatalf("agent turn not shown as work:\n%s", ansi.Strip(m.View().Content))
	}
	release()
	for m.agentTurn {
		if time.Now().After(deadline) {
			t.Fatal("agent-started turn never ended")
		}
		m.followAgentTurn()
		time.Sleep(5 * time.Millisecond)
	}
	if m.busy || m.prompting {
		t.Fatal("agent turn left the composer busy")
	}
	printed := ansi.Strip(strings.Join(m.printQueue, "\n"))
	if !strings.Contains(printed, "peer mail") || !strings.Contains(printed, "Got mail.") {
		t.Fatalf("agent turn output missing:\n%s", printed)
	}
}
