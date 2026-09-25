package client

import (
	"fmt"
	"os"
	"os/exec"

	"github.com/BrokkAi/acp-go/schema"
	"github.com/BrokkAi/micro-acp/internal/config"
)

type AuthChoice struct {
	ID, Name, Description string
	Terminal              bool
}

func (c *Client) AuthChoices() []AuthChoice {
	var choices []AuthChoice
	for _, method := range c.Init.AuthMethods {
		if m := method.Agent; m != nil {
			choices = append(choices, AuthChoice{string(m.ID), m.Name, ptrText(m.Description), false})
		}
		if m := method.Terminal; m != nil {
			choices = append(choices, AuthChoice{string(m.ID), m.Name, ptrText(m.Description), true})
		}
	}
	return choices
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
func (c *Client) Done() <-chan struct{} { return c.conn.Done() }

func (c *Client) Invocation() config.Command { return c.command }
