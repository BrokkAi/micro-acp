package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"

	"github.com/BrokkAi/acp-go/schema"
	"github.com/BrokkAi/micro-acp/internal/client"
	"github.com/BrokkAi/micro-acp/internal/config"
	"github.com/BrokkAi/micro-acp/internal/store"
)

type loginOptions struct {
	Config  config.Config
	Paths   config.Paths
	Store   store.Store
	Agent   string
	Cwd     string
	Offline bool
	Demo    bool
	Method  string
}

// authStdio is the terminal handed to an agent's login process.
var authStdio = struct {
	In  *os.File
	Out *os.File
	Err *os.File
}{os.Stdin, os.Stdout, os.Stderr}

// runLogin authenticates an agent without the TUI. With no --method it lists
// the agent's login methods so scripts can discover them.
func runLogin(ctx context.Context, opts loginOptions, args []string, out, errOut io.Writer) error {
	custom, rest, err := customCommand(args)
	if err != nil {
		return withCode(exitUsage, err)
	}
	if len(rest) > 0 {
		return withCode(exitUsage, errors.New("usage: micro-acp [flags] login [--method <id>]"))
	}
	if custom != nil && (opts.Agent != "" || opts.Demo) {
		return withCode(exitUsage, errors.New("choose either --agent, --demo, or a custom command after --"))
	}
	if opts.Demo && opts.Agent != "" {
		return withCode(exitUsage, errors.New("choose either --agent or --demo"))
	}

	cwd := opts.Cwd
	if cwd == "" {
		cwd, _ = os.Getwd()
	}
	cwd, err = filepath.Abs(cwd)
	if err != nil {
		return withCode(exitUsage, err)
	}
	info, err := os.Stat(cwd)
	if err != nil {
		return withCode(exitUsage, err)
	}
	if !info.IsDir() {
		return withCode(exitUsage, errors.New("workspace must be a directory"))
	}

	agentID := opts.Agent
	var command config.Command
	switch {
	case custom != nil:
		command = *custom
		agentID = customAgentID(command)
	case opts.Demo:
		self, err := os.Executable()
		if err != nil {
			return withCode(exitUsage, err)
		}
		if agentID == "" {
			agentID = "demo"
		}
		command = config.Command{Command: self, Args: []string{"__demo-agent"}}
	default:
		if agentID == "" {
			return withCode(exitUsage, errors.New("provide --agent <id>, a command after --, or --demo"))
		}
		resolved, err := resolveAgentCommand(ctx, opts.Config, opts.Paths, agentID, opts.Offline, errOut)
		if err != nil {
			return err
		}
		command = resolved
	}

	// A headless login cannot answer form or URL requests, so it advertises no
	// elicitation support and declines anything an agent sends anyway.
	c, err := client.OpenInteractive(ctx, agentID, cwd, command, opts.Store,
		client.Interactions{DisableElicitation: true}, opts.Config.Session)
	if err != nil {
		return clientError(nil, err)
	}
	defer c.Close()
	done := make(chan struct{})
	defer close(done)
	go serveInteractions(ctx, c, done, func(schema.RequestPermissionRequest) schema.RequestPermissionOutcome {
		return cancelledPermission()
	})

	choices := c.AuthChoices()
	if opts.Method == "" {
		if len(choices) == 0 {
			return withCode(exitAuth, errors.New("agent advertises no login methods; configure its credentials and retry"))
		}
		w := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
		fmt.Fprintln(w, "METHOD\tNAME\tKIND\tDESCRIPTION")
		for _, choice := range choices {
			kind := "agent"
			if choice.Terminal {
				kind = "terminal"
			}
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", choice.ID, choice.Name, kind, choice.Description)
		}
		return w.Flush()
	}

	index := -1
	for i, choice := range choices {
		if choice.ID == opts.Method {
			index = i
			break
		}
	}
	if index < 0 {
		return withCode(exitUsage, fmt.Errorf("unknown login method %q%s", opts.Method, methodHint(choices)))
	}
	choice := choices[index]

	if !choice.Terminal {
		if err := c.Authenticate(choice.ID); err != nil {
			return clientError(c, fmt.Errorf("login %s: %w", choice.ID, err))
		}
		return nil
	}

	loginCommand, err := c.AuthCommand(choice.ID)
	if err != nil {
		return withCode(exitUsage, err)
	}
	loginCommand.Stdin, loginCommand.Stdout, loginCommand.Stderr = authStdio.In, authStdio.Out, authStdio.Err
	if err := loginCommand.Run(); err != nil {
		return fmt.Errorf("login %s: %w", choice.ID, err)
	}
	return nil
}

func methodHint(choices []client.AuthChoice) string {
	if len(choices) == 0 {
		return " (the agent advertises no login methods)"
	}
	ids := make([]string, 0, len(choices))
	for _, choice := range choices {
		ids = append(ids, choice.ID)
	}
	return " (available methods: " + strings.Join(ids, ", ") + ")"
}
