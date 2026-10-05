package client_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	acp "github.com/BrokkAi/acp-go"
	"github.com/BrokkAi/acp-go/acptest"
	"github.com/BrokkAi/acp-go/schema"
	schema2 "github.com/BrokkAi/acp-go/schema/v2"
	agent2 "github.com/BrokkAi/acp-go/v2/agent"
	"github.com/BrokkAi/micro-acp/internal/client"
	"github.com/BrokkAi/micro-acp/internal/config"
	"github.com/BrokkAi/micro-acp/internal/store"
)

func decode[T any](text string) T {
	var value T
	if err := json.Unmarshal([]byte(text), &value); err != nil {
		panic(fmt.Sprintf("%v: %s", err, text))
	}
	return value
}

// v2TestAgent is acptest's draft-v2 fake agent with the turn behavior the
// draft adds: prompts are acknowledged at once, and state_update reports
// running, requires_action and idle.
type v2TestAgent struct {
	acptest.V2Agent
	mu       sync.Mutex
	cancel   chan struct{}
	sequence atomic.Int64
	mode     string
}

func v2Options(mode string) string {
	return `[{"configId":"mode","name":"Mode","category":"mode","type":"select","currentValue":"` + mode + `","options":[{"value":"ask","name":"Ask"},{"value":"code","name":"Code"}]}]`
}

func (a *v2TestAgent) Initialize(context.Context, agent2.Client, schema2.InitializeRequest) (schema2.InitializeResponse, error) {
	return decode[schema2.InitializeResponse](`{"protocolVersion":2,"info":{"name":"v2-test","version":"1"},"capabilities":{"session":{"delete":{},"prompt":{"image":{}}}}}`), nil
}

func (a *v2TestAgent) NewSession(context.Context, agent2.Client, schema2.NewSessionRequest) (schema2.NewSessionResponse, error) {
	return decode[schema2.NewSessionResponse](`{"sessionId":"v2-session","configOptions":` + v2Options(a.mode) + `,"availableCommands":[{"name":"compact","description":"Compact","input":{"type":"text","hint":"what"}}]}`), nil
}

func (a *v2TestAgent) SetConfigOption(_ context.Context, _ agent2.Client, r schema2.SetSessionConfigOptionRequest) (schema2.SetSessionConfigOptionResponse, error) {
	if r.ID == nil {
		return schema2.SetSessionConfigOptionResponse{}, errors.New("select value expected")
	}
	a.mode = string(r.ID.Value)
	return decode[schema2.SetSessionConfigOptionResponse](`{"configOptions":` + v2Options(a.mode) + `}`), nil
}

func (a *v2TestAgent) ListSessions(_ context.Context, _ agent2.Client, r schema2.ListSessionsRequest) (schema2.ListSessionsResponse, error) {
	// Quote the path: Windows paths have backslashes.
	cwd, _ := json.Marshal(string(*r.Cwd))
	return decode[schema2.ListSessionsResponse](`{"sessions":[{"sessionId":"remote-v2","cwd":` + string(cwd) + `,"title":"Remote v2"}]}`), nil
}

func (a *v2TestAgent) DeleteSession(context.Context, agent2.Client, schema2.DeleteSessionRequest) (schema2.DeleteSessionResponse, error) {
	return schema2.DeleteSessionResponse{}, nil
}

func (a *v2TestAgent) CancelSession(schema2.CancelSessionNotification) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.cancel != nil {
		close(a.cancel)
		a.cancel = nil
	}
	return nil
}

func notifyV2(ctx context.Context, c agent2.Client, session schema2.SessionId, update string) {
	_ = c.Notify(ctx, schema2.SessionUpdateMethodName, json.RawMessage(`{"sessionId":"`+string(session)+`","update":`+update+`}`))
}

func (a *v2TestAgent) ResumeSession(ctx context.Context, c agent2.Client, r schema2.ResumeSessionRequest) (schema2.ResumeSessionResponse, error) {
	if r.ReplayFrom != nil {
		if r.ReplayFrom.Start == nil {
			return schema2.ResumeSessionResponse{}, &acp.RPCError{Code: -32602, Message: "only replay from start"}
		}
		// A replayed message is cleared first, then its chunks follow.
		for _, update := range []string{
			`{"sessionUpdate":"user_message","messageId":"u0","content":[]}`,
			`{"sessionUpdate":"user_message_chunk","messageId":"u0","content":{"type":"text","text":"earlier question"}}`,
			`{"sessionUpdate":"agent_message","messageId":"a0","content":[]}`,
			`{"sessionUpdate":"agent_message_chunk","messageId":"a0","content":{"type":"text","text":"earlier answer"}}`,
			`{"sessionUpdate":"terminal_update","terminalId":"ls","command":"ls","output":{"data":"` + base64.StdEncoding.EncodeToString([]byte("a.go\n")) + `"},"exitStatus":{"exitCode":0}}`,
		} {
			notifyV2(ctx, c, r.SessionID, update)
		}
	}
	return decode[schema2.ResumeSessionResponse](`{"configOptions":` + v2Options(a.mode) + `}`), nil
}

