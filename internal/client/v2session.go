package client

import (
	"context"
	"errors"
	"fmt"
	"time"

	acp "github.com/BrokkAi/acp-go"
	"github.com/BrokkAi/acp-go/schema"
	schema2 "github.com/BrokkAi/acp-go/schema/v2"
	draft "github.com/BrokkAi/acp-go/schema/v2/unstable"
	acpv2 "github.com/BrokkAi/acp-go/v2"
	mcpv2 "github.com/BrokkAi/acp-go/v2/mcp"
)

// v2Servers converts the configured MCP servers. The v2 draft has stdio and
// HTTP transports only.
func (c *Client) v2Servers() ([]schema2.McpServer, error) {
	servers, err := convert[[]schema2.McpServer](c.mcpServers())
	if err != nil {
		return nil, err
	}
	for i, server := range servers {
		if server.Stdio == nil && server.HTTP == nil {
			return nil, fmt.Errorf("MCP server %d: ACP v2 supports only stdio and HTTP servers", i)
		}
	}
	return servers, nil
}

// v2Session builds the v1 session handle and initial commands from a v2
// session response.
func v2Session(id schema2.SessionId, options, commands any) (acp.Session, []schema.AvailableCommand) {
	converted, _ := convert[[]schema.AvailableCommand](commands)
	return acp.Session{SessionID: schema.SessionId(id), ConfigOptions: v1ConfigOptions(options)}, converted
}

func (c *Client) newV2(ctx context.Context, directories []string) (acp.Session, []schema.AvailableCommand, error) {
	servers, err := c.v2Servers()
	if err != nil {
		return acp.Session{}, nil, err
	}
	session, err := mcpv2.NewSession(ctx, c.v2.conn, c.v2.init, c.Cwd, mcpv2.NewSessionOptions{AdditionalDirectories: directories, Servers: servers})
	if err != nil {
		return acp.Session{}, nil, err
	}
	w, commands := v2Session(session.SessionID, session.ConfigOptions, session.AvailableCommands)
	return w, commands, nil
}

// resumeV2 resumes a session. With replay it asks for the whole history from
// the start; without it the agent resumes silently.
func (c *Client) resumeV2(ctx context.Context, id schema.SessionId, directories []string, replay bool) (acp.Session, []schema.AvailableCommand, error) {
	servers, err := c.v2Servers()
	if err != nil {
		return acp.Session{}, nil, err
	}
	var response schema2.ResumeSessionResponse
	if replay && len(servers) == 0 {
		response, err = c.v2.conn.ResumeSessionFromStart(ctx, c.v2.init, schema2.SessionId(id), c.Cwd, directories)
	} else {
		request := schema2.ResumeSessionRequest{SessionID: schema2.SessionId(id), Cwd: schema2.AbsolutePath(c.Cwd), MCPServers: servers}
		for _, directory := range directories {
			request.AdditionalDirectories = append(request.AdditionalDirectories, schema2.AbsolutePath(directory))
		}
		if replay {
			request.ReplayFrom = &schema2.ReplayFrom{Start: &schema2.ReplayFromStart{}}
		}
		response, err = mcpv2.ResumeSession(ctx, c.v2.conn, c.v2.init, request)
	}
	if err != nil {
		return acp.Session{}, nil, err
	}
	w, commands := v2Session(schema2.SessionId(id), response.ConfigOptions, response.AvailableCommands)
	return w, commands, nil
}

func (c *Client) forkV2(ctx context.Context, id string, directories []string) (acp.Session, []schema.AvailableCommand, error) {
	servers, err := c.v2Servers()
	if err != nil {
		return acp.Session{}, nil, err
	}
	request := draft.ForkSessionRequest{SessionID: draft.SessionId(id), Cwd: draft.AbsolutePath(c.Cwd)}
	request.MCPServers, err = convert[[]draft.McpServer](servers)
	if err != nil {
		return acp.Session{}, nil, err
	}
	for _, directory := range directories {
		request.AdditionalDirectories = append(request.AdditionalDirectories, draft.AbsolutePath(directory))
	}
	var response draft.ForkSessionResponse
	if err := c.conn.Call(ctx, draft.SessionForkMethodName, request, &response); err != nil {
		return acp.Session{}, nil, err
	}
	w, commands := v2Session(schema2.SessionId(response.SessionID), response.ConfigOptions, response.AvailableCommands)
	return w, commands, nil
}

