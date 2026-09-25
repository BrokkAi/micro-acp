// Package demo provides a local, deterministic ACP agent for trying the UI.
package demo

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	acp "github.com/BrokkAi/acp-go"
	"github.com/BrokkAi/acp-go/agent"
	"github.com/BrokkAi/acp-go/schema"
	"github.com/BrokkAi/micro-acp/internal/store"
)

type Agent struct {
	Store    store.Store
	mu       sync.Mutex
	settings map[schema.SessionId][]schema.SessionConfigOption
}

func (a *Agent) options(id schema.SessionId) []schema.SessionConfigOption {
	if a.settings == nil {
		a.settings = map[schema.SessionId][]schema.SessionConfigOption{}
	}
	if options, ok := a.settings[id]; ok {
		return options
	}
	category := schema.SessionConfigOptionCategoryMode
	options := []schema.SessionConfigOption{
		{ID: "mode", Name: "Demo response", Category: &category, Select: &schema.SessionConfigSelect{CurrentValue: "walkthrough", Options: []schema.SessionConfigSelectOption{{Value: "walkthrough", Name: "Walkthrough"}, {Value: "brief", Name: "Brief"}}}},
		{ID: "stream", Name: "Stream words", Boolean: &schema.SessionConfigBoolean{CurrentValue: true}},
	}
	a.settings[id] = options
	return options
}

func (a *Agent) SetConfigOption(_ context.Context, _ agent.Client, r schema.SetSessionConfigOptionRequest) (schema.SetSessionConfigOptionResponse, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	options := append([]schema.SessionConfigOption(nil), a.options(r.SessionID)...)
	switch {
	case r.ConfigID == "mode" && r.ValueID != nil && (r.ValueID.Value == "walkthrough" || r.ValueID.Value == "brief"):
		value := *options[0].Select
		value.CurrentValue = r.ValueID.Value
		options[0].Select = &value
	case r.ConfigID == "stream" && r.Boolean != nil:
		options[1].Boolean = &schema.SessionConfigBoolean{CurrentValue: r.Boolean.Value}
	default:
		return schema.SetSessionConfigOptionResponse{}, fmt.Errorf("invalid demo setting")
	}
	a.settings[r.SessionID] = options
	return schema.SetSessionConfigOptionResponse{ConfigOptions: options}, nil
}

