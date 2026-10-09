package client

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"
	"time"

	acp "github.com/BrokkAi/acp-go"
	"github.com/BrokkAi/acp-go/clientrouter"
	"github.com/BrokkAi/acp-go/schema"
	"github.com/BrokkAi/acp-go/schema/unstable"
	"github.com/BrokkAi/micro-acp/internal/buildinfo"
)

// agentStartupTimeout bounds process launch plus the initialize handshake.
// Starting an agent often means a first-time package install (npx --yes,
// uvx, or a binary download) on a cold cache, which routinely takes minutes;
// the deadline has to survive that, not just a warm process spawn. A hung
// agent stays interruptible, and a dead one is still noticed through the
// process and connection watchers, so generosity here only costs patience.
const agentStartupTimeout = 10 * time.Minute

// agentProcess is one running agent command. Reads and writes go to its
// stdout and stdin; Close stops it and waits for it to exit.
type agentProcess struct {
	cmd       *exec.Cmd
	out       *os.File
	in        io.WriteCloser
	done      chan struct{}
	closeOnce sync.Once
}

func (p *agentProcess) Read(b []byte) (int, error)  { return p.out.Read(b) }
func (p *agentProcess) Write(b []byte) (int, error) { return p.in.Write(b) }
func (p *agentProcess) Close() error {
	p.closeOnce.Do(func() {
		killProcess(p.cmd)
		_ = p.in.Close()
		_ = p.out.Close()
		<-p.done
	})
	return nil
}

// startAgent runs the agent command directly as argv. It becomes the
// client's current process, which Close stops.
func (c *Client) startAgent() (*agentProcess, error) {
	cmd := exec.CommandContext(c.ctx, c.command.Command, c.command.Args...)
	cmd.Dir = c.Cwd
	cmd.Env = os.Environ()
	for key, value := range c.command.Env {
		cmd.Env = append(cmd.Env, key+"="+value)
	}
	configureProcess(cmd)
	cmd.Stderr = &c.stderr
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, stdoutWriter, err := os.Pipe()
	if err != nil {
		stdin.Close()
		return nil, err
	}
	cmd.Stdout = stdoutWriter
	if err := cmd.Start(); err != nil {
		stdin.Close()
		stdout.Close()
		stdoutWriter.Close()
		return nil, err
	}
	postStart(cmd)
	_ = stdoutWriter.Close()
	p := &agentProcess{cmd: cmd, out: stdout, in: stdin, done: make(chan struct{})}
	go func() { _ = cmd.Wait(); close(p.done) }()
	c.mu.Lock()
	c.process = p
	c.mu.Unlock()
	return p, nil
}

// clientCapabilities is the v1 capability set. Subagent sessions are
// unstable, so the stable capability type omits them.
func clientCapabilities(interactions Interactions) unstable.ClientCapabilities {
	caps := acp.WorkspaceCapabilities(true, true, true)
	caps.Session = acp.ConfigOptionsClientCapabilities(true)
	if interactions.DisableElicitation {
		caps.Elicitation = acp.ElicitationClientCapabilities(false, false)
	} else {
		caps.Elicitation = acp.ElicitationClientCapabilities(true, true)
	}
	terminalAuth := true
	caps.Auth = &schema.AuthCapabilities{Terminal: &terminalAuth}
	var extended unstable.ClientCapabilities
	b, _ := json.Marshal(caps)
	_ = json.Unmarshal(b, &extended)
	extended.Subagents = &unstable.SubagentCapabilities{}
	return extended
}

// initializeV1 keeps the raw handshake to inspect opt-in capabilities, which
// the stable facade omits. Both representations come from acp-go's schemas.
func initializeV1(ctx context.Context, conn *acp.Connection, caps unstable.ClientCapabilities) (json.RawMessage, error) {
	var raw json.RawMessage
	err := conn.Call(ctx, schema.InitializeMethodName, unstable.InitializeRequest{
		ProtocolVersion: unstable.ProtocolVersion(acp.Version), ClientCapabilities: &caps,
		ClientInfo: &unstable.Implementation{Name: "micro-acp", Version: buildinfo.Version},
	}, &raw)
	return raw, err
}