func (a *v2TestAgent) Prompt(ctx context.Context, c agent2.Client, r schema2.PromptRequest, updates agent2.SessionUpdater) (schema2.PromptResponse, error) {
	text := r.Prompt[0].Text.Text
	id := schema2.MessageId(fmt.Sprintf("user-%d", a.sequence.Add(1)))
	send := func(update string) { _ = updates.Update(decode[schema2.SessionUpdate](update)) }
	echo := `{"sessionUpdate":"user_message","messageId":"` + string(id) + `","content":[{"type":"text","text":` + fmt.Sprintf("%q", text) + `}]}`
	if text != "mail" {
		// Like claude-agent-acp: echo and running before the acknowledgement.
		send(echo)
		send(`{"sessionUpdate":"state_update","state":"running"}`)
	}
	a.mu.Lock()
	cancel := make(chan struct{})
	a.cancel = cancel
	a.mu.Unlock()
	session := r.SessionID
	go func() {
		ctx := context.Background()
		idle := func(reason string) {
			send(`{"sessionUpdate":"state_update","state":"idle","stopReason":"` + reason + `"}`)
		}
		switch text {
		case "hello":
			patch := "--- /w/app.go\n+++ /w/app.go\n@@ -1 +1 @@\n-a\n+b\n"
			for _, update := range []string{
				`{"sessionUpdate":"agent_message_chunk","messageId":"a1","content":{"type":"text","text":"Hi "}}`,
				`{"sessionUpdate":"agent_message_chunk","messageId":"a1","content":{"type":"text","text":"there"}}`,
				`{"sessionUpdate":"agent_thought","messageId":"a1","content":[{"type":"text","text":"thinking"}]}`,
				`{"sessionUpdate":"tool_call_update","toolCallId":"edit","title":"Edit app.go","kind":"edit","status":"pending","locations":[{"path":"/w/app.go","line":1}],"rawInput":{"path":"/w/app.go"},"content":[{"type":"diff","changes":[{"operation":"modify","path":"/w/app.go","fileType":"text"},{"operation":"move","path":"/w/b.go","oldPath":"/w/a.go"}],"patch":{"format":"git_patch","text":` + fmt.Sprintf("%q", patch) + `}}]}`,
				`{"sessionUpdate":"tool_call_update","toolCallId":"edit","status":"completed"}`,
				`{"sessionUpdate":"terminal_update","terminalId":"make","command":"make","cwd":"/w"}`,
				`{"sessionUpdate":"terminal_output_chunk","terminalId":"make","data":"` + base64.StdEncoding.EncodeToString([]byte("ok\n")) + `"}`,
				`{"sessionUpdate":"terminal_update","terminalId":"make","exitStatus":{"exitCode":0}}`,
				`{"sessionUpdate":"tool_call_update","toolCallId":"run","title":"make","kind":"execute","status":"completed","content":[{"type":"terminal","terminalId":"make"}]}`,
				`{"sessionUpdate":"plan_update","plan":{"type":"items","planId":"plan","entries":[{"content":"Build","priority":"high","status":"completed"}]}}`,
				`{"sessionUpdate":"usage_update","used":10,"size":100}`,
				`{"sessionUpdate":"session_info_update","title":"Greeting"}`,
				`{"sessionUpdate":"notice","severity":"warning","title":"Rate limited","description":"Retrying"}`,
			} {
				send(update)
			}
			idle("end_turn")
		case "permission":
			send(`{"sessionUpdate":"state_update","state":"requires_action"}`)
			response, err := c.RequestPermission(ctx, decode[schema2.RequestPermissionRequest](`{"sessionId":"`+string(session)+`","title":"Edit app.go","options":[{"optionId":"allow","name":"Allow","kind":"allow_once"},{"optionId":"reject","name":"Reject","kind":"reject_once"}],"subject":{"type":"tool_call","toolCall":{"toolCallId":"edit","title":"Edit app.go","rawInput":{"path":"/w/app.go"},"content":[{"type":"diff","changes":[{"operation":"modify","path":"/w/app.go"}],"patch":{"format":"git_patch","text":"-a\n+b\n"}}]}}}`))
			choice := "cancelled"
			if err == nil && response.Outcome.Selected != nil {
				choice = string(response.Outcome.Selected.OptionID)
			}
			send(`{"sessionUpdate":"state_update","state":"running"}`)
			send(`{"sessionUpdate":"agent_message_chunk","messageId":"a2","content":{"type":"text","text":"chose ` + choice + `"}}`)
			idle("end_turn")
		case "child":
			// No agent sends subagents on v2 yet. When one does, the child's
			// state arrives as state_update on the child's own session.
			send(`{"sessionUpdate":"subagent_update","sessionId":"kid","title":"Helper","description":"Look around"}`)
			for _, update := range []string{
				`{"sessionUpdate":"state_update","state":"running"}`,
				`{"sessionUpdate":"agent_message_chunk","messageId":"k1","content":{"type":"text","text":"child work"}}`,
				`{"sessionUpdate":"state_update","state":"idle","stopReason":"end_turn"}`,
			} {
				notifyV2(ctx, c, "kid", update)
			}
			notifyV2(ctx, c, "ghost", `{"sessionUpdate":"agent_message_chunk","messageId":"g","content":{"type":"text","text":"leaked"}}`)
			idle("end_turn")
		case "crash":
			os.Exit(3)
		case "fail":
			send(`{"sessionUpdate":"notice","severity":"error","title":"The model is overloaded"}`)
			send(`{"sessionUpdate":"state_update","state":"idle","stopReason":"_error","_meta":{"claudeCode":{"error":{"code":-32603,"message":"model overloaded"}}}}`)
		case "slow":
			for i := 0; ; i++ {
				select {
				case <-cancel:
					idle("cancelled")
					return
				case <-time.After(10 * time.Millisecond):
					send(`{"sessionUpdate":"agent_message_chunk","messageId":"slow","content":{"type":"text","text":"."}}`)
				}
			}
		case "mail":
			// Like codegraff: the acknowledgement comes first.
			time.Sleep(20 * time.Millisecond)
			send(echo)
			send(`{"sessionUpdate":"state_update","state":"running"}`)
			idle("end_turn")
			// Then a turn the agent starts on its own, for incoming mail.
			time.Sleep(50 * time.Millisecond)
			send(`{"sessionUpdate":"state_update","state":"running"}`)
			send(`{"sessionUpdate":"user_message","messageId":"mail-1","content":[{"type":"text","text":"peer mail"}]}`)
			send(`{"sessionUpdate":"agent_message_chunk","messageId":"a3","content":{"type":"text","text":"Got mail."}}`)
			idle("end_turn")
		default:
			idle("end_turn")
		}
	}()
	return schema2.PromptResponse{MessageID: id}, nil
}

