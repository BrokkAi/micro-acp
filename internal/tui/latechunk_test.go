package tui

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	acp "github.com/BrokkAi/acp-go"
	"github.com/BrokkAi/acp-go/agent"
	"github.com/BrokkAi/acp-go/schema"
	"github.com/BrokkAi/micro-acp/internal/client"
	"github.com/BrokkAi/micro-acp/internal/config"
	"github.com/BrokkAi/micro-acp/internal/store"
	"github.com/charmbracelet/x/ansi"
)

// lateTestAgent supplies the mandatory ACP surface; each probe only overrides
// the prompt behavior.
type lateTestAgent struct{}

func (lateTestAgent) Initialize(context.Context, agent.Client, schema.InitializeRequest) (schema.InitializeResponse, error) {
	return schema.InitializeResponse{ProtocolVersion: acp.Version, AgentInfo: &schema.Implementation{Name: "late chunk agent", Version: "1.0.0"}}, nil
}

func (lateTestAgent) NewSession(context.Context, agent.Client, schema.NewSessionRequest) (schema.NewSessionResponse, error) {
	return schema.NewSessionResponse{SessionID: "late-session"}, nil
}

// lateChunkAgent streams the start of a message, answers the prompt, and only
// then flushes the rest of the text. A real adapter can race its final writes
// against the prompt response the same way.
type lateChunkAgent struct{ lateTestAgent }

func (lateChunkAgent) Prompt(_ context.Context, c agent.Client, r schema.PromptRequest, updates agent.SessionUpdater) (schema.PromptResponse, error) {
	first := schema.ContentChunk{Content: acp.NewTextContent("first half\n\n")}
	if err := updates.Update(schema.SessionUpdate{AgentMessageChunk: &first}); err != nil {
		return schema.PromptResponse{}, err
	}
	go func() {
		awaitLateGate()
		tail := schema.ContentChunk{Content: acp.NewTextContent("tail flushed after the response")}
		_ = c.Notify(context.Background(), schema.SessionUpdateMethodName, acp.NewAgentMessageChunkUpdate(r.SessionID, tail))
	}()
	return schema.PromptResponse{StopReason: schema.StopReasonEndTurn}, nil
}

// lateToolAgent finishes its turn with a tool call still in progress and
// reports the completion afterwards.
type lateToolAgent struct{ lateTestAgent }

func (lateToolAgent) Prompt(_ context.Context, c agent.Client, r schema.PromptRequest, updates agent.SessionUpdater) (schema.PromptResponse, error) {
	inProgress := schema.ToolCallStatusInProgress
	tool := schema.ToolCall{ToolCallID: "tool-1", Title: "Run tests", Status: &inProgress}
	if err := updates.Update(schema.SessionUpdate{ToolCall: &tool}); err != nil {
		return schema.PromptResponse{}, err
	}
	go func() {
		awaitLateGate()
		completed := schema.ToolCallStatusCompleted
		_ = c.Notify(context.Background(), schema.SessionUpdateMethodName, acp.NewToolCallChangedUpdate(r.SessionID, schema.ToolCallUpdate{ToolCallID: "tool-1", Status: &completed}))
	}()
	return schema.PromptResponse{StopReason: schema.StopReasonEndTurn}, nil
}

// lateKindAgent changes only the kind of a committed tool call, which the
// compact transcript line does not show.
type lateKindAgent struct{ lateTestAgent }

func (lateKindAgent) Prompt(_ context.Context, c agent.Client, r schema.PromptRequest, updates agent.SessionUpdater) (schema.PromptResponse, error) {
	inProgress := schema.ToolCallStatusInProgress
	tool := schema.ToolCall{ToolCallID: "tool-1", Title: "Run tests", Status: &inProgress}
	if err := updates.Update(schema.SessionUpdate{ToolCall: &tool}); err != nil {
		return schema.PromptResponse{}, err
	}
	go func() {
		awaitLateGate()
		kind := schema.ToolKindExecute
		_ = c.Notify(context.Background(), schema.SessionUpdateMethodName, acp.NewToolCallChangedUpdate(r.SessionID, schema.ToolCallUpdate{ToolCallID: "tool-1", Kind: &kind}))
	}()
	return schema.PromptResponse{StopReason: schema.StopReasonEndTurn}, nil
}