func (c *Client) useV1Initialization(raw json.RawMessage) error {
	filtered, env, err := splitAuthMethods(raw)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(filtered, &c.Init); err != nil {
		return err
	}
	c.envAuth = env
	if c.Init.ProtocolVersion != acp.Version {
		return fmt.Errorf("agent selected unsupported ACP version %d", c.Init.ProtocolVersion)
	}
	var extended unstable.InitializeResponse
	if json.Unmarshal(raw, &extended) == nil && extended.AgentCapabilities != nil && extended.AgentCapabilities.SessionCapabilities != nil {
		c.CanFork = extended.AgentCapabilities.SessionCapabilities.Fork != nil
	}
	var subagents struct {
		AgentCapabilities struct {
			SessionCapabilities struct {
				Subagents json.RawMessage `json:"subagents"`
			} `json:"sessionCapabilities"`
		} `json:"agentCapabilities"`
	}
	if json.Unmarshal(raw, &subagents) == nil {
		c.Subagents = hasJSONValue(subagents.AgentCapabilities.SessionCapabilities.Subagents)
	}
	return nil
}

// routed is the connection the client router settled on.
type routed struct {
	conn *acp.Connection
	v2   bool
	raw  json.RawMessage
}

type v1Route struct {
	c     *Client
	caps  unstable.ClientCapabilities
	ready chan<- routed
}

func (r *v1Route) RequestHandler() acp.Handler            { return r.c.request }
func (r *v1Route) NotificationHandler() acp.Notifications { return r.c.notification }

// Serve initializes v1 and holds the connection open. The router closes
// connections it probes and discards, which ends Serve with an error.
func (r *v1Route) Serve(ctx context.Context, conn *acp.Connection) error {
	setup, stop := context.WithTimeout(ctx, agentStartupTimeout)
	raw, err := initializeV1(setup, conn, r.caps)
	stop()
	if err != nil {
		return err
	}
	r.ready <- routed{conn: conn, raw: raw}
	r.c.holdRoute(ctx, conn)
	return nil
}

// holdRoute keeps a routed connection open until the client stops or the
// agent exits. The router closes the connection only after Serve returns, so
// Serve has to notice a dead agent itself. The agent that answered initialize
// is the current process.
func (c *Client) holdRoute(ctx context.Context, conn *acp.Connection) {
	c.mu.Lock()
	process := c.process
	c.mu.Unlock()
	var exited <-chan struct{}
	if process != nil {
		exited = process.done
	}
	select {
	case <-ctx.Done():
	case <-conn.Done():
	case <-exited:
	}
}

// connectRouted asks for the ACP v2 draft through acp-go's client router. An
// agent that answers v1 continues with v1 under the router's rules; a v2
// rejection is reported, never retried as v1.
func (c *Client) connectRouted(setup context.Context, interactions Interactions) (json.RawMessage, error) {
	state := newV2State(c)
	c.v2 = state
	ready := make(chan routed, 1)
	router := clientrouter.New().
		WithV1(func() clientrouter.V1Client {
			return &v1Route{c: c, caps: clientCapabilities(interactions), ready: ready}
		}).
		WithV2(func() clientrouter.V2Client {
			return &v2Route{c: c, state: state, elicitation: !interactions.DisableElicitation, ready: ready}
		})
	failed := make(chan error, 1)
	c.routed = make(chan struct{})
	go func() {
		defer close(c.routed)
		failed <- router.Connect(c.ctx, func() (io.ReadWriteCloser, error) { return c.startAgent() })
	}()
	select {
	case r := <-ready:
		c.mu.Lock()
		c.conn = r.conn
		c.mu.Unlock()
		if !r.v2 {
			c.v2 = nil
			return r.raw, c.useV1Initialization(r.raw)
		}
		return r.raw, c.useV2Initialization(r.raw)
	case err := <-failed:
		if err == nil {
			err = errors.New("agent closed the connection during initialize")
		}
		return nil, err
	case <-setup.Done():
		return nil, setup.Err()
	}
}