func Run(ctx context.Context, directory string) error {
	return agent.New(&Agent{Store: store.Store{Directory: directory}}).Serve(ctx, os.Stdin, os.Stdout)
}
func (a *Agent) Initialize(context.Context, agent.Client, schema.InitializeRequest) (schema.InitializeResponse, error) {
	load := true
	return schema.InitializeResponse{ProtocolVersion: 1, AgentInfo: &schema.Implementation{Name: "micro-acp demo", Version: "1.0.0"}, AgentCapabilities: &schema.AgentCapabilities{LoadSession: &load, SessionCapabilities: &schema.SessionCapabilities{List: &schema.SessionListCapabilities{}, Close: &schema.SessionCloseCapabilities{}, Delete: &schema.SessionDeleteCapabilities{}}}}, nil
}
func (a *Agent) NewSession(_ context.Context, _ agent.Client, r schema.NewSessionRequest) (schema.NewSessionResponse, error) {
	s := store.NewSession("demo", "", r.Cwd)
	s.RemoteID = s.ID
	if err := a.Store.Save(s); err != nil {
		return schema.NewSessionResponse{}, err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	return schema.NewSessionResponse{SessionID: schema.SessionId(s.ID), ConfigOptions: a.options(schema.SessionId(s.ID))}, nil
}
func (a *Agent) LoadSession(ctx context.Context, c agent.Client, r schema.LoadSessionRequest) (schema.LoadSessionResponse, error) {
	s, err := a.Store.Load(string(r.SessionID))
	if err != nil {
		return schema.LoadSessionResponse{}, err
	}
	for _, m := range s.Messages {
		chunk := schema.ContentChunk{Content: acp.NewTextContent(m.Text)}
		update := acp.NewAgentMessageChunkUpdate(r.SessionID, chunk)
		if m.Role == "user" {
			update = acp.NewUserMessageChunkUpdate(r.SessionID, chunk)
		}
		if err := c.Notify(ctx, schema.SessionUpdateMethodName, update); err != nil {
			return schema.LoadSessionResponse{}, err
		}
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	return schema.LoadSessionResponse{ConfigOptions: a.options(r.SessionID)}, nil
}
func (a *Agent) CloseSession(context.Context, agent.Client, schema.CloseSessionRequest) (schema.CloseSessionResponse, error) {
	return schema.CloseSessionResponse{}, nil
}
func (a *Agent) ListSessions(_ context.Context, _ agent.Client, r schema.ListSessionsRequest) (schema.ListSessionsResponse, error) {
	all, err := a.Store.List()
	response := schema.ListSessionsResponse{Sessions: []schema.SessionInfo{}}
	for _, s := range all {
		if r.Cwd != nil && *r.Cwd != s.Cwd {
			continue
		}
		title := s.Title
		updated := s.UpdatedAt.Format(time.RFC3339)
		response.Sessions = append(response.Sessions, schema.SessionInfo{SessionID: schema.SessionId(s.RemoteID), Cwd: s.Cwd, Title: &title, UpdatedAt: &updated})
	}
	return response, err
}
func (a *Agent) DeleteSession(_ context.Context, _ agent.Client, r schema.DeleteSessionRequest) (schema.DeleteSessionResponse, error) {
	return schema.DeleteSessionResponse{}, a.Store.Delete(string(r.SessionID))
}
func (a *Agent) Prompt(ctx context.Context, c agent.Client, r schema.PromptRequest, updates agent.SessionUpdater) (schema.PromptResponse, error) {
	s, err := a.Store.Load(string(r.SessionID))
	if err != nil {
		return schema.PromptResponse{}, err
	}
	var input strings.Builder
	for _, block := range r.Prompt {
		if block.Text != nil {
			input.WriteString(block.Text.Text)
		}
	}
	text := input.String()
	if i := strings.LastIndex(text, "New user request:\n"); i >= 0 {
		text = text[i+len("New user request:\n"):]
	}
	s.Messages = append(s.Messages, store.Message{Role: "user", Text: text})
	if s.Title == "New session" {
		runes := []rune(text)
		s.Title = string(runes[:min(len(runes), 60)])
	}
	a.mu.Lock()
	options := a.options(r.SessionID)
	brief, stream := options[0].Select.CurrentValue == "brief", options[1].Boolean.CurrentValue
	a.mu.Unlock()
	answer := "This is the **local demo agent**. No model or credentials are being used.\n\nYou said:\n\n> " + strings.ReplaceAll(text, "\n", "\n> ")
	if !brief {
		answer += "\n\nTry `/settings`, `/new`, `/sessions`, `/fork --context`, or `/delete`. Send `permission` for an approval dialog, `form` for structured input, or `slow` to test cancellation."
	}
	if strings.EqualFold(text, "form") {
		var response schema.CreateElicitationResponse
		params := map[string]any{"sessionId": r.SessionID, "mode": "form", "message": "Choose how this local demo should respond.", "requestedSchema": map[string]any{"type": "object", "properties": map[string]any{"name": map[string]any{"type": "string", "title": "Your name", "default": "Ada", "minLength": 1}, "style": map[string]any{"type": "string", "title": "Greeting", "enum": []string{"hello", "welcome"}, "default": "hello"}}, "required": []string{"name", "style"}}}
		if err := c.Call(ctx, schema.ElicitationCreateMethodName, params, &response); err != nil {
			return schema.PromptResponse{}, err
		}
		answer = "Input request dismissed."
		if response.Accept != nil {
			answer = fmt.Sprintf("%v, **%v**! This response came from your form answers.", response.Accept.Content["style"], response.Accept.Content["name"])
		}
	}
	if strings.EqualFold(text, "permission") {
		title := "Demonstrate a permission request (no command will be executed)"
		response, err := c.RequestPermission(ctx, schema.RequestPermissionRequest{SessionID: r.SessionID, ToolCall: schema.ToolCallUpdate{ToolCallID: "demo-permission", Title: &title}, Options: []schema.PermissionOption{{OptionID: "allow", Name: "Allow once", Kind: schema.PermissionOptionKindAllowOnce}, {OptionID: "reject", Name: "Reject", Kind: schema.PermissionOptionKindRejectOnce}}})
		if err != nil {
			return schema.PromptResponse{}, err
		}
		answer = "Permission cancelled."
		if response.Outcome.Selected != nil {
			answer = fmt.Sprintf("Selected permission: **%s**. No command was executed.", response.Outcome.Selected.OptionID)
		}
	}
	if strings.EqualFold(text, "slow") {
		answer = strings.Repeat("Streaming a local demo response. Press Esc to stop. ", 60)
	}
	var emitted strings.Builder
	reason := schema.StopReasonEndTurn
	delay := time.Duration(0)
	if stream {
		delay = 20 * time.Millisecond
	}
	for _, word := range strings.SplitAfter(answer, " ") {
		select {
		case <-ctx.Done():
			reason = schema.StopReasonCancelled
			goto finished
		case <-time.After(delay):
		}
		if err := updates.Update(schema.SessionUpdate{AgentMessageChunk: &schema.ContentChunk{Content: acp.NewTextContent(word)}}); err != nil {
			return schema.PromptResponse{}, err
		}
		emitted.WriteString(word)
	}
finished:
	s.Messages = append(s.Messages, store.Message{Role: "assistant", Text: emitted.String()})
	s.UpdatedAt = time.Now().UTC()
	if err := a.Store.Save(s); err != nil {
		return schema.PromptResponse{}, err
	}
	return schema.PromptResponse{StopReason: reason}, nil
}