// lateOlderToolAgent ends the turn with text after an in-progress tool call and
// completes that older tool afterwards.
type lateOlderToolAgent struct{ lateTestAgent }

func (lateOlderToolAgent) Prompt(_ context.Context, c agent.Client, r schema.PromptRequest, updates agent.SessionUpdater) (schema.PromptResponse, error) {
	inProgress := schema.ToolCallStatusInProgress
	tool := schema.ToolCall{ToolCallID: "tool-1", Title: "Run tests", Status: &inProgress}
	if err := updates.Update(schema.SessionUpdate{ToolCall: &tool}); err != nil {
		return schema.PromptResponse{}, err
	}
	text := schema.ContentChunk{Content: acp.NewTextContent("waiting on the suite\n\n")}
	if err := updates.Update(schema.SessionUpdate{AgentMessageChunk: &text}); err != nil {
		return schema.PromptResponse{}, err
	}
	go func() {
		awaitLateGate()
		completed := schema.ToolCallStatusCompleted
		_ = c.Notify(context.Background(), schema.SessionUpdateMethodName, acp.NewToolCallChangedUpdate(r.SessionID, schema.ToolCallUpdate{ToolCallID: "tool-1", Status: &completed}))
	}()
	return schema.PromptResponse{StopReason: schema.StopReasonEndTurn}, nil
}

// awaitLateGate holds a late update until the test has checked the turn as
// committed. A fixed delay raced the test on slow CI runners.
func awaitLateGate() {
	gate := os.Getenv("MICRO_ACP_LATE_GATE")
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(5 * time.Millisecond) {
		if _, err := os.Stat(gate); err == nil {
			return
		}
	}
}

func TestLateAgentProcess(t *testing.T) {
	var implementation agent.Agent
	switch os.Getenv("MICRO_ACP_LATE_AGENT") {
	case "chunk":
		implementation = lateChunkAgent{}
	case "tool":
		implementation = lateToolAgent{}
	case "kind":
		implementation = lateKindAgent{}
	case "older":
		implementation = lateOlderToolAgent{}
	default:
		return
	}
	if err := agent.New(implementation).Serve(context.Background(), os.Stdin, os.Stdout); err != nil {
		t.Fatal(err)
	}
}