// serveRejectingAgent rejects ACP v2 and logs every initialize version, so a
// test can prove the client never retried with v1.
func serveRejectingAgent() {
	log := filepath.Join(os.Getenv("MICRO_ACP_TEST_DATA"), "initialize.log")
	conn := acp.Connect(os.Stdin, os.Stdout, func(_ context.Context, method string, raw json.RawMessage) (any, error) {
		if method != "initialize" {
			return nil, &acp.RPCError{Code: -32601, Message: "not supported"}
		}
		var p struct {
			ProtocolVersion int `json:"protocolVersion"`
		}
		_ = json.Unmarshal(raw, &p)
		f, _ := os.OpenFile(log, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
		fmt.Fprintf(f, "%d\n", p.ProtocolVersion)
		f.Close()
		if p.ProtocolVersion == 2 {
			return nil, &acp.RPCError{Code: -32600, Message: "v2 is switched off"}
		}
		return map[string]any{"protocolVersion": 1}, nil
	}, nil)
	<-conn.Done()
}

func openV2(t *testing.T, mode, data string) *client.Client {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	t.Cleanup(cancel)
	c, err := client.Open(ctx, "demo", t.TempDir(), config.Command{Command: os.Args[0], Args: []string{"-test.run=^TestAgentProcess$"}, Env: map[string]string{"MICRO_ACP_TEST_HELPER": mode, "MICRO_ACP_TEST_DATA": data}, Protocol: config.ProtocolV2}, store.Store{Directory: data})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := c.Shutdown(); err != nil {
			t.Error(err)
		}
	})
	return c
}

func roles(messages []store.Message) string {
	var parts []string
	for _, m := range messages {
		parts = append(parts, m.Role+":"+strings.SplitN(m.Text, "\n", 2)[0])
	}
	return strings.Join(parts, "|")
}

