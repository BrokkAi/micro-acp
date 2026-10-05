package client

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	acp "github.com/BrokkAi/acp-go"
	"github.com/BrokkAi/acp-go/schema"
	schema2 "github.com/BrokkAi/acp-go/schema/v2"
	draft "github.com/BrokkAi/acp-go/schema/v2/unstable"
	acpv2 "github.com/BrokkAi/acp-go/v2"
	"github.com/BrokkAi/micro-acp/internal/buildinfo"
)

// The ACP v2 draft shares no wire types with v1. This client keeps its v1
// session model and maps v2 into it at the edges: the handshake, permission
// and form requests, and session updates. Session operations branch on v2 in
// their own methods. Updates decode with schema/v2/unstable, the superset that
// also carries notices, compaction and subagents.

// Turn states reported by state_update.
const (
	TurnRunning        = "running"
	TurnRequiresAction = "requires_action"
	TurnIdle           = "idle"
	TurnUnknown        = "unknown"
)

type v2State struct {
	conn        *acpv2.Connection
	init        acpv2.Initialization
	tracker     *acpv2.SessionTracker
	permissions *acpv2.CancellablePermissions
	// Guarded by Client.mu.
	state     string
	idleMeta  json.RawMessage
	echo      bool
	echoID    string
	terminals map[string]*agentTerminal
}

func newV2State(c *Client) *v2State {
	return &v2State{
		tracker:     acpv2.NewSessionTracker(),
		permissions: acpv2.NewCancellablePermissions(v2Permissions{c}),
		terminals:   map[string]*agentTerminal{},
	}
}

// Protocol is the negotiated ACP version.
func (c *Client) Protocol() int {
	if c.v2 != nil {
		return 2
	}
	return 1
}

