package client

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"

	"github.com/BrokkAi/acp-go/schema"
	"github.com/BrokkAi/micro-acp/internal/config"
)

// Auth kinds surfaced by AuthChoices.
const (
	AuthKindAgent    = "agent"
	AuthKindTerminal = "terminal"
	AuthKindEnv      = "env_var"
)

type AuthChoice struct {
	ID, Name, Description string
	// Kind is one of agent, terminal or env_var.
	Kind string
	// Terminal reports a terminal login method. It stays in sync with
	// Kind == AuthKindTerminal for existing callers.
	Terminal bool
	// Vars lists the environment variables an env_var method wants the
	// client to provide when starting the agent process.
	Vars []AuthEnvVar
	// Link points at the page where env_var credentials can be obtained.
	Link string
}

// AuthEnvVar describes a single environment variable requested by an
// env_var authentication method.
type AuthEnvVar struct {
	Name     string
	Label    string
	Secret   bool
	Optional bool
}

// EnvAuthMethod is an env_var authentication method: the user provides
// credentials that the client passes to the agent as environment variables.
type EnvAuthMethod struct {
	ID          string
	Name        string
	Description string
	Vars        []AuthEnvVar
	Link        string
}

func (c *Client) AuthChoices() []AuthChoice {
	var choices []AuthChoice
	for _, method := range c.Init.AuthMethods {
		if m := method.Agent; m != nil {
			choices = append(choices, AuthChoice{ID: string(m.ID), Name: m.Name, Description: ptrText(m.Description), Kind: AuthKindAgent})
		}
		if m := method.Terminal; m != nil {
			choices = append(choices, AuthChoice{ID: string(m.ID), Name: m.Name, Description: ptrText(m.Description), Kind: AuthKindTerminal, Terminal: true})
		}
	}
	for _, m := range c.envAuth {
		choices = append(choices, AuthChoice{ID: m.ID, Name: m.Name, Description: m.Description, Kind: AuthKindEnv, Vars: m.Vars, Link: m.Link})
	}
	return choices
}

// EnvAuthMethods returns the env_var authentication methods the agent
// advertised. Clients satisfy them by starting the agent with the requested
// variables set, then calling Authenticate with the method id.
func (c *Client) EnvAuthMethods() []EnvAuthMethod {
	return append([]EnvAuthMethod(nil), c.envAuth...)
}

func (c *Client) AuthCommand(id string) (*exec.Cmd, error) {
	for _, method := range c.Init.AuthMethods {
		if m := method.Terminal; m != nil && m.ID == schema.AuthMethodId(id) {
			cmd := exec.CommandContext(c.ctx, c.command.Command, append(append([]string(nil), c.command.Args...), m.Args...)...)
			cmd.Dir = c.Cwd
			cmd.Env = os.Environ()
			for k, v := range c.command.Env {
				cmd.Env = append(cmd.Env, k+"="+v)
			}
			for k, v := range m.Env {
				cmd.Env = append(cmd.Env, k+"="+v)
			}
			return cmd, nil
		}
	}
	return nil, fmt.Errorf("agent did not advertise terminal authentication %q", id)
}

// splitAuthMethods separates the authMethods of a raw v1 initialize response
// into the entries this acp-go release decodes (agent and terminal) and the
// env_var entries it rejects with "unknown type tag". Env_var methods need no
// capability opt-in: any client can set environment variables when starting
// the agent process. Entries with any other unknown type are dropped like the
// v2 mapping drops unrecognized methods, so one future method cannot fail the
// whole handshake.
func splitAuthMethods(raw json.RawMessage) (filtered json.RawMessage, env []EnvAuthMethod, err error) {
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(raw, &doc); err != nil {
		return raw, nil, nil
	}
	rawMethods, ok := doc["authMethods"]
	if !ok {
		return raw, nil, nil
	}
	var methods []json.RawMessage
	if err := json.Unmarshal(rawMethods, &methods); err != nil {
		return raw, nil, nil
	}
	kept := make([]json.RawMessage, 0, len(methods))
	for _, m := range methods {
		var probe struct {
			Type *string `json:"type"`
		}
		if err := json.Unmarshal(m, &probe); err != nil {
			return raw, nil, nil
		}
		switch {
		case probe.Type == nil || *probe.Type == "" || *probe.Type == AuthKindTerminal:
			kept = append(kept, m)
		case *probe.Type == AuthKindEnv:
			parsed, err := parseEnvAuthMethod(m)
			if err != nil {
				return nil, nil, err
			}
			env = append(env, parsed)
		default:
			// Unknown future method type: skip it rather than fail initialize.
		}
	}
	if len(kept) == len(methods) && env == nil {
		return raw, nil, nil
	}
	if kept == nil {
		kept = []json.RawMessage{}
	}
	encoded, err := json.Marshal(kept)
	if err != nil {
		return raw, nil, nil
	}
	doc["authMethods"] = encoded
	out, err := json.Marshal(doc)
	if err != nil {
		return raw, nil, nil
	}
	return out, env, nil
}

// parseEnvAuthMethod decodes one env_var authentication method. Secret
// defaults to true and optional to false, matching the ACP spec.
func parseEnvAuthMethod(raw json.RawMessage) (EnvAuthMethod, error) {
	var wire struct {
		ID          string  `json:"id"`
		MethodID    string  `json:"methodId"`
		Name        string  `json:"name"`
		Description *string `json:"description"`
		Vars        []struct {
			Name     string  `json:"name"`
			Label    *string `json:"label"`
			Secret   *bool   `json:"secret"`
			Optional *bool   `json:"optional"`
		} `json:"vars"`
		Link *string `json:"link"`
	}
	if err := json.Unmarshal(raw, &wire); err != nil {
		return EnvAuthMethod{}, fmt.Errorf("env_var auth method: %w", err)
	}
	// v1 identifies methods with id, v2 with methodId.
	id := wire.ID
	if id == "" {
		id = wire.MethodID
	}
	if id == "" || wire.Name == "" {
		return EnvAuthMethod{}, fmt.Errorf("env_var auth method requires id and name")
	}
	if len(wire.Vars) == 0 {
		return EnvAuthMethod{}, fmt.Errorf("env_var auth method %q requires vars", id)
	}
	method := EnvAuthMethod{ID: id, Name: wire.Name}
	if wire.Description != nil {
		method.Description = *wire.Description
	}
	if wire.Link != nil {
		method.Link = *wire.Link
	}
	for _, v := range wire.Vars {
		if v.Name == "" {
			return EnvAuthMethod{}, fmt.Errorf("env_var auth method %q has a var without a name", id)
		}
		entry := AuthEnvVar{Name: v.Name, Secret: true}
		if v.Label != nil {
			entry.Label = *v.Label
		}
		if v.Secret != nil {
			entry.Secret = *v.Secret
		}
		if v.Optional != nil {
			entry.Optional = *v.Optional
		}
		method.Vars = append(method.Vars, entry)
	}
	return method, nil
}

func (c *Client) Done() <-chan struct{} { return c.conn.Done() }

func (c *Client) Invocation() config.Command { return c.command }