func TestV2PromptIdleAndUpdates(t *testing.T) {
	c := openV2(t, "v2", t.TempDir())
	if c.Protocol() != 2 || c.CanSteer() || !c.CanLoad() || !c.CanDelete() {
		t.Fatalf("protocol %d steer %v load %v delete %v", c.Protocol(), c.CanSteer(), c.CanLoad(), c.CanDelete())
	}
	require(t, c.New())
	s, _ := c.Snapshot()
	if s.RemoteID != "v2-session" || len(s.Commands) != 1 || s.Commands[0].Name != "compact" {
		t.Fatalf("session = %+v", s)
	}
	require(t, c.Configure("mode", "code"))

	reason, err := c.Prompt("hello")
	require(t, err)
	if reason != schema.StopReasonEndTurn || c.TurnState() != client.TurnIdle {
		t.Fatalf("turn ended %q in state %q", reason, c.TurnState())
	}
	s, _ = c.Snapshot()
	want := "user:hello|assistant:Hi there|thought:thinking|tool:Edit app.go · completed · edit|terminal:make|tool:make · completed · execute|plan:completed  [high] Build|notice:Warning: Rate limited"
	if got := roles(s.Messages); got != want {
		t.Fatalf("transcript\n got %s\nwant %s", got, want)
	}
	if s.Messages[0].ID != "user-1" {
		t.Fatalf("prompt not matched to its echo: %+v", s.Messages[0])
	}
	edit := s.Messages[3]
	if len(edit.Changes) != 2 || edit.Changes[0].Label() != "updated" || edit.Changes[1].Label() != "moved from /w/a.go" || !strings.Contains(edit.Patch, "+b") || len(edit.Tool.Locations) != 1 {
		t.Fatalf("file changes = %+v", edit)
	}
	if terminal := s.Messages[4].Text; terminal != "make\nok\nExit: 0" {
		t.Fatalf("terminal = %q", terminal)
	}
	if run := s.Messages[5].Tool; len(run.Content) != 1 || run.Content[0].Terminal == nil {
		t.Fatalf("terminal tool = %+v", run)
	}
	if s.Plan == nil || len(s.Plan.Entries) != 1 || s.Usage == nil || s.Usage.Used != 10 || s.Title != "Greeting" || s.Messages[7].Text != "Warning: Rate limited\nRetrying" {
		t.Fatalf("session state = %+v", s)
	}
	selectors := c.Selectors()
	if len(selectors) != 1 || selectors[0].Category != "mode" || selectors[0].Current != "code" || len(selectors[0].Choices) != 2 {
		t.Fatalf("selectors = %+v", selectors)
	}
	sessions, err := c.Sessions()
	require(t, err)
	found := false
	for _, session := range sessions {
		found = found || session.Title == "Remote v2"
	}
	if !found {
		t.Fatalf("remote sessions = %+v", sessions)
	}
}

func TestV2RequiresActionAndCancel(t *testing.T) {
	c := openV2(t, "v2", t.TempDir())
	require(t, c.New())
	done := make(chan error, 1)
	go func() {
		reason, err := c.Prompt("permission")
		if err == nil && reason != schema.StopReasonEndTurn {
			err = fmt.Errorf("stop reason %q", reason)
		}
		done <- err
	}()
	p := <-c.Permissions
	if c.TurnState() != client.TurnRequiresAction {
		t.Fatalf("state while asking = %q", c.TurnState())
	}
	if *p.Request.ToolCall.Title != "Edit app.go" || p.Request.ToolCall.ToolCallID != "edit" || !strings.Contains(client.ToolText(schema.ToolCall{Content: p.Request.ToolCall.Content}), "+b") {
		t.Fatalf("permission = %+v", p.Request)
	}
	p.Reply <- schema.RequestPermissionOutcome{Selected: &schema.SelectedPermissionOutcome{OptionID: "allow"}}
	require(t, <-done)
	s, _ := c.Snapshot()
	if !strings.Contains(roles(s.Messages), "assistant:chose allow") {
		t.Fatalf("transcript = %s", roles(s.Messages))
	}
	// Claude sends an edit's file changes only in the permission subject.
	if edit := s.Messages[1]; edit.Role != "tool" || edit.ID != "edit" || len(edit.Changes) != 1 || edit.Patch == "" {
		t.Fatalf("permission subject not kept: %+v", edit)
	}

	go func() {
		reason, err := c.Prompt("slow")
		if err == nil && reason != schema.StopReasonCancelled {
			err = fmt.Errorf("stop reason %q", reason)
		}
		done <- err
	}()
	waitFor(t, func() bool { return c.TurnState() == client.TurnRunning })
	time.Sleep(30 * time.Millisecond)
	require(t, c.Cancel())
	require(t, <-done)
}

