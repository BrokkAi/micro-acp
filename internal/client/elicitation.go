package client

import (
	"context"
	"encoding/json"
	"net/url"

	acp "github.com/BrokkAi/acp-go"
	"github.com/BrokkAi/acp-go/schema"
	"github.com/BrokkAi/micro-acp/internal/store"
)

type Elicitation struct {
	Agent   string
	Request schema.CreateElicitationRequest
	Schema  *schema.ElicitationSchema
	URL     string
	ID      schema.ElicitationId
	Reply   chan schema.CreateElicitationResponse
	Done    <-chan struct{}
}

func (c *Client) elicit(ctx context.Context, raw json.RawMessage) (any, error) {
	var request schema.CreateElicitationRequest
	if err := json.Unmarshal(raw, &request); err != nil {
		return nil, &acp.RPCError{Code: -32602, Message: err.Error()}
	}
	e := Elicitation{Agent: c.Agent, Request: request, Reply: make(chan schema.CreateElicitationResponse, 1)}
	var session *schema.ElicitationSessionScope
	switch {
	case request.Form != nil:
		session = request.Form.Session
		e.Schema = &request.Form.RequestedSchema
	case request.URL != nil:
		session = request.URL.Session
		e.URL, e.ID = request.URL.URL, request.URL.ElicitationID
		u, err := url.Parse(e.URL)
		if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" || u.User != nil || e.ID == "" {
			return nil, &acp.RPCError{Code: -32602, Message: "invalid elicitation URL or ID"}
		}
	default:
		return nil, &acp.RPCError{Code: -32602, Message: "unsupported elicitation mode"}
	}
	c.mu.Lock()
	active := c.wire.SessionID
	turn := c.permissionCtx
	c.mu.Unlock()
	if session != nil && (active == "" || session.SessionID != active) {
		return nil, &acp.RPCError{Code: -32602, Message: "unknown elicitation session"}
	}
	requestCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	if session != nil && turn != nil {
		stop := context.AfterFunc(turn, cancel)
		defer stop()
	}
	if e.ID != "" {
		c.mu.Lock()
		if c.urlElicitations[e.ID] {
			c.mu.Unlock()
			return nil, &acp.RPCError{Code: -32602, Message: "duplicate elicitation ID"}
		}
		c.urlElicitations[e.ID] = true
		c.mu.Unlock()
	}
	accepted := false
	defer func() {
		if !accepted && e.ID != "" {
			c.mu.Lock()
			delete(c.urlElicitations, e.ID)
			c.mu.Unlock()
		}
	}()
	e.Done = requestCtx.Done()
	select {
	case c.Elicitations <- e:
	case <-requestCtx.Done():
		return acp.CancelElicitation(), nil
	}
	select {
	case response := <-e.Reply:
		accepted = response.Accept != nil
		return response, nil
	case <-requestCtx.Done():
		return acp.CancelElicitation(), nil
	}
}
func (c *Client) elicitationComplete(raw json.RawMessage) error {
	var notice schema.CompleteElicitationNotification
	if err := json.Unmarshal(raw, &notice); err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.urlElicitations[notice.ElicitationID] {
		return nil
	}
	delete(c.urlElicitations, notice.ElicitationID)
	c.current.Messages = append(c.current.Messages, store.Message{Role: "notice", Text: "External interaction completed: " + string(notice.ElicitationID)})
	c.revision++
	return nil
}
