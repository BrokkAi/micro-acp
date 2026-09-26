package client_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	acp "github.com/BrokkAi/acp-go"
	"github.com/BrokkAi/acp-go/schema"
	"github.com/BrokkAi/micro-acp/internal/buildinfo"
	"github.com/BrokkAi/micro-acp/internal/client"
	"github.com/BrokkAi/micro-acp/internal/config"
	"github.com/BrokkAi/micro-acp/internal/demo"
	"github.com/BrokkAi/micro-acp/internal/store"
)

func TestAgentProcess(t *testing.T) {
	mode := os.Getenv("MICRO_ACP_TEST_HELPER")
	if mode == "" {
		return
	}
	if mode == "rich" {
		serveRichAgent()
		os.Exit(0)
	}
	if mode == "demo" {
		if err := demo.Run(context.Background(), os.Getenv("MICRO_ACP_TEST_DATA")); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		os.Exit(0)
	}
	var seq atomic.Uint64
	ready := make(chan struct{})
	var conn *acp.Connection
	conn = acp.Connect(os.Stdin, os.Stdout, func(ctx context.Context, method string, raw json.RawMessage) (any, error) {
		<-ready
		switch method {
		case "initialize":
			if mode == "version" {
				var request schema.InitializeRequest
				if err := json.Unmarshal(raw, &request); err != nil {
					return nil, err
				}
				if request.ClientInfo == nil || request.ClientInfo.Name != "micro-acp" || request.ClientInfo.Version != buildinfo.Version {
					return nil, fmt.Errorf("client identity %+v does not report build %q", request.ClientInfo, buildinfo.Version)
				}
			}
			if mode == "native-replay" {
				return map[string]any{"protocolVersion": 1, "agentCapabilities": map[string]any{"loadSession": true, "sessionCapabilities": map[string]any{"fork": map[string]any{}, "resume": map[string]any{}}}}, nil
			}
			return map[string]any{"protocolVersion": 1, "agentCapabilities": map[string]any{"sessionCapabilities": map[string]any{"fork": map[string]any{}}}}, nil
		case "session/new":
			return schema.NewSessionResponse{SessionID: schema.SessionId(fmt.Sprintf("native-%d", seq.Add(1)))}, nil
		case "session/resume":
			return schema.ResumeSessionResponse{}, nil
		case "session/load":
			var request schema.LoadSessionRequest
			if err := json.Unmarshal(raw, &request); err != nil {
				return nil, err
			}
			_ = conn.Notify(ctx, "session/update", acp.NewUserMessageChunkUpdate(request.SessionID, schema.ContentChunk{Content: acp.NewTextContent("hello")}))
			_ = conn.Notify(ctx, "session/update", acp.NewAgentMessageChunkUpdate(request.SessionID, schema.ContentChunk{Content: acp.NewTextContent("native reply")}))
			return schema.LoadSessionResponse{}, nil
		case "session/fork":
			var p struct {
				SessionID string `json:"sessionId"`
				Cwd       string `json:"cwd"`
			}
			if err := json.Unmarshal(raw, &p); err != nil {
				return nil, err
			}
			if p.SessionID != "native-1" || p.Cwd == "" {
				return nil, fmt.Errorf("incorrect fork request: %s", raw)
			}
			if mode == "native-replay" {
				_ = conn.Notify(ctx, "session/update", acp.NewUserMessageChunkUpdate("native-fork", schema.ContentChunk{Content: acp.NewTextContent("hello")}))
				_ = conn.Notify(ctx, "session/update", acp.NewAgentMessageChunkUpdate("native-fork", schema.ContentChunk{Content: acp.NewTextContent("native reply")}))
			}
			_ = conn.Notify(ctx, "session/update", map[string]any{"sessionId": "native-fork", "update": map[string]any{"sessionUpdate": "available_commands_update", "availableCommands": []any{map[string]any{"name": "branch", "description": "Branch command"}}}})
			return schema.NewSessionResponse{SessionID: "native-fork"}, nil
		case "session/prompt":
			var p schema.PromptRequest
			if err := json.Unmarshal(raw, &p); err != nil {
				return nil, err
			}
			_ = conn.Notify(ctx, "session/update", acp.NewAgentMessageChunkUpdate(p.SessionID, schema.ContentChunk{Content: acp.NewTextContent("native reply")}))
			return schema.PromptResponse{StopReason: schema.StopReasonEndTurn}, nil
		}
		return nil, &acp.RPCError{Code: -32601, Message: "not supported"}
	}, nil)
	close(ready)
	<-conn.Done()
	os.Exit(0)
}