// listV2 lists one page of remote sessions in the v1 shape. Agents without
// session/list report no sessions rather than an error.
func (c *Client) listV2(ctx context.Context, cursor *string) (schema.ListSessionsResponse, error) {
	cwd := schema2.AbsolutePath(c.Cwd)
	request := schema2.ListSessionsRequest{Cwd: &cwd}
	if cursor != nil {
		next := schema2.SessionListCursor(*cursor)
		request.Cursor = &next
	}
	response, err := c.v2.conn.ListSessions(ctx, c.v2.init, request)
	var rpcErr *acp.RPCError
	if errors.As(err, &rpcErr) && rpcErr.Code == -32601 {
		return schema.ListSessionsResponse{}, nil
	}
	if err != nil {
		return schema.ListSessionsResponse{}, err
	}
	var result schema.ListSessionsResponse
	if response.NextCursor != nil {
		next := string(*response.NextCursor)
		result.NextCursor = &next
	}
	for _, session := range response.Sessions {
		info := schema.SessionInfo{SessionID: schema.SessionId(session.SessionID), Cwd: string(session.Cwd)}
		for _, directory := range session.AdditionalDirectories {
			info.AdditionalDirectories = append(info.AdditionalDirectories, string(directory))
		}
		if session.Title.Set && !session.Title.Null {
			title := session.Title.Value
			info.Title = &title
		}
		if session.UpdatedAt.Set && !session.UpdatedAt.Null {
			updated := session.UpdatedAt.Value
			info.UpdatedAt = &updated
		}
		result.Sessions = append(result.Sessions, info)
	}
	return result, nil
}

// promptV2 submits a prompt and waits for the turn to go idle. The prompt
// response only says the agent took the message in.
func (c *Client) promptV2(ctx context.Context, blocks []acp.Content) (schema.StopReason, error) {
	content, err := convert[[]schema2.ContentBlock](blocks)
	if err != nil {
		return "", err
	}
	id := schema2.SessionId(c.session().SessionID)
	c.mu.Lock()
	c.v2.echo, c.v2.echoID = true, ""
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		c.v2.echo = false
		c.mu.Unlock()
	}()
	work := c.v2.tracker.BeginWork(id)
	messageID, err := c.v2.conn.PromptContent(ctx, c.v2.init, acpv2.Session{SessionID: id}, content)
	if err != nil {
		return "", err
	}
	c.mu.Lock()
	if c.v2.echoID == "" {
		c.v2.echoID = string(messageID)
		if c.turnStart < len(c.current.Messages) {
			c.current.Messages[c.turnStart].ID = string(messageID)
		}
	}
	c.mu.Unlock()
	select {
	case <-work.Done():
	case <-c.conn.Done():
		return "", errors.New("agent disconnected before the turn finished")
	case <-ctx.Done():
		return "", ctx.Err()
	}
	result := work.Result()
	if result.StopReason == nil {
		return schema.StopReasonEndTurn, nil
	}
	if *result.StopReason == "_error" {
		c.mu.Lock()
		meta := c.v2.idleMeta
		c.mu.Unlock()
		return "", turnError(meta)
	}
	return schema.StopReason(*result.StopReason), nil
}

// cancelAgentTurn stops a v2 turn the agent started on its own, which no
// prompt of this client waits on.
func (c *Client) cancelAgentTurn(id schema.SessionId) error {
	if c.v2 == nil || id == "" {
		return nil
	}
	c.mu.Lock()
	state := c.v2.state
	c.mu.Unlock()
	if state != TurnRunning && state != TurnRequiresAction {
		return nil
	}
	c.v2.permissions.CancelPermissionRequests(schema2.SessionId(id))
	ctx, cancel := context.WithTimeout(c.ctx, time.Second)
	defer cancel()
	return c.conn.CancelSession(ctx, id)
}

func (c *Client) setConfigV2(ctx context.Context, session schema.SessionId, id, value string, boolean bool) ([]schema.SessionConfigOption, error) {
	request := draft.SetSessionConfigOptionRequest{SessionID: draft.SessionId(session), ConfigID: draft.SessionConfigId(id)}
	if boolean {
		request.Boolean = &draft.SetSessionConfigOptionRequestBoolean{Value: value == "true"}
	} else {
		request.ID = &draft.SetSessionConfigOptionRequestID{Value: draft.SessionConfigValueId(value)}
	}
	var response draft.SetSessionConfigOptionResponse
	if err := c.conn.Call(ctx, draft.SessionSetConfigOptionMethodName, request, &response); err != nil {
		return nil, err
	}
	return v1ConfigOptions(response.ConfigOptions), nil
}
