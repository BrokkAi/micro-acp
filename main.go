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

func run(ctx context.Context, args []string, out, errOut io.Writer) error {
	paths, err := config.DefaultPaths()
	if err != nil {
		return err
	}
	if len(args) > 0 && args[0] == "__demo-agent" {
		return demo.Run(ctx, filepath.Join(paths.Data, "demo"))
	}
	fs := flag.NewFlagSet("micro-acp", flag.ContinueOnError)
	fs.SetOutput(errOut)
	agent := fs.String("agent", "", "registry ID or custom agent name")
	cwd := fs.String("cwd", "", "workspace directory (defaults to current directory)")
	sessionID := fs.String("session", "", "resume a saved local session ID")
	configFile := fs.String("config", "", "configuration file")
	offline := fs.Bool("offline", false, "use the cached registry without a network request")
	demoMode := fs.Bool("demo", false, "try the TUI with a local demo agent; no credentials required")
	execFormat := fs.String("format", "text", "exec output format: text, json, or stream-json")
	execPermission := fs.String("permission", "deny", "exec reply to permission requests: allow, deny, or fail")
	execTimeout := fs.Duration("timeout", 0, "exec deadline such as 90s; 0 means no limit")
	execSave := fs.Bool("save", false, "exec: persist the session locally instead of running ephemeral")
	showVersion := fs.Bool("version", false, "print version")
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
  micro-acp [flags] agents                List the latest registry and custom agents
  micro-acp sessions                      List saved sessions
  micro-acp sessions forget <id>          Remove a local saved session
  micro-acp config                        Print paths and an example configuration

Flags must precede subcommands. The format, permission, timeout, and save flags
apply to exec. Session deletion and forks are available in the TUI.

`)
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if *showVersion {
		fmt.Fprintln(out, buildinfo.Version)
		return nil
	}
	explicitConfig := *configFile != ""
	if explicitConfig {
		paths.Config = *configFile
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
	// For the top-level custom-command form (`micro-acp -- agent`) flag parsing
	// consumes the "--", so it is absent from remaining. A "--" still present in
	// remaining belongs to exec's inline command form (`micro-acp exec -- agent`).
	inlineCommand := false
	for _, arg := range remaining {
		if arg == "--" {
			inlineCommand = true
			break
		}
	}
	if !(custom && !inlineCommand) && len(remaining) > 0 && remaining[0] == "exec" {
		return runExec(ctx, execOptions{
			Config:     cfg,
			Paths:      paths,
			Store:      st,
			Agent:      *agent,
			Cwd:        *cwd,
			Offline:    *offline,
			Demo:       *demoMode,
			Session:    *sessionID,
			Format:     *execFormat,
			Permission: *execPermission,
			Timeout:    *execTimeout,
			Save:       *execSave,
		}, remaining[1:], out, errOut)
	}
	if !custom && len(remaining) > 0 {
		switch remaining[0] {
		case "agents":
			if len(remaining) != 1 {
				return errors.New("usage: micro-acp [flags] agents")
			}
			r := registry.Client{URL: cfg.RegistryURL, Cache: paths.Cache}
			snapshot, err := r.Load(ctx, *offline)
			if err != nil {
				snapshot.Warning = err.Error()
			}
			if snapshot.Warning != "" {
				fmt.Fprintln(errOut, snapshot.Warning)
			}
			w := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
			fmt.Fprintln(w, "ID\tNAME\tVERSION\tLAUNCH")
			names := make([]string, 0, len(cfg.Agents))
			for name := range cfg.Agents {
				names = append(names, name)
			}
			sort.Strings(names)
			for _, name := range names {
				fmt.Fprintf(w, "%s\t%s\tcustom\t%s\n", name, name, cfg.Agents[name].Command)
			}
			var agents []registry.Agent
			for _, a := range registry.WithBuiltins(snapshot.Index.Agents) {
				if _, ok := cfg.Agents[a.ID]; !ok {
					agents = append(agents, a)
				}
			}
			for _, a := range r.ResolveVersions(ctx, agents, *offline) {
				version := a.Version
				if a.VersionError != "" {
					version = "unavailable"
					fmt.Fprintf(errOut, "%s: %s\n", a.Name, a.VersionError)
				}
				fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", a.ID, a.Name, version, a.Kind())
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
	options := tui.Options{Config: cfg, Paths: paths, Agent: *agent, Offline: *offline}
	if custom {
		if len(remaining) == 0 {
			return errors.New("provide an agent command after --")
		}
		if *agent != "" || *demoMode {
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
	if *demoMode {
		if *agent != "" {
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
	if *sessionID != "" {
		s, err := st.Load(*sessionID)
		if err != nil {
			return err
		}
		options.Resume = &s
		if options.Agent == "" {
			options.Agent = s.Agent
		} else if options.Agent != s.Agent {
			return errors.New("selected agent does not match the saved session")
		}
		if *cwd == "" {
			*cwd = s.Cwd
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
	if *cwd == "" {
		*cwd, err = os.Getwd()
		if err != nil {
			return err
		}
	}
	options.Cwd, err = filepath.Abs(*cwd)
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