func openTest(t *testing.T, mode, cwd, data string, st store.Store) *client.Client {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	t.Cleanup(cancel)
	c, err := client.Open(ctx, "demo", cwd, config.Command{Command: os.Args[0], Args: []string{"-test.run=^TestAgentProcess$"}, Env: map[string]string{"MICRO_ACP_TEST_HELPER": mode, "MICRO_ACP_TEST_DATA": data}}, st)
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

func TestClientReportsBuildVersion(t *testing.T) {
	openTest(t, "version", t.TempDir(), t.TempDir(), store.Store{Directory: t.TempDir()})
}

func require(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func TestSessionLifecycleAcrossProcesses(t *testing.T) {
	cwd, data := t.TempDir(), t.TempDir()
	st := store.Store{Directory: t.TempDir()}
	c := openTest(t, "demo", cwd, data, st)
	require(t, c.New())
	_, err := c.Prompt("hello")
	require(t, err)
	parent, _ := c.Snapshot()
	if len(parent.Messages) != 2 || !strings.Contains(parent.Messages[1].Text, "hello") {
		t.Fatalf("bad transcript: %+v", parent)
	}
	require(t, c.Fork(true))
	fork, _ := c.Snapshot()
	if fork.ParentID != parent.ID || fork.RemoteID == parent.RemoteID || fork.PendingContext == "" {
		t.Fatalf("bad context fork: %+v", fork)
	}
	_, err = c.Prompt("branch")
	require(t, err)
	fork, _ = c.Snapshot()
	if fork.PendingContext != "" {
		t.Fatal("context was not consumed")
	}
	require(t, c.Shutdown())
	c2 := openTest(t, "demo", cwd, data, st)
	require(t, c2.Load(parent))
	loaded, _ := c2.Snapshot()
	if len(loaded.Messages) != 2 || loaded.Messages[1].Text != parent.Messages[1].Text {
		t.Fatalf("history was duplicated or lost: %+v", loaded.Messages)
	}
	all, err := c2.Sessions()
	require(t, err)
	if len(all) != 2 {
		t.Fatalf("got %d sessions", len(all))
	}
	require(t, c2.Delete(fork, false))
	all, err = c2.Sessions()
	require(t, err)
	if len(all) != 1 {
		t.Fatalf("delete did not reach agent: %+v", all)
	}
	if _, err := st.Load(fork.ID); !os.IsNotExist(err) {
		t.Fatalf("local fork still exists: %v", err)
	}
}

func TestNativeForkUsesAdvertisedCapability(t *testing.T) {
	c := openTest(t, "native", t.TempDir(), t.TempDir(), store.Store{Directory: t.TempDir()})
	require(t, c.New())
	_, err := c.Prompt("hello")
	require(t, err)
	parent, _ := c.Snapshot()
	require(t, c.Fork(false))
	fork, _ := c.Snapshot()
	if fork.RemoteID != "native-fork" || fork.ParentID != parent.ID || fork.ForkKind != "native" || len(fork.Messages) != 2 {
		t.Fatalf("incorrect fork: %+v", fork)
	}
	if len(fork.Commands) != 1 || fork.Commands[0].Name != "branch" {
		t.Fatal("fork setup notifications discarded")
	}
	if err := c.Delete(fork, false); err == nil {
		t.Fatal("unadvertised delete was accepted")
	}
}

func TestPermissionsCancellationAndNextTurn(t *testing.T) {
	c := openTest(t, "demo", t.TempDir(), t.TempDir(), store.Store{Directory: t.TempDir()})
	require(t, c.New())
	type result struct {
		reason schema.StopReason
		err    error
	}
	done := make(chan result, 1)
	go func() { r, e := c.Prompt("permission"); done <- result{r, e} }()
	select {
	case p := <-c.Permissions:
		p.Reply <- schema.RequestPermissionOutcome{Selected: &schema.SelectedPermissionOutcome{OptionID: "allow"}}
	case <-time.After(5 * time.Second):
		t.Fatal("permission request did not arrive")
	}
	r := <-done
	require(t, r.err)
	go func() { r, e := c.Prompt("slow"); done <- result{r, e} }()
	deadline := time.Now().Add(3 * time.Second)
	for {
		s, _ := c.Snapshot()
		if len(s.Messages) >= 4 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("no streamed output")
		}
		time.Sleep(10 * time.Millisecond)
	}
	require(t, c.Cancel())
	select {
	case r = <-done:
		require(t, r.err)
		if r.reason != schema.StopReasonCancelled {
			t.Fatalf("stop reason %s", r.reason)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("cancel did not finish")
	}
	_, err := c.Prompt("next turn")
	require(t, err)
	s, _ := c.Snapshot()
	if !strings.Contains(s.Messages[len(s.Messages)-1].Text, "next turn") {
		t.Fatal("connection did not survive cancellation")
	}
}

func TestCancelWhileWaitingForPermission(t *testing.T) {
	c := openTest(t, "demo", t.TempDir(), t.TempDir(), store.Store{Directory: t.TempDir()})
	require(t, c.New())
	done := make(chan error, 1)
	go func() { _, err := c.Prompt("permission"); done <- err }()
	var p client.Permission
	select {
	case p = <-c.Permissions:
	case <-time.After(3 * time.Second):
		t.Fatal("missing permission")
	}
	require(t, c.Cancel())
	select {
	case <-p.Done:
	case <-time.After(time.Second):
		t.Fatal("permission was not cancelled")
	}
	select {
	case err := <-done:
		require(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("prompt did not cancel")
	}
	_, err := c.Prompt("next turn after permission cancellation")
	require(t, err)
	s, _ := c.Snapshot()
	if !strings.Contains(s.Messages[len(s.Messages)-1].Text, "next turn after permission cancellation") {
		t.Fatal("connection did not survive permission cancellation")
	}
}

func TestNativeForkReplayReplacesInheritedHistory(t *testing.T) {
	c := openTest(t, "native-replay", t.TempDir(), t.TempDir(), store.Store{Directory: t.TempDir()})
	require(t, c.New())
	_, err := c.Prompt("hello")
	require(t, err)
	parent, _ := c.Snapshot()
	require(t, c.Fork(false))
	fork, _ := c.Snapshot()
	if len(fork.Messages) != len(parent.Messages) {
		t.Fatalf("fork duplicated history: %#v", fork.Messages)
	}
	for i, msg := range fork.Messages {
		if msg.Text != parent.Messages[i].Text || msg.Role != parent.Messages[i].Role {
			t.Fatalf("fork corrupted message %d: got %q want %q", i, msg.Text, parent.Messages[i].Text)
		}
	}
}

func TestRemoteOnlySessionLoadsHistoryWhenResumeIsAlsoAvailable(t *testing.T) {
	cwd := t.TempDir()
	c := openTest(t, "native-replay", cwd, t.TempDir(), store.Store{Directory: t.TempDir()})
	// Session listings provide metadata, but no locally saved conversation.
	remote := store.NewSession("demo", "remote-existing", cwd)
	require(t, c.Load(remote))
	loaded, _ := c.Snapshot()
	if len(loaded.Messages) != 2 || loaded.Messages[0].Role != "user" || loaded.Messages[1].Text != "native reply" {
		t.Fatalf("remote session did not restore its history: %+v", loaded.Messages)
	}
}
