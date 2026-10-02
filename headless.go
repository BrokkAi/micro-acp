package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	acp "github.com/BrokkAi/acp-go"
	"github.com/BrokkAi/acp-go/schema"
	"github.com/BrokkAi/micro-acp/internal/client"
	"github.com/BrokkAi/micro-acp/internal/config"
	"github.com/BrokkAi/micro-acp/internal/registry"
)

// Scripted exit codes shared by the headless subcommands. Zero means success.
const (
	exitUsage      = 2
	exitAuth       = 3
	exitPermission = 4
	exitTimeout    = 5
	exitStop       = 6
)

// exitError carries a process exit code for scripted callers.
type exitError struct {
	code int
	err  error
}

func (e *exitError) Error() string { return e.err.Error() }
func (e *exitError) Unwrap() error { return e.err }

func withCode(code int, err error) error {
	if err == nil {
		return nil
	}
	return &exitError{code: code, err: err}
}

// positionalSubcommand returns the arguments after name when name is the first
// positional token. A "--" consumed by flag parsing marks the top-level
// custom-command form (`micro-acp -- agent`), while one still present in
// remaining belongs to a subcommand (`micro-acp exec -- agent`).
func positionalSubcommand(remaining []string, custom bool, name string) ([]string, bool) {
	if len(remaining) == 0 || remaining[0] != name {
		return nil, false
	}
	if !custom {
		return remaining[1:], true
	}
	for _, arg := range remaining {
		if arg == "--" {
			return remaining[1:], true
		}
	}
	return nil, false
}

// customCommand parses the inline `-- argv...` agent form, returning the
// command and the remaining positional arguments.
func customCommand(args []string) (*config.Command, []string, error) {
	if len(args) == 0 || args[0] != "--" {
		return nil, args, nil
	}
	if len(args) == 1 {
		return nil, nil, errors.New("provide an agent command after --")
	}
	command := config.Command{Command: args[1], Args: args[2:]}
	if filepath.IsAbs(command.Command) || strings.ContainsRune(command.Command, os.PathSeparator) {
		abs, err := filepath.Abs(command.Command)
		if err != nil {
			return nil, nil, err
		}
		command.Command = abs
	}
	return &command, nil, nil
}

// resolveAgentCommand maps an agent id to a launch command, preferring custom
// configuration over the registry and its built-ins.
func resolveAgentCommand(ctx context.Context, cfg config.Config, paths config.Paths, id string, offline bool, errOut io.Writer) (config.Command, error) {
	if command, ok := cfg.Agents[id]; ok {
		return command, nil
	}
	r := registry.Client{URL: cfg.RegistryURL, Cache: paths.Cache}
	snapshot, loadErr := r.Load(ctx, offline)
	if snapshot.Warning != "" {
		fmt.Fprintln(errOut, snapshot.Warning)
	}
	for _, a := range registry.WithBuiltins(snapshot.Index.Agents) {
		if a.ID != id {
			continue
		}
		command, err := r.Resolve(ctx, a, offline)
		if err != nil {
			return config.Command{}, withCode(exitUsage, fmt.Errorf("resolve agent %s: %w", id, err))
		}
		return command, nil
	}
	if loadErr != nil {
		return config.Command{}, withCode(exitUsage, fmt.Errorf("unknown agent %q (registry unavailable: %v); run 'micro-acp agents' to list valid agent ids", id, loadErr))
	}
	return config.Command{}, withCode(exitUsage, fmt.Errorf("unknown agent %q; run 'micro-acp agents' to list valid agent ids", id))
}

func customAgentID(command config.Command) string {
	encoded, _ := json.Marshal(command)
	hash := sha256.Sum256(encoded)
	return "custom-" + hex.EncodeToString(hash[:4])
}

// clientError maps an agent's authentication-required failure to its exit code.
func clientError(c *client.Client, err error) error {
	if err == nil {
		return nil
	}
	if acp.IsAuthRequired(err) {
		return withCode(exitAuth, fmt.Errorf("%w%s", err, authHint(c)))
	}
	var exit *exitError
	if errors.As(err, &exit) {
		return err
	}
	return err
}

func authHint(c *client.Client) string {
	if c == nil {
		return ""
	}
	var ids []string
	for _, choice := range c.AuthChoices() {
		ids = append(ids, choice.ID)
	}
	if len(ids) == 0 {
		return " (authentication required; the agent offers no login methods, so configure its credentials and retry)"
	}
	return " (authentication required; run 'micro-acp --agent <id> --method <id> login' with one of: " + strings.Join(ids, ", ") + ")"
}

// serveInteractions answers agent requests that a headless run cannot present,
// so an agent cannot stall waiting for input. One goroutine owns both channels:
// elicitations are always declined, and permissions go to the caller's policy.
func serveInteractions(ctx context.Context, c *client.Client, done <-chan struct{}, permission func(schema.RequestPermissionRequest) schema.RequestPermissionOutcome) {
	for {
		select {
		case e := <-c.Elicitations:
			reply(e.Reply, acp.CancelElicitation())
		case p := <-c.Permissions:
			reply(p.Reply, permission(p.Request))
		case <-ctx.Done():
			return
		case <-done:
			return
		}
	}
}

func reply[T any](ch chan T, value T) {
	select {
	case ch <- value:
	default:
	}
}

func cancelledPermission() schema.RequestPermissionOutcome {
	return schema.RequestPermissionOutcome{Cancelled: &schema.RequestPermissionOutcomeCancelled{}}
}