// TurnState is the agent's last reported v2 turn state: running,
// requires_action, idle or unknown. It is empty for v1, which has none.
func (c *Client) TurnState() string {
	if c.v2 == nil {
		return ""
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.v2.state
}

type v2Route struct {
	c *Client
	// state is fixed at creation: c.v2 is cleared when the agent answers v1.
	state       *v2State
	elicitation bool
	ready       chan<- routed
}

func (r *v2Route) RequestHandler() acp.Handler {
	host := acpv2.HandleClientHost(r.state.permissions, v2Elicitation{r.c})
	return func(ctx context.Context, method string, raw json.RawMessage) (any, error) {
		r.c.bindSetupSession(raw)
		// v2 removed client filesystem and terminal methods; only interactions remain.
		return host(ctx, method, raw)
	}
}

func (r *v2Route) NotificationHandler() acp.Notifications {
	tracker := r.state.tracker
	return func(method string, raw json.RawMessage) error {
		// Apply the update before the tracker can end a waiting turn, so the
		// turn sees everything up to its idle state.
		if err := r.c.v2Notification(method, raw); err != nil {
			return err
		}
		if method == schema2.SessionUpdateMethodName {
			var update acpv2.Update
			if json.Unmarshal(raw, &update) == nil {
				tracker.Observe(update)
			}
		}
		return nil
	}
}

func (r *v2Route) Serve(ctx context.Context, conn *acpv2.Connection) error {
	capabilities := schema2.ClientCapabilities{Auth: &schema2.AuthCapabilities{Terminal: &schema2.TerminalAuthCapabilities{}}}
	if r.elicitation {
		capabilities.Elicitation = acpv2.ElicitationClientCapabilities(true, true)
	}
	setup, stop := context.WithTimeout(ctx, 45*time.Second)
	var raw json.RawMessage
	err := conn.Call(setup, schema2.InitializeMethodName, schema2.InitializeRequest{
		Capabilities: &capabilities, Info: schema2.Implementation{Name: "micro-acp", Version: buildinfo.Version}, ProtocolVersion: acpv2.Version,
	}, &raw)
	stop()
	if err != nil {
		return err
	}
	r.state.conn = conn
	r.ready <- routed{conn: conn.Connection, v2: true, raw: raw}
	select {
	case <-ctx.Done():
	case <-conn.Done():
	}
	return nil
}

// useV2Initialization records the v2 handshake and a v1-shaped copy for the
// parts of the client that read capabilities.
func (c *Client) useV2Initialization(raw json.RawMessage) error {
	var init acpv2.Initialization
	var extended draft.InitializeResponse
	if err := json.Unmarshal(raw, &init); err != nil {
		return err
	}
	if err := json.Unmarshal(raw, &extended); err != nil {
		return err
	}
	if init.ProtocolVersion != acpv2.Version {
		return fmt.Errorf("agent selected unsupported ACP version %d", init.ProtocolVersion)
	}
	if init.Capabilities == nil || init.Capabilities.Session == nil {
		return errors.New("agent did not advertise the ACP v2 session methods")
	}
	c.v2.init = init
	on := true
	title := nullableText(extended.Info.Title)
	c.Init = acp.Initialization{ProtocolVersion: 2, AgentInfo: &schema.Implementation{Name: init.Info.Name, Title: title, Version: init.Info.Version}}
	// List, resume and close are baseline v2 methods.
	session := &schema.SessionCapabilities{List: &schema.SessionListCapabilities{}, Resume: &schema.SessionResumeCapabilities{}, Close: &schema.SessionCloseCapabilities{}}
	caps := &schema.AgentCapabilities{SessionCapabilities: session, PromptCapabilities: &schema.PromptCapabilities{}}
	if s := init.Capabilities.Session; s != nil {
		if s.Delete != nil {
			session.Delete = &schema.SessionDeleteCapabilities{}
		}
		if s.AdditionalDirectories != nil {
			session.AdditionalDirectories = &schema.SessionAdditionalDirectoriesCapabilities{}
		}
		if p := s.Prompt; p != nil {
			if p.Image != nil {
				caps.PromptCapabilities.Image = &on
			}
			if p.Audio != nil {
				caps.PromptCapabilities.Audio = &on
			}
			if p.EmbeddedContext != nil {
				caps.PromptCapabilities.EmbeddedContext = &on
			}
		}
	}
	if len(init.AuthMethods) > 0 {
		caps.Auth = &schema.AgentAuthCapabilities{Logout: &schema.LogoutCapabilities{}}
	}
	c.Init.AgentCapabilities = caps
	for _, method := range extended.AuthMethods {
		switch {
		case method.Agent != nil:
			m := method.Agent
			c.Init.AuthMethods = append(c.Init.AuthMethods, schema.AuthMethod{Agent: &schema.AuthMethodAgent{ID: schema.AuthMethodId(m.MethodID), Name: m.Name, Description: nullableText(m.Description)}})
		case method.Terminal != nil:
			m := method.Terminal
			env := map[string]string{}
			for _, variable := range m.Env {
				env[variable.Name] = variable.Value
			}
			c.Init.AuthMethods = append(c.Init.AuthMethods, schema.AuthMethod{Terminal: &schema.AuthMethodTerminal{ID: schema.AuthMethodId(m.MethodID), Name: m.Name, Description: nullableText(m.Description), Args: m.Args, Env: env}})
		}
	}
	if extended.Capabilities != nil && extended.Capabilities.Session != nil {
		c.CanFork = extended.Capabilities.Session.Fork != nil
	}
	return nil
}

func nullableText[T ~string](value draft.Nullable[T]) *string {
	if !value.Set || value.Null {
		return nil
	}
	text := string(value.Value)
	return &text
}

// convert re-decodes a value between protocol versions whose JSON matches.
func convert[T any](value any) (T, error) {
	var result T
	b, err := json.Marshal(value)
	if err == nil {
		err = json.Unmarshal(b, &result)
	}
	return result, err
}

type v2Permissions struct{ c *Client }

func (h v2Permissions) RequestPermission(ctx context.Context, typed schema2.RequestPermissionRequest) (schema2.RequestPermissionResponse, error) {
	subagent, err := h.c.requestSession(schema.SessionId(typed.SessionID))
	if err != nil {
		return schema2.RequestPermissionResponse{}, err
	}
	request, err := convert[draft.RequestPermissionRequest](typed)
	if err != nil {
		return schema2.RequestPermissionResponse{}, &acp.RPCError{Code: -32602, Message: err.Error()}
	}
	options, err := convert[[]schema.PermissionOption](request.Options)
	if err != nil {
		return schema2.RequestPermissionResponse{}, &acp.RPCError{Code: -32602, Message: err.Error()}
	}
	title := request.Title
	tool := schema.ToolCallUpdate{ToolCallID: "permission", Title: &title}
	var details []string
	if description := nullableText(request.Description); description != nil {
		details = append(details, *description)
	}
	if subject := request.Subject; subject != nil {
		switch {
		case subject.ToolCall != nil:
			call := subject.ToolCall.ToolCall
			// The subject is an upsert of the tool call. Agents such as Claude
			// send an edit's file changes only here, so keep them in the transcript.
			h.c.mu.Lock()
			if messages := h.c.transcript(string(request.SessionID)); messages != nil {
				mergeV2Tool(toolMessage(messages, string(call.ToolCallID), false), call)
				h.c.revision++
			}
			h.c.mu.Unlock()
			tool.ToolCallID = schema.ToolCallId(call.ToolCallID)
			tool.RawInput = call.RawInput
			if locations, err := convert[[]schema.ToolCallLocation](call.Locations.Value); err == nil && call.Locations.Set {
				tool.Locations = locations
			}
			if call.Content.Set {
				for _, part := range call.Content.Value {
					if text := v2ContentText(part); text != "" {
						details = append(details, text)
					}
				}
			}
		case subject.Command != nil:
			details = append(details, "$ "+subject.Command.Command+"\nin "+string(subject.Command.Cwd))
		}
	}
	for _, text := range details {
		tool.Content = append(tool.Content, schema.ToolCallContent{Content: &schema.Content{Content: acp.NewTextContent(text)}})
	}
	outcome := h.c.askPermission(ctx, schema.RequestPermissionRequest{SessionID: schema.SessionId(request.SessionID), ToolCall: tool, Options: options}, subagent)
	return convert[schema2.RequestPermissionResponse](schema.RequestPermissionResponse{Outcome: outcome})
}

// v2ContentText is a text form of one v2 tool content block for dialogs.
func v2ContentText(part draft.ToolCallContent) string {
	switch {
	case part.Content != nil:
		if block, err := convert[schema.ContentBlock](part.Content.Content); err == nil {
			return ContentText(block)
		}
	case part.Diff != nil:
		if part.Diff.Patch != nil {
			return part.Diff.Patch.Text
		}
		var rows []string
		for _, change := range part.Diff.Changes {
			rows = append(rows, fileChange(change).String())
		}
		return strings.Join(rows, "\n")
	case part.Terminal != nil:
		return "Terminal: " + string(part.Terminal.TerminalID)
	}
	return ""
}

type v2Elicitation struct{ c *Client }

// The v2 form and URL requests have the v1 wire shape, so the v1 handler
// validates and presents them.
func (h v2Elicitation) CreateElicitation(ctx context.Context, request schema2.CreateElicitationRequest) (schema2.CreateElicitationResponse, error) {
	raw, err := json.Marshal(request)
	if err != nil {
		return schema2.CreateElicitationResponse{}, err
	}
	response, err := h.c.elicit(ctx, raw)
	if err != nil {
		return schema2.CreateElicitationResponse{}, err
	}
	return convert[schema2.CreateElicitationResponse](response)
}

// turnError reads the JSON-RPC error a failed v2 turn reports in its idle
// _meta, such as claudeCode.error or graff/error.
func turnError(meta json.RawMessage) error {
	var fields map[string]json.RawMessage
	_ = json.Unmarshal(meta, &fields)
	describe := func(raw json.RawMessage) error {
		var text string
		if json.Unmarshal(raw, &text) == nil && text != "" {
			return errors.New(text)
		}
		var rpc acp.RPCError
		if json.Unmarshal(raw, &rpc) == nil && rpc.Message != "" {
			return &rpc
		}
		return nil
	}
	for key, value := range fields {
		if strings.HasSuffix(key, "/error") {
			if err := describe(value); err != nil {
				return err
			}
		}
		var nested map[string]json.RawMessage
		if json.Unmarshal(value, &nested) == nil {
			if raw, ok := nested["error"]; ok {
				if err := describe(raw); err != nil {
					return err
				}
			}
		}
	}
	return errors.New("the agent reported that the turn failed")
}