func TestV2SubagentStateComesFromTheChildSession(t *testing.T) {
	c := openV2(t, "v2", t.TempDir())
	require(t, c.New())
	_, err := c.Prompt("child")
	require(t, err)
	s, _ := c.Snapshot()
	child, ok := s.Subagent("kid")
	if !ok || child.Title != "Helper" || child.StateLabel() != "done" || len(child.Messages) != 1 || child.Messages[0].Text != "child work" {
		t.Fatalf("child = %+v", s.Subagents)
	}
	if got := roles(s.Messages); got != "user:child|subagent:Helper" {
		t.Fatalf("parent transcript = %s", got)
	}
}

func TestV2AgentExitEndsTheTurn(t *testing.T) {
	c := openV2(t, "v2", t.TempDir())
	require(t, c.New())
	start := time.Now()
	if _, err := c.Prompt("crash"); err == nil {
		t.Fatal("a turn whose agent exited succeeded")
	}
	select {
	case <-c.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("client never noticed the agent exited")
	}
	if time.Since(start) > 5*time.Second {
		t.Fatalf("noticing the exit took %s", time.Since(start))
	}
}

func TestV2FailedTurnIsAnError(t *testing.T) {
	c := openV2(t, "v2", t.TempDir())
	require(t, c.New())
	_, err := c.Prompt("fail")
	var rpcErr *acp.RPCError
	if !errors.As(err, &rpcErr) || rpcErr.Code != -32603 || rpcErr.Message != "model overloaded" {
		t.Fatalf("failed turn returned %v", err)
	}
	if s, _ := c.Snapshot(); !strings.Contains(roles(s.Messages), "notice:Error: The model is overloaded") {
		t.Fatalf("transcript = %s", roles(s.Messages))
	}
}

func TestV2ResumeFromStartReplaysHistory(t *testing.T) {
	data := t.TempDir()
	c := openV2(t, "v2", data)
	saved := store.NewSession("demo", "v2-session", c.Cwd)
	require(t, c.Load(saved))
	s, _ := c.Snapshot()
	if got := roles(s.Messages); got != "user:earlier question|assistant:earlier answer|terminal:ls" {
		t.Fatalf("replayed transcript = %s", got)
	}
	// With a local transcript the agent resumes without replay.
	saved.Messages = []store.Message{{Role: "user", Text: "kept"}}
	require(t, c.Load(saved))
	s, _ = c.Snapshot()
	if got := roles(s.Messages); got != "user:kept" {
		t.Fatalf("resumed transcript = %s", got)
	}
}

func TestV2AgentStartedTurn(t *testing.T) {
	c := openV2(t, "v2", t.TempDir())
	require(t, c.New())
	reason, err := c.Prompt("mail")
	require(t, err)
	if reason != schema.StopReasonEndTurn {
		t.Fatalf("stop reason %q", reason)
	}
	waitFor(t, func() bool {
		s, _ := c.Snapshot()
		return strings.Contains(roles(s.Messages), "Got mail.") && c.TurnState() == client.TurnIdle
	})
	s, _ := c.Snapshot()
	if got := roles(s.Messages); got != "user:mail|user:peer mail|assistant:Got mail." {
		t.Fatalf("transcript = %s", got)
	}
}

func TestV2FallsBackToV1AndNeverRetriesARejection(t *testing.T) {
	// A v1 agent answers v1, and the client continues with v1.
	c := openV2(t, "native-replay", t.TempDir())
	if c.Protocol() != 1 {
		t.Fatalf("protocol = %d", c.Protocol())
	}
	require(t, c.New())
	reason, err := c.Prompt("hello")
	require(t, err)
	if reason != schema.StopReasonEndTurn {
		t.Fatalf("v1 fallback prompt ended %q", reason)
	}

	data := t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	_, err = client.Open(ctx, "demo", t.TempDir(), config.Command{Command: os.Args[0], Args: []string{"-test.run=^TestAgentProcess$"}, Env: map[string]string{"MICRO_ACP_TEST_HELPER": "v2-reject", "MICRO_ACP_TEST_DATA": data}, Protocol: config.ProtocolV2}, store.Store{Directory: data})
	if err == nil || !strings.Contains(err.Error(), "v2 is switched off") {
		t.Fatalf("v2 rejection = %v", err)
	}
	log, _ := os.ReadFile(filepath.Join(data, "initialize.log"))
	if string(log) != "2\n" {
		t.Fatalf("initialize versions sent: %q", log)
	}
}

func waitFor(t *testing.T, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !condition() {
		if time.Now().After(deadline) {
			t.Fatal("condition never held")
		}
		time.Sleep(5 * time.Millisecond)
	}
}
