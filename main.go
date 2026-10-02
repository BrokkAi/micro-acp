package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/BrokkAi/micro-acp/internal/buildinfo"
	"github.com/BrokkAi/micro-acp/internal/config"
	"github.com/BrokkAi/micro-acp/internal/demo"
	"github.com/BrokkAi/micro-acp/internal/registry"
	"github.com/BrokkAi/micro-acp/internal/store"
	"github.com/BrokkAi/micro-acp/internal/tui"
	"golang.org/x/term"
)

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()
	if err := run(ctx, os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "micro-acp:", err)
		var exit *exitError
		if errors.As(err, &exit) {
			os.Exit(exit.code)
		}
		os.Exit(1)
	}
}

// cliFlags holds every flag the CLI accepts. The set is registered on the
// top-level parser and again after a subcommand, so flags may appear on either
// side of it: `micro-acp --offline agents` and `micro-acp agents --offline`.
type cliFlags struct {
	agent      string
	cwd        string
	session    string
	config     string
	offline    bool
	demo       bool
	format     string
	permission string
	timeout    time.Duration
	save       bool
	method     string
	json       bool
	version    bool
}

func (f *cliFlags) register(fs *flag.FlagSet) {
	fs.StringVar(&f.agent, "agent", f.agent, "registry ID or custom agent name")
	fs.StringVar(&f.cwd, "cwd", f.cwd, "workspace directory (defaults to current directory)")
	fs.StringVar(&f.session, "session", f.session, "resume a saved local session ID")
	fs.StringVar(&f.config, "config", f.config, "configuration file")
	fs.BoolVar(&f.offline, "offline", f.offline, "use the cached registry without a network request")
	fs.BoolVar(&f.demo, "demo", f.demo, "try the TUI with a local demo agent; no credentials required")
	fs.StringVar(&f.format, "format", f.format, "exec output format: text, json, or stream-json")
	fs.StringVar(&f.permission, "permission", f.permission, "exec reply to permission requests: allow, deny, or fail")
	fs.DurationVar(&f.timeout, "timeout", f.timeout, "exec deadline such as 90s; 0 means no limit")
	fs.BoolVar(&f.save, "save", f.save, "exec: persist the session locally instead of running ephemeral")
	fs.StringVar(&f.method, "method", f.method, "login method id, from a prior 'micro-acp login' listing")
	fs.BoolVar(&f.json, "json", f.json, "agents: print machine-readable JSON")
	fs.BoolVar(&f.version, "version", f.version, "print version")
}

// subcommands accept flags on either side of their name.
var subcommands = map[string]bool{"exec": true, "login": true, "agents": true, "sessions": true, "config": true}

// agentRow is one row of the agents listing, rendered as text or JSON.
type agentRow struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Version string `json:"version"`
	Kind    string `json:"kind"`
	Launch  string `json:"launch"`
}

