package tui

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	acp "github.com/BrokkAi/acp-go"
	"github.com/BrokkAi/acp-go/schema"
	"github.com/BrokkAi/micro-acp/internal/client"
	"github.com/BrokkAi/micro-acp/internal/store"
)

// runUntil executes cmd and every command it batches, returning the first
// message of type T. Print commands are executed and ignored.
func runUntil[T tea.Msg](cmd tea.Cmd) (T, bool) {
	var zero T
	if cmd == nil {
		return zero, false
	}
	switch msg := cmd().(type) {
	case T:
		return msg, true
	case tea.BatchMsg:
		for _, next := range msg {
			if found, ok := runUntil[T](next); ok {
				return found, true
			}
		}
	}
	return zero, false
}

// An agent that ignores session/cancel is disconnected after the stop grace
// period. That used to leave a dead client: the turn and every later prompt
// failed with "context canceled" until micro-acp was restarted.
func TestStopTimeoutRestartsTheAgent(t *testing.T) {
	t.Parallel() // Waits out the real stop grace period.
	c, _ := openLateAgentClient(t, "stuck")
	m := composer(t)
	m.store = store.Store{Directory: t.TempDir()} // The restarted client saves here.
	m.client = c
	s, _ := c.Snapshot()
	m.queued = []queuedPrompt{{id: 99, text: "follow-up", sessionID: s.ID}}
	prompt := m.startPrompt(queuedPrompt{text: "hang", blocks: []acp.Content{acp.NewTextContent("hang")}, sessionID: s.ID})
	results := make(chan tea.Msg, 1)
	go func() { results <- prompt() }()
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(10 * time.Millisecond) {
		if s, _ := c.Snapshot(); len(s.Messages) >= 2 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the prompt never reached the agent")
		}
	}
	_, stop := m.Update(keyPress(tea.KeyEscape, 0))
	if !m.stopping {
		t.Fatal("Esc did not stop the turn")
	}
	runUntil[resultMsg](stop) // Sends session/cancel and arms the grace timer.
	var result tea.Msg
	select {
	case result = <-results:
	case <-time.After(10 * time.Second):
		t.Fatal("the stop grace period did not end the prompt")
	}
	_, restart := m.Update(result)
	if m.client != nil || !m.busy || !strings.HasPrefix(m.status, "Starting") {
		t.Fatalf("an unresponsive agent was not restarted: status %q, error %q", m.status, m.lastError)
	}
	if !m.queuePaused || len(m.queued) != 1 {
		t.Fatal("the restart released prompts queued behind the stopped turn")
	}
	connected, ok := runUntil[connectedMsg](restart)
	if !ok || connected.err != nil || connected.client == nil || connected.client == c {
		t.Fatalf("restart did not connect a new agent: %+v", connected)
	}
	t.Cleanup(func() { _ = connected.client.Shutdown() })
	_, create := m.Update(connected)
	created, ok := runUntil[resultMsg](create)
	if !ok || created.err != nil {
		t.Fatalf("the restarted agent has no session: %+v", created)
	}
	m.Update(created)
	m.input.SetValue("after the stop")
	_, send := m.Update(keyPress(tea.KeyEnter, 0))
	if m.lastError != "" {
		t.Fatalf("prompt after restart was refused: %s", m.lastError)
	}
	sent, ok := runUntil[resultMsg](send)
	if !ok || sent.err != nil || sent.reason != schema.StopReasonEndTurn {
		t.Fatalf("prompt after restart failed: %+v", sent)
	}
}

// Every prompt to a dead agent fails, so the composer keeps the draft and
// says how to recover instead of reporting the transport error.
func TestPromptToDisconnectedAgentKeepsDraftAndPointsToReconnect(t *testing.T) {
	c, _ := openLateAgentClient(t, "stuck")
	c.Close()
	m := composer(t)
	m.client = c
	m.input.SetValue("hello")
	m.Update(keyPress(tea.KeyEnter, 0))
	if m.prompting || m.input.Value() != "hello" || m.lastError != disconnectedHint {
		t.Fatalf("prompt to a closed agent: prompting=%v draft=%q error=%q", m.prompting, m.input.Value(), m.lastError)
	}
	m.Update(resultMsg{status: "Ready", prompt: true, err: client.ErrStopTimeout})
	if m.lastError != disconnectedHint {
		t.Fatalf("result from a closed agent shows %q", m.lastError)
	}
}
