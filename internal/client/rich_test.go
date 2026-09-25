package client_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	acp "github.com/BrokkAi/acp-go"
	"github.com/BrokkAi/acp-go/schema"
	"github.com/BrokkAi/micro-acp/internal/client"
	"github.com/BrokkAi/micro-acp/internal/config"
	"github.com/BrokkAi/micro-acp/internal/store"
)

func serveRichAgent() {
	var mu sync.Mutex
	model, effort, toggle := "fast", "low", false
	var sessionRequest schema.NewSessionRequest
	options := func() []any {
		levels := []any{map[string]any{"value": "low", "name": "Low"}}
		if model == "deep" {
			levels = append(levels, map[string]any{"value": "high", "name": "High"})
		}
		return []any{
			map[string]any{"id": "engine", "name": "Model", "category": "model", "type": "select", "currentValue": model, "options": []any{map[string]any{"group": "provider", "name": "Provider", "options": []any{map[string]any{"value": "fast", "name": "Fast"}, map[string]any{"value": "deep", "name": "Deep"}}}}},
			map[string]any{"id": "effort", "name": "Reasoning", "category": "thought_level", "type": "select", "currentValue": effort, "options": levels},
			map[string]any{"id": "review", "name": "Review", "type": "boolean", "currentValue": toggle},
		}
	}
	var conn *acp.Connection
	ready := make(chan struct{})
	conn = acp.Connect(os.Stdin, os.Stdout, func(ctx context.Context, method string, raw json.RawMessage) (any, error) {
		<-ready
		switch method {
		case "initialize":
			var p schema.InitializeRequest
			_ = json.Unmarshal(raw, &p)
			if p.ClientCapabilities.Session == nil || p.ClientCapabilities.Session.ConfigOptions.Boolean == nil {
				return nil, fmt.Errorf("boolean options not advertised")
			}
			if p.ClientCapabilities.Elicitation == nil || p.ClientCapabilities.Elicitation.Form == nil || p.ClientCapabilities.Elicitation.URL == nil || p.ClientCapabilities.Auth == nil || p.ClientCapabilities.Auth.Terminal == nil || !*p.ClientCapabilities.Auth.Terminal {
				return nil, fmt.Errorf("interactive capabilities missing")
			}
			return map[string]any{"protocolVersion": 1, "authMethods": []any{map[string]any{"id": "login", "name": "Login"}, map[string]any{"type": "terminal", "id": "terminal-login", "name": "Terminal login", "args": []string{"login"}, "env": map[string]string{"LOGIN_TEST": "yes"}}}, "agentCapabilities": map[string]any{"promptCapabilities": map[string]any{"image": true, "audio": true, "embeddedContext": true}, "mcpCapabilities": map[string]any{"http": true, "sse": true}, "sessionCapabilities": map[string]any{"close": map[string]any{}, "additionalDirectories": map[string]any{}}, "auth": map[string]any{"logout": map[string]any{}}}}, nil
		case "session/new":
			mu.Lock()
			defer mu.Unlock()
			_ = json.Unmarshal(raw, &sessionRequest)
			return map[string]any{"sessionId": "rich-session", "configOptions": options()}, nil
		case "authenticate":
			return map[string]any{}, nil
		case "session/set_config_option":
			var p struct {
				ConfigID string `json:"configId"`
				Value    any    `json:"value"`
			}
			if err := json.Unmarshal(raw, &p); err != nil {
				return nil, err
			}
			mu.Lock()
			defer mu.Unlock()
			switch p.ConfigID {
			case "engine":
				model = p.Value.(string)
				effort = "low"
			case "effort":
				effort = p.Value.(string)
			case "review":
				toggle = p.Value.(bool)
			}
			return map[string]any{"configOptions": options()}, nil
		case "logout", "session/close":
			return map[string]any{}, nil
		case "session/prompt":
			var p schema.PromptRequest
			_ = json.Unmarshal(raw, &p)
			text := ""
			if len(p.Prompt) > 0 && p.Prompt[0].Text != nil {
				text = p.Prompt[0].Text.Text
			}
			if text == "form" || text == "url" {
				params := map[string]any{"sessionId": p.SessionID, "mode": text, "message": "Please provide input"}
				if text == "form" {
					params["requestedSchema"] = map[string]any{"type": "object", "properties": map[string]any{"name": map[string]any{"type": "string"}}, "required": []string{"name"}}
				} else {
					params["url"] = "https://example.com/login"
					params["elicitationId"] = "login-flow"
				}
				var reply schema.CreateElicitationResponse
				if err := conn.Call(ctx, "elicitation/create", params, &reply); err != nil {
					return nil, err
				}
				if text == "form" && reply.Accept != nil && reply.Accept.Content["name"] != "Ada" {
					return nil, fmt.Errorf("form response corrupted")
				}
				if text == "url" && reply.Accept != nil {
					_ = conn.Notify(ctx, "elicitation/complete", map[string]any{"elicitationId": "login-flow"})
				}
				return schema.PromptResponse{StopReason: schema.StopReasonEndTurn}, nil
			}
			if text == "host" {
				mu.Lock()
				setup := sessionRequest
				mu.Unlock()
				if len(setup.AdditionalDirectories) != 1 || len(setup.MCPServers) != 3 {
					return nil, fmt.Errorf("session options were not forwarded")
				}
				path := filepath.Join(setup.AdditionalDirectories[0], "written.txt")
				if err := conn.Call(ctx, "fs/write_text_file", schema.WriteTextFileRequest{SessionID: p.SessionID, Path: path, Content: "hello\nworld"}, nil); err != nil {
					return nil, err
				}
				var read schema.ReadTextFileResponse
				if err := conn.Call(ctx, "fs/read_text_file", schema.ReadTextFileRequest{SessionID: p.SessionID, Path: path}, &read); err != nil {
					return nil, err
				}
				if read.Content != "hello\nworld" {
					return nil, fmt.Errorf("bad file read")
				}
				var terminal schema.CreateTerminalResponse
				if err := conn.Call(ctx, "terminal/create", map[string]any{"sessionId": p.SessionID, "command": "sh", "args": []string{"-c", "printf live-output"}, "cwd": setup.AdditionalDirectories[0]}, &terminal); err != nil {
					return nil, err
				}
				params := map[string]any{"sessionId": p.SessionID, "terminalId": terminal.TerminalID}
				if err := conn.Call(ctx, "terminal/wait_for_exit", params, nil); err != nil {
					return nil, err
				}
				var output schema.TerminalOutputResponse
				if err := conn.Call(ctx, "terminal/output", params, &output); err != nil {
					return nil, err
				}
				if output.Output != "live-output" {
					return nil, fmt.Errorf("missing terminal output")
				}
				if err := conn.Call(ctx, "terminal/kill", params, nil); err != nil {
					return nil, err
				}
				if err := conn.Call(ctx, "terminal/release", params, nil); err != nil {
					return nil, err
				}
				return schema.PromptResponse{StopReason: schema.StopReasonEndTurn}, nil
			}
			updates := []any{
				map[string]any{"sessionUpdate": "available_commands_update", "availableCommands": []any{map[string]any{"name": "compact", "description": "Compact the context"}}},
				map[string]any{"sessionUpdate": "usage_update", "used": 100, "size": 4096, "cost": map[string]any{"amount": 0.02, "currency": "USD"}},
				map[string]any{"sessionUpdate": "tool_call", "toolCallId": "edit", "title": "Edit file", "status": "in_progress", "rawInput": map[string]any{"path": "main.go"}, "content": []any{map[string]any{"type": "diff", "path": "main.go", "oldText": "old", "newText": "new"}}},
				map[string]any{"sessionUpdate": "tool_call_update", "toolCallId": "edit", "status": "completed", "rawOutput": map[string]any{"ok": true}},
				map[string]any{"sessionUpdate": "agent_message_chunk", "content": map[string]any{"type": "text", "text": "Received " + p.Prompt[0].Text.Text}},
			}
			for _, u := range updates {
				if err := conn.Notify(ctx, "session/update", map[string]any{"sessionId": p.SessionID, "update": u}); err != nil {
					return nil, err
				}
			}
			return schema.PromptResponse{StopReason: schema.StopReasonEndTurn}, nil
		}
		return nil, &acp.RPCError{Code: -32601, Message: method}
	}, nil)
	close(ready)
	<-conn.Done()
}

