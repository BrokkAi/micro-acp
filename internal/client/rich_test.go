package client_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"

	acp "github.com/BrokkAi/acp-go"
	"github.com/BrokkAi/acp-go/schema"
	"github.com/BrokkAi/micro-acp/internal/store"
)

func serveRichAgent() {
	var mu sync.Mutex
	model, effort, toggle := "fast", "low", false
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
			return map[string]any{"protocolVersion": 1, "agentCapabilities": map[string]any{"promptCapabilities": map[string]any{"image": true, "audio": true, "embeddedContext": true}, "mcpCapabilities": map[string]any{"http": true, "sse": true}, "sessionCapabilities": map[string]any{"close": map[string]any{}, "additionalDirectories": map[string]any{}}, "auth": map[string]any{"logout": map[string]any{}}}}, nil
		case "session/new":
			mu.Lock()
			defer mu.Unlock()
			return map[string]any{"sessionId": "rich-session", "configOptions": options()}, nil
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
