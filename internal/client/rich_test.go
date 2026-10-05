package client_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
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
	mode := "ask"
	modes := func() map[string]any {
		return map[string]any{"currentModeId": mode, "availableModes": []any{map[string]any{"id": "ask", "name": "Ask"}, map[string]any{"id": "code", "name": "Code"}}}
	}
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
			if os.Getenv("MICRO_ACP_TEST_ELICIT_INIT") == "yes" {
				var response schema.CreateElicitationResponse
				params := map[string]any{"requestId": 1, "mode": "form", "message": "Initialize profile", "requestedSchema": map[string]any{"type": "object", "properties": map[string]any{"name": map[string]any{"type": "string"}}}}
				if err := conn.Call(ctx, "elicitation/create", params, &response); err != nil {
					return nil, err
				}
				if response.Accept == nil {
					return nil, fmt.Errorf("profile not provided")
				}
			}
			return map[string]any{"protocolVersion": 1, "authMethods": []any{map[string]any{"id": "login", "name": "Login"}, map[string]any{"type": "terminal", "id": "terminal-login", "name": "Terminal login", "args": []string{"login"}, "env": map[string]string{"LOGIN_TEST": "yes"}}}, "agentCapabilities": map[string]any{"promptCapabilities": map[string]any{"image": true, "audio": true, "embeddedContext": true}, "mcpCapabilities": map[string]any{"http": true, "sse": true}, "sessionCapabilities": map[string]any{"close": map[string]any{}, "resume": map[string]any{}, "additionalDirectories": map[string]any{}}, "auth": map[string]any{"logout": map[string]any{}}}}, nil
		case "session/new":
			mu.Lock()
			defer mu.Unlock()
			_ = json.Unmarshal(raw, &sessionRequest)
			// These updates arrive before the session/new response.
			_ = conn.Notify(ctx, "session/update", map[string]any{"sessionId": "rich-session", "update": map[string]any{"sessionUpdate": "available_commands_update", "availableCommands": []any{map[string]any{"name": "compact", "description": "Compact"}}}})
			return map[string]any{"sessionId": "rich-session", "configOptions": options(), "modes": modes()}, nil
		case "session/resume":
			var p schema.ResumeSessionRequest
			if err := json.Unmarshal(raw, &p); err != nil {
				return nil, err
			}
			if p.SessionID != "rich-session" || p.Cwd == "" {
				return nil, fmt.Errorf("invalid resume")
			}
			mu.Lock()
			defer mu.Unlock()
			return map[string]any{"configOptions": options(), "modes": modes()}, nil
		case "session/set_mode":
			var p schema.SetSessionModeRequest
			if err := json.Unmarshal(raw, &p); err != nil {
				return nil, err
			}
			mu.Lock()
			mode = string(p.ModeID)
			mu.Unlock()
			_ = conn.Notify(ctx, "session/update", map[string]any{"sessionId": p.SessionID, "update": map[string]any{"sessionUpdate": "current_mode_update", "currentModeId": p.ModeID}})
			return map[string]any{}, nil
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
			if text == "media" {
				if len(p.Prompt) != 6 || p.Prompt[1].Image == nil || p.Prompt[2].Audio == nil || p.Prompt[3].Resource == nil || p.Prompt[4].Resource == nil || p.Prompt[5].ResourceLink == nil {
					return nil, fmt.Errorf("rich prompt lost content: %s", raw)
				}
				for _, content := range p.Prompt[1:] {
					if err := conn.Notify(ctx, "session/update", acp.NewAgentMessageChunkUpdate(p.SessionID, schema.ContentChunk{Content: content})); err != nil {
						return nil, err
					}
				}
				return schema.PromptResponse{StopReason: schema.StopReasonEndTurn}, nil
			}
			if text == "cancel-form" {
				requestCtx, cancel := context.WithTimeout(ctx, 250*time.Millisecond)
				defer cancel()
				var response schema.CreateElicitationResponse
				_ = conn.Call(requestCtx, "elicitation/create", map[string]any{"sessionId": p.SessionID, "mode": "form", "message": "Cancelled input", "requestedSchema": map[string]any{"type": "object", "properties": map[string]any{}}}, &response)
				return schema.PromptResponse{StopReason: schema.StopReasonEndTurn}, nil
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
				line, limit := uint32(2), uint32(1)
				if err := conn.Call(ctx, "fs/read_text_file", schema.ReadTextFileRequest{SessionID: p.SessionID, Path: path, Line: &line, Limit: &limit}, &read); err != nil {
					return nil, err
				}
				if strings.TrimSpace(read.Content) != "world" {
					return nil, fmt.Errorf("line range ignored")
				}
				if err := conn.Call(ctx, "fs/read_text_file", schema.ReadTextFileRequest{SessionID: p.SessionID, Path: filepath.Join(setup.Cwd, "..", "outside.txt")}, &read); err == nil {
					return nil, fmt.Errorf("workspace escape permitted")
				}
				// Windows has no sh unless Git for Windows puts one on PATH.
				command, args := "sh", []string{"-c", "printf live-output"}
				if runtime.GOOS == "windows" {
					command, args = "cmd", []string{"/c", "echo live-output"}
				}
				var terminal schema.CreateTerminalResponse
				if err := conn.Call(ctx, "terminal/create", map[string]any{"sessionId": p.SessionID, "command": command, "args": args, "cwd": setup.AdditionalDirectories[0]}, &terminal); err != nil {
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
				// cmd's echo ends the line with CRLF; printf adds nothing.
				if strings.TrimRight(output.Output, "\r\n") != "live-output" {
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
			mu.Lock()
			configOptions := options()
			mu.Unlock()
			updates := []any{
				map[string]any{"sessionUpdate": "agent_thought_chunk", "content": map[string]any{"type": "text", "text": "Checking the requested change"}},
				map[string]any{"sessionUpdate": "plan", "entries": []any{map[string]any{"content": "Review the code", "priority": "high", "status": "completed"}}},
				map[string]any{"sessionUpdate": "session_info_update", "title": "Agent title", "updatedAt": "2026-09-25T12:00:00Z"},
				map[string]any{"sessionUpdate": "config_option_update", "configOptions": configOptions},
				map[string]any{"sessionUpdate": "available_commands_update", "availableCommands": []any{map[string]any{"name": "compact", "description": "Compact the context"}}},
				map[string]any{"sessionUpdate": "usage_update", "used": 100, "size": 4096, "cost": map[string]any{"amount": 0.02, "currency": "USD"}},
				map[string]any{"sessionUpdate": "tool_call", "toolCallId": "edit", "title": "Edit file", "status": "in_progress", "rawInput": map[string]any{"path": "main.go"}, "content": []any{map[string]any{"type": "diff", "path": "main.go", "oldText": "old", "newText": "new"}}},
				map[string]any{"sessionUpdate": "tool_call_update", "toolCallId": "edit", "status": "completed", "rawOutput": map[string]any{"ok": true}},
				map[string]any{"sessionUpdate": "tool_call_update", "toolCallId": "edit", "rawInput": nil, "rawOutput": nil},
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
	if len(selectors) != 4 || selectors[0].Choices[0].Group != "Provider" {
		t.Fatalf("missing grouped selector: %+v", selectors)
	}
	if !strings.Contains(c.Status(), "Review Off") {
		t.Fatalf("disabled toggle missing: %s", c.Status())
	}
	require(t, c.Configure("mode", "code"))
	if err := c.Configure("effort", "high"); err == nil {
		t.Fatal("accepted effort not offered for current model")
	}
	require(t, c.Configure("model", "deep"))
	require(t, c.Configure("effort", "high"))
	require(t, c.SetConfig("review", "true"))
	_, err := c.Prompt("/compact keep recent work")
	require(t, err)
	s, _ := c.Snapshot()
	if len(s.Commands) != 1 || s.Commands[0].Name != "compact" || s.Usage == nil || s.Usage.Used != 100 || s.Plan == nil || s.Title != "Agent title" {
		t.Fatalf("missing session state: %+v", s)
	}
	var tool string
	for _, entry := range s.Messages {
		if entry.Role == "tool" {
			tool = entry.Text
		}
	}
	for _, want := range []string{"completed", "\"path\": \"main.go\"", "- old", "+ new", "\"ok\": true"} {
		if !strings.Contains(tool, want) {
			t.Errorf("tool missing %q: %s", want, tool)
		}
	}
	if !strings.Contains(c.Status(), "Deep") || !strings.Contains(c.Status(), "Code") || !strings.Contains(c.Status(), "100/4096") || !strings.Contains(c.Status(), "Review On") {
		t.Fatalf("status: %s", c.Status())
	}
}

func TestElicitationDuringInitialization(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	events := client.Interactions{Elicitations: make(chan client.Elicitation, 1)}
	go func() {
		select {
		case e := <-events.Elicitations:
			e.Reply <- acp.AcceptElicitation(nil)
		case <-ctx.Done():
		}
	}()
	c, err := client.OpenInteractive(ctx, "rich", t.TempDir(), config.Command{Command: os.Args[0], Args: []string{"-test.run=^TestAgentProcess$"}, Env: map[string]string{"MICRO_ACP_TEST_HELPER": "rich", "MICRO_ACP_TEST_ELICIT_INIT": "yes"}}, store.Store{Directory: t.TempDir()}, events)
	require(t, err)
	defer c.Shutdown()
}

func TestAgentCancelsElicitationAndNextTurnWorks(t *testing.T) {
	c := openTest(t, "rich", t.TempDir(), t.TempDir(), store.Store{Directory: t.TempDir()})
	require(t, c.New())
	done := make(chan error, 1)
	go func() { _, err := c.Prompt("cancel-form"); done <- err }()
	select {
	case e := <-c.Elicitations:
		select {
		case <-e.Done:
		case <-time.After(3 * time.Second):
			t.Fatal("cancel_request did not dismiss form")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("no form request")
	}
	require(t, <-done)
	_, err := c.Prompt("next turn")
	require(t, err)
}

func TestResumeKeepsLocalTranscriptAndRefreshesState(t *testing.T) {
	c := openTest(t, "rich", t.TempDir(), t.TempDir(), store.Store{Directory: t.TempDir()})
	require(t, c.New())
	s, _ := c.Snapshot()
	if len(s.Commands) != 1 {
		t.Fatal("session/new updates discarded")
	}
	_, err := c.Prompt("remember this")
	require(t, err)
	s, _ = c.Snapshot()
	require(t, c.New())
	require(t, c.Load(s))
	loaded, _ := c.Snapshot()
	if len(loaded.Messages) != len(s.Messages) || loaded.ID != s.ID || len(c.Selectors()) != 4 {
		t.Fatal("resume lost transcript or configuration")
	}
}