func TestElicitationAndAuthenticationRoundTrips(t *testing.T) {
	c := openTest(t, "rich", t.TempDir(), t.TempDir(), store.Store{Directory: t.TempDir()})
	if len(c.AuthChoices()) != 2 {
		t.Fatalf("auth choices: %+v", c.AuthChoices())
	}
	require(t, c.Authenticate("login"))
	cmd, err := c.AuthCommand("terminal-login")
	require(t, err)
	if cmd.Args[len(cmd.Args)-1] != "login" {
		t.Fatal("terminal auth arguments missing")
	}
	require(t, c.New())
	for _, mode := range []string{"form", "url"} {
		done := make(chan error, 1)
		go func() { _, err := c.Prompt(mode); done <- err }()
		select {
		case e := <-c.Elicitations:
			if mode == "form" {
				if e.Schema == nil || len(e.Schema.Properties) != 1 {
					t.Fatal("form schema dropped")
				}
				e.Reply <- acp.AcceptElicitation(map[string]schema.ElicitationContentValue{"name": "Ada"})
			} else {
				if e.URL != "https://example.com/login" || e.ID != "login-flow" {
					t.Fatal("URL fields dropped")
				}
				e.Reply <- acp.AcceptElicitation(nil)
			}
		case <-time.After(3 * time.Second):
			t.Fatal("elicitation missing")
		}
		require(t, <-done)
	}
	s, _ := c.Snapshot()
	found := false
	for _, m := range s.Messages {
		if m.Role == "notice" && strings.Contains(m.Text, "login-flow") {
			found = true
		}
	}
	if !found {
		t.Fatal("completion notification discarded")
	}
	require(t, c.CloseSession())
	s, _ = c.Snapshot()
	if s.ID != "" {
		t.Fatal("session still active after close")
	}
	require(t, c.Logout())
}