func run(ctx context.Context, args []string, out, errOut io.Writer) error {
	paths, err := config.DefaultPaths()
	if err != nil {
		return err
	}
	if len(args) > 0 && args[0] == "__demo-agent" {
		return demo.Run(ctx, filepath.Join(paths.Data, "demo"))
	}
	opts := cliFlags{format: "text", permission: "deny"}
	fs := flag.NewFlagSet("micro-acp", flag.ContinueOnError)
	fs.SetOutput(errOut)
	opts.register(fs)
	fs.Usage = func() {
		fmt.Fprint(errOut, `micro-acp — a small terminal client for ACP agents

Usage:
  micro-acp [flags]                       Open the TUI and choose an agent
  micro-acp --agent <id>                  Connect to a registry or custom agent
  micro-acp -- /path/to/agent --acp        Run a custom stdio command
  micro-acp --session <id>                Resume a saved session
  micro-acp --demo                        Try the local demo
  micro-acp [flags] exec <prompt>         Send one prompt without the TUI
  micro-acp [flags] exec -                Read the prompt from stdin
  micro-acp [flags] login                 List an agent's login methods
  micro-acp [flags] login --method <id>   Run one advertised login method
  micro-acp [flags] agents                List the latest registry and custom agents
  micro-acp sessions                      List saved sessions
  micro-acp sessions forget <id>          Remove a local saved session
  micro-acp config                        Print paths and an example configuration

Flags may precede or follow a subcommand. The format, permission, timeout, and
save flags apply to exec, method applies to login, and json applies to agents.
Run 'micro-acp agents' to list valid agent ids. Session deletion and forks are
available in the TUI.

`)
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if opts.version {
		fmt.Fprintln(out, buildinfo.Version)
		return nil
	}
	explicitConfig := opts.config != ""
	if explicitConfig {
		paths.Config = opts.config
	}
	cfg, err := config.Load(paths.Config, explicitConfig)
	if err != nil {
		return err
	}
	st := store.Store{Directory: paths.Data}
	remaining := fs.Args()
	custom := false
	for _, arg := range args {
		if arg == "--" {
			custom = true
			break
		}
	}
	// Flags may also follow a subcommand. Reparse from its name with the same
	// flag set, which keeps values already given before it unless repeated.
	if !custom && len(remaining) > 0 && subcommands[remaining[0]] {
		name := remaining[0]
		sub := flag.NewFlagSet("micro-acp "+name, flag.ContinueOnError)
		sub.SetOutput(errOut)
		opts.register(sub)
		if err := sub.Parse(remaining[1:]); err != nil {
			if errors.Is(err, flag.ErrHelp) {
				return nil
			}
			return err
		}
		remaining = append([]string{name}, sub.Args()...)
	}
	if rest, ok := positionalSubcommand(remaining, custom, "exec"); ok {
		return runExec(ctx, execOptions{
			Config:     cfg,
			Paths:      paths,
			Store:      st,
			Agent:      opts.agent,
			Cwd:        opts.cwd,
			Offline:    opts.offline,
			Demo:       opts.demo,
			Session:    opts.session,
			Format:     opts.format,
			Permission: opts.permission,
			Timeout:    opts.timeout,
			Save:       opts.save,
		}, rest, out, errOut)
	}
	if rest, ok := positionalSubcommand(remaining, custom, "login"); ok {
		return runLogin(ctx, loginOptions{
			Config:  cfg,
			Paths:   paths,
			Store:   st,
			Agent:   opts.agent,
			Cwd:     opts.cwd,
			Offline: opts.offline,
			Demo:    opts.demo,
			Method:  opts.method,
		}, rest, out, errOut)
	}
	if !custom && len(remaining) > 0 {
		switch remaining[0] {
		case "agents":
			if len(remaining) != 1 {
				return errors.New("usage: micro-acp [flags] agents")
			}
			r := registry.Client{URL: cfg.RegistryURL, Cache: paths.Cache}
			snapshot, err := r.Load(ctx, opts.offline)
			if err != nil {
				snapshot.Warning = err.Error()
			}
			if snapshot.Warning != "" {
				fmt.Fprintln(errOut, snapshot.Warning)
			}
			names := make([]string, 0, len(cfg.Agents))
			for name := range cfg.Agents {
				names = append(names, name)
			}
			sort.Strings(names)
			rows := make([]agentRow, 0, len(cfg.Agents))
			for _, name := range names {
				rows = append(rows, agentRow{ID: name, Name: name, Version: "custom", Kind: "custom", Launch: cfg.Agents[name].Command})
			}
			var agents []registry.Agent
			for _, a := range registry.WithBuiltins(snapshot.Index.Agents) {
				if _, ok := cfg.Agents[a.ID]; !ok {
					agents = append(agents, a)
				}
			}
			for _, a := range r.ResolveVersions(ctx, agents, opts.offline) {
				version := a.Version
				if a.VersionError != "" {
					version = "unavailable"
					fmt.Fprintf(errOut, "%s: %s\n", a.Name, a.VersionError)
				}
				rows = append(rows, agentRow{ID: a.ID, Name: a.Name, Version: version, Kind: a.Kind(), Launch: a.Kind()})
			}
			if opts.json {
				encoded, err := json.Marshal(rows)
				if err != nil {
					return err
				}
				fmt.Fprintln(out, string(encoded))
				return nil
			}
			w := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
			fmt.Fprintln(w, "ID\tNAME\tVERSION\tLAUNCH")
			for _, row := range rows {
				fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", row.ID, row.Name, row.Version, row.Launch)
			}
			return w.Flush()
		case "sessions":
			if len(remaining) == 3 && remaining[1] == "forget" {
				if err := st.Delete(remaining[2]); err != nil {
					return err
				}
				fmt.Fprintln(out, "Local session removed. The agent's copy is unchanged.")
				return nil
			}
			if len(remaining) != 1 {
				return errors.New("usage: micro-acp sessions [forget <id>]")
			}
			sessions, err := st.List()
			if err != nil {
				return err
			}
			w := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
			fmt.Fprintln(w, "ID\tAGENT\tTITLE\tWORKSPACE\tUPDATED")
			for _, s := range sessions {
				fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", s.ID, s.Agent, s.Title, s.Cwd, s.UpdatedAt.Local().Format("Jan 02 15:04"))
			}
			return w.Flush()
		case "config":
			if len(remaining) != 1 {
				return errors.New("usage: micro-acp config")
			}
			fmt.Fprintf(out, "Config: %s\nSessions: %s\nCache: %s\n\nExample config.json:\n", paths.Config, paths.Data, paths.Cache)
			b, _ := json.MarshalIndent(config.Config{DefaultAgent: "my-agent", RegistryURL: config.RegistryURL, Agents: map[string]config.Command{"my-agent": {Command: "/path/to/agent", Args: []string{"--acp"}}}}, "", "  ")
			fmt.Fprintln(out, string(b))
			return nil
		default:
			return fmt.Errorf("unknown command %q (custom agent commands go after --)", remaining[0])
		}
	}
	options := tui.Options{Config: cfg, Paths: paths, Agent: opts.agent, Offline: opts.offline}
	if custom {
		if len(remaining) == 0 {
			return errors.New("provide an agent command after --")
		}
		if opts.agent != "" || opts.demo {
			return errors.New("choose either --agent, --demo, or a custom command")
		}
		command := config.Command{Command: remaining[0], Args: remaining[1:]}
		if command.Command == "" {
			return errors.New("custom agent command cannot be empty")
		}
		if filepath.IsAbs(command.Command) || strings.ContainsRune(command.Command, os.PathSeparator) {
			command.Command, err = filepath.Abs(command.Command)
			if err != nil {
				return err
			}
		}
		b, _ := json.Marshal(command)
		hash := sha256.Sum256(b)
		options.Agent = "custom-" + hex.EncodeToString(hash[:4])
		options.Command = &command
	}
	if opts.demo {
		if opts.agent != "" {
			return errors.New("choose either --agent or --demo")
		}
		self, err := os.Executable()
		if err != nil {
			return err
		}
		options.Agent = "demo"
		options.Command = &config.Command{Command: self, Args: []string{"__demo-agent"}}
		options.Offline = true
	}
	if opts.session != "" {
		s, err := st.Load(opts.session)
		if err != nil {
			return err
		}
		options.Resume = &s
		if options.Agent == "" {
			options.Agent = s.Agent
		} else if options.Agent != s.Agent {
			return errors.New("selected agent does not match the saved session")
		}
		if opts.cwd == "" {
			opts.cwd = s.Cwd
		}
	}
	if options.Agent == "" {
		options.Agent = cfg.DefaultAgent
	}
	if options.Agent == "demo" && options.Command == nil {
		self, err := os.Executable()
		if err != nil {
			return err
		}
		options.Command = &config.Command{Command: self, Args: []string{"__demo-agent"}}
		options.Offline = true
	}
	if opts.cwd == "" {
		opts.cwd, err = os.Getwd()
		if err != nil {
			return err
		}
	}
	options.Cwd, err = filepath.Abs(opts.cwd)
	if err != nil {
		return err
	}
	info, err := os.Stat(options.Cwd)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return errors.New("workspace must be a directory")
	}
	if !term.IsTerminal(int(os.Stdin.Fd())) || !term.IsTerminal(int(os.Stdout.Fd())) {
		return errors.New("the TUI needs a terminal; use agents, sessions, config, or --help for non-interactive output")
	}
	return tui.Run(ctx, options)
}