// openLateAgentClient returns a client and the function that lets the agent
// send its late update.
func openLateAgentClient(t *testing.T, mode string) (*client.Client, func()) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	t.Cleanup(cancel)
	gate := filepath.Join(t.TempDir(), "release")
	c, err := client.Open(ctx, "late", t.TempDir(), config.Command{
		Command: os.Args[0], Args: []string{"-test.run=^TestLateAgentProcess$"},
		Env: map[string]string{"MICRO_ACP_LATE_AGENT": mode, "MICRO_ACP_LATE_GATE": gate},
	}, store.Store{Directory: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Shutdown() })
	if err := c.New(); err != nil {
		t.Fatal(err)
	}
	release := func() {
		if err := os.WriteFile(gate, nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return c, release
}

func finishTurn(t *testing.T, m *model, c *client.Client) {
	t.Helper()
	s, _ := c.Snapshot()
	run := m.startPrompt(queuedPrompt{text: "report", blocks: []acp.Content{acp.NewTextContent("report")}, sessionID: s.ID})
	result, ok := run().(resultMsg)
	if !ok || result.err != nil {
		t.Fatalf("prompt failed: %#v", result)
	}
	// Apply the state changes the resultMsg handler makes, then sync directly:
	// Update also flushes the print queue into a command, hiding it from tests.
	m.busy, m.prompting = false, false
	m.syncTranscript()
}

func waitForSnapshot(t *testing.T, c *client.Client, want string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		s, _ := c.Snapshot()
		if strings.Contains(sessionText(s), want) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("client never received %q: %+v", want, s.Messages)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestLateChunkAfterPromptResponseIsPrinted(t *testing.T) {
	if os.Getenv("MICRO_ACP_LATE_AGENT") != "" {
		t.Skip("agent process mode")
	}
	m := composer(t)
	c, release := openLateAgentClient(t, "chunk")
	m.client = c
	finishTurn(t, m, c)
	first := ansi.Strip(strings.Join(m.printQueue, "\n"))
	if !strings.Contains(first, "first half") {
		t.Fatalf("streamed text was not committed:\n%s", first)
	}
	m.printQueue = nil

	release()
	waitForSnapshot(t, c, "tail flushed after the response")
	m.syncTranscript()
	late := ansi.Strip(strings.Join(m.printQueue, "\n"))
	if !strings.Contains(late, "tail flushed after the response") {
		t.Fatalf("late chunk stayed invisible in the transcript:\n%s", late)
	}
	if strings.Contains(late, "first half") {
		t.Fatalf("late chunk reprinted committed text:\n%s", late)
	}
	m.printQueue = nil
	m.syncTranscript()
	if len(m.printQueue) != 0 {
		t.Fatalf("repeated sync duplicated output: %q", m.printQueue)
	}
}

func TestLateToolUpdateAfterPromptResponseIsReprinted(t *testing.T) {
	if os.Getenv("MICRO_ACP_LATE_AGENT") != "" {
		t.Skip("agent process mode")
	}
	m := composer(t)
	c, release := openLateAgentClient(t, "tool")
	m.client = c
	finishTurn(t, m, c)
	first := ansi.Strip(strings.Join(m.printQueue, "\n"))
	if !strings.Contains(first, "◦ Run tests") {
		t.Fatalf("in-progress tool call was not committed:\n%s", first)
	}
	m.printQueue = nil

	release()
	waitForSnapshot(t, c, "Run tests · completed")
	m.syncTranscript()
	late := ansi.Strip(strings.Join(m.printQueue, "\n"))
	if !strings.Contains(late, "✓ Run tests") {
		t.Fatalf("late tool update stayed invisible in the transcript:\n%s", late)
	}
	m.printQueue = nil
	m.syncTranscript()
	if len(m.printQueue) != 0 {
		t.Fatalf("repeated sync duplicated output: %q", m.printQueue)
	}
}

func TestLateKindOnlyToolUpdateIsIgnored(t *testing.T) {
	if os.Getenv("MICRO_ACP_LATE_AGENT") != "" {
		t.Skip("agent process mode")
	}
	m := composer(t)
	c, release := openLateAgentClient(t, "kind")
	m.client = c
	finishTurn(t, m, c)
	if first := ansi.Strip(strings.Join(m.printQueue, "\n")); !strings.Contains(first, "◦ Run tests") {
		t.Fatalf("tool call was not committed:\n%s", first)
	}
	m.printQueue = nil

	release()
	waitForSnapshot(t, c, "execute")
	m.syncTranscript()
	if late := ansi.Strip(strings.Join(m.printQueue, "\n")); late != "" {
		t.Fatalf("kind-only update reprinted an unchanged line: %q", late)
	}
}

func TestLateOlderToolUpdateIsReprinted(t *testing.T) {
	if os.Getenv("MICRO_ACP_LATE_AGENT") != "" {
		t.Skip("agent process mode")
	}
	m := composer(t)
	c, release := openLateAgentClient(t, "older")
	m.client = c
	finishTurn(t, m, c)
	first := ansi.Strip(strings.Join(m.printQueue, "\n"))
	if !strings.Contains(first, "◦ Run tests") || !strings.Contains(first, "waiting on the suite") {
		t.Fatalf("turn was not committed:\n%s", first)
	}
	m.printQueue = nil

	release()
	waitForSnapshot(t, c, "Run tests · completed")
	m.syncTranscript()
	late := ansi.Strip(strings.Join(m.printQueue, "\n"))
	if !strings.Contains(late, "✓ Run tests") {
		t.Fatalf("completion of an older tool call stayed invisible:\n%s", late)
	}
	m.printQueue = nil
	m.syncTranscript()
	if len(m.printQueue) != 0 {
		t.Fatalf("repeated sync duplicated output: %q", m.printQueue)
	}
}

func sessionText(s store.Session) string {
	var b strings.Builder
	for _, message := range s.Messages {
		b.WriteString(message.Text)
	}
	return b.String()
}