func TestMCPRootsFilesystemAndAllTerminalCallbacks(t *testing.T) {
	cwd, extra := t.TempDir(), t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	settings := config.SessionOptions{AdditionalDirectories: []string{extra}, MCPServers: []schema.McpServer{acp.NewStdioMCPServer("stdio", "server", []string{}, nil), acp.NewHTTPMCPServer("http", "https://example.com/mcp"), acp.NewSSEMCPServer("sse", "https://example.com/sse")}}
	c, err := client.Open(ctx, "rich", cwd, config.Command{Command: os.Args[0], Args: []string{"-test.run=^TestAgentProcess$"}, Env: map[string]string{"MICRO_ACP_TEST_HELPER": "rich"}}, store.Store{Directory: t.TempDir()}, settings)
	require(t, err)
	defer c.Shutdown()
	require(t, c.New())
	_, err = c.Prompt("host")
	require(t, err)
	b, err := os.ReadFile(filepath.Join(extra, "written.txt"))
	require(t, err)
	if string(b) != "hello\nworld" {
		t.Fatal("extra root not writable")
	}
	s, _ := c.Snapshot()
	found := false
	for _, message := range s.Messages {
		if message.Role == "terminal" && strings.Contains(message.Text, "live-output") && strings.Contains(message.Text, "Exit: 0") {
			found = true
		}
	}
	if !found {
		t.Fatalf("terminal output not projected: %+v", s.Messages)
	}
}

func TestAgentDrivenConfigurationAndRichUpdates(t *testing.T) {
	c := openTest(t, "rich", t.TempDir(), t.TempDir(), store.Store{Directory: t.TempDir()})
	require(t, c.New())
	selectors := c.Selectors()
	if len(selectors) != 3 || selectors[0].Choices[0].Group != "Provider" {
		t.Fatalf("missing grouped selector: %+v", selectors)
	}
	if err := c.Configure("effort", "high"); err == nil {
		t.Fatal("accepted effort not offered for current model")
	}
	require(t, c.Configure("model", "deep"))
	require(t, c.Configure("effort", "high"))
	require(t, c.SetConfig("review", "true"))
	_, err := c.Prompt("/compact keep recent work")
	require(t, err)
	s, _ := c.Snapshot()
	if len(s.Commands) != 1 || s.Commands[0].Name != "compact" || s.Usage == nil || s.Usage.Used != 100 {
		t.Fatalf("missing session state: %+v", s)
	}
	var tool string
	for _, entry := range s.Messages {
		if entry.Role == "tool" {
			tool = entry.Text
		}
	}
	for _, want := range []string{"completed", "main.go", "- old", "+ new", "\"ok\": true"} {
		if !strings.Contains(tool, want) {
			t.Errorf("tool missing %q: %s", want, tool)
		}
	}
	if !strings.Contains(c.Status(), "Deep") || !strings.Contains(c.Status(), "100/4096") {
		t.Fatalf("status: %s", c.Status())
	}
}
