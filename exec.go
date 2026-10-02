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
	"sync"
	"sync/atomic"
	"time"

	acp "github.com/BrokkAi/acp-go"
	"github.com/BrokkAi/acp-go/schema"
	"github.com/BrokkAi/micro-acp/internal/client"
	"github.com/BrokkAi/micro-acp/internal/config"
	"github.com/BrokkAi/micro-acp/internal/registry"
	"github.com/BrokkAi/micro-acp/internal/store"
	"golang.org/x/term"
)

// Scripted exit codes. Zero means the agent finished normally.
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

// stdin is a variable so tests can supply a prompt without a terminal.
var stdin io.Reader = os.Stdin

type execOptions struct {
	Config     config.Config
	Paths      config.Paths
	Store      store.Store
	Agent      string
	Cwd        string
	Offline    bool
	Demo       bool
	Session    string
	Format     string
	Permission string
	Timeout    time.Duration
	Save       bool
}

type execResult struct {
	Agent      string              `json:"agent"`
	SessionID  string              `json:"session_id"`
	StopReason string              `json:"stop_reason"`
	Text       string              `json:"text"`
	Usage      *schema.UsageUpdate `json:"usage,omitempty"`
}

// runExec drives one prompt headlessly. It never opens the TUI and does not
// require a terminal, so scripts and harnesses can call it directly.
func runExec(ctx context.Context, opts execOptions, args []string, out, errOut io.Writer) error {
	switch opts.Format {
	case "text", "json", "stream-json":
	default:
		return withCode(exitUsage, fmt.Errorf("unknown --format %q (want text, json, or stream-json)", opts.Format))
	}
	switch opts.Permission {
	case "allow", "deny", "fail":
	default:
		return withCode(exitUsage, fmt.Errorf("unknown --permission %q (want allow, deny, or fail)", opts.Permission))
	}

	var custom *config.Command
	promptArgs := args
	if len(args) > 0 && args[0] == "--" {
		if len(args) == 1 {
			return withCode(exitUsage, errors.New("provide an agent command after --"))
		}
		if opts.Agent != "" || opts.Demo {
			return withCode(exitUsage, errors.New("choose either --agent, --demo, or a custom command after --"))
		}
		command := config.Command{Command: args[1], Args: args[2:]}
		if filepath.IsAbs(command.Command) || strings.ContainsRune(command.Command, os.PathSeparator) {
			abs, err := filepath.Abs(command.Command)
			if err != nil {
				return withCode(exitUsage, err)
			}
			command.Command = abs
		}
		custom = &command
		// An inline command has no unambiguous place for a positional prompt.
		promptArgs = nil
	}

	var resumed *store.Session
	if opts.Session != "" {
		s, err := opts.Store.Load(opts.Session)
		if err != nil {
			return withCode(exitUsage, err)
		}
		resumed = &s
	}

	cwd := opts.Cwd
	if cwd == "" && resumed != nil {
		cwd = resumed.Cwd
	}
	if cwd == "" {
		cwd, _ = os.Getwd()
	}
	cwd, err := filepath.Abs(cwd)
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
	if agentID == "" && resumed != nil {
		agentID = resumed.Agent
	}

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
		opts.Offline = true
	default:
		if agentID == "" {
			return withCode(exitUsage, errors.New("provide --agent <id>, a command after --, or --demo"))
		}
		resolved, err := resolveExecAgent(ctx, opts, agentID, errOut)
		if err != nil {
			return err
		}
		command = resolved
	}

	if resumed != nil && resumed.Agent != agentID {
		return withCode(exitUsage, errors.New("selected agent does not match the saved session"))
	}

	text, err := execPrompt(promptArgs)
	if err != nil {
		return withCode(exitUsage, err)
	}

	if opts.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, opts.Timeout)
		defer cancel()
	}

	// Persist only when asked, or when updating a session the caller already
	// saved. Everything else runs ephemeral and leaves no local trace.
	ephemeral := !opts.Save && opts.Session == ""

	c, err := client.OpenInteractive(ctx, agentID, cwd, command, opts.Store,
		client.Interactions{DisableElicitation: true}, opts.Config.Session)
	if err != nil {
		return execClientError(nil, err)
	}
	defer c.Close()
	c.Ephemeral = ephemeral

	if resumed != nil {
		err = c.Load(*resumed)
	} else {
		err = c.New()
	}
	if err != nil {
		return execClientError(c, err)
	}

	// Resuming replays saved history into the snapshot. Only messages produced
	// by this turn belong in the output.
	start, _ := c.Snapshot()
	base := len(start.Messages)

	var denied atomic.Bool
	done := make(chan struct{})
	defer close(done)
	go answerExecPermissions(ctx, c, opts.Permission, &denied, done)
	go cancelExecElicitations(ctx, c, done)

	var stream *execStreamer
	stopStream := make(chan struct{})
	if opts.Format == "stream-json" {
		stream = newExecStreamer(out, base)
		go func() {
			ticker := time.NewTicker(80 * time.Millisecond)
			defer ticker.Stop()
			for {
				select {
				case <-ticker.C:
					stream.poll(c)
				case <-stopStream:
					return
				}
			}
		}()
	}

	reason, promptErr := c.Prompt(text)
	if stream != nil {
		close(stopStream)
		stream.poll(c)
	}

	snapshot, _ := c.Snapshot()
	result := execResult{
		Agent:      agentID,
		SessionID:  snapshot.ID,
		StopReason: string(reason),
		Text:       assistantText(snapshot.Messages[base:]),
		Usage:      snapshot.Usage,
	}

	if promptErr != nil {
		switch {
		case errors.Is(promptErr, context.DeadlineExceeded) || ctx.Err() == context.DeadlineExceeded:
			return withCode(exitTimeout, promptErr)
		case acp.IsAuthRequired(promptErr):
			return withCode(exitAuth, fmt.Errorf("%w%s", promptErr, authHint(c)))
		case denied.Load():
			return withCode(exitPermission, fmt.Errorf("permission request denied: %w", promptErr))
		default:
			return promptErr
		}
	}
	if denied.Load() {
		return withCode(exitPermission, errors.New("permission request denied by --permission fail"))
	}

	switch opts.Format {
	case "stream-json":
		stream.emit(map[string]any{
			"type":        "result",
			"session_id":  result.SessionID,
			"stop_reason": result.StopReason,
			"text":        result.Text,
		})
	case "json":
		encoded, err := json.Marshal(result)
		if err != nil {
			return err
		}
		fmt.Fprintln(out, string(encoded))
	default:
		if result.Text != "" {
			fmt.Fprintln(out, result.Text)
		}
	}

	if reason != schema.StopReasonEndTurn {
		return withCode(exitStop, fmt.Errorf("agent stopped with %q", reason))
	}
	return nil
}

// resolveExecAgent turns an agent id into a launch command, preferring custom
// configuration over the registry and its built-ins.
func resolveExecAgent(ctx context.Context, opts execOptions, id string, errOut io.Writer) (config.Command, error) {
	if command, ok := opts.Config.Agents[id]; ok {
		return command, nil
	}
	r := registry.Client{URL: opts.Config.RegistryURL, Cache: opts.Paths.Cache}
	snapshot, loadErr := r.Load(ctx, opts.Offline)
	if snapshot.Warning != "" {
		fmt.Fprintln(errOut, snapshot.Warning)
	}
	for _, a := range registry.WithBuiltins(snapshot.Index.Agents) {
		if a.ID != id {
			continue
		}
		command, err := r.Resolve(ctx, a, opts.Offline)
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

func execClientError(c *client.Client, err error) error {
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
	return " (authentication required; login methods: " + strings.Join(ids, ", ") + ")"
}

func execPrompt(args []string) (string, error) {
	if len(args) == 0 || (len(args) == 1 && args[0] == "-") {
		if len(args) == 0 && term.IsTerminal(int(os.Stdin.Fd())) {
			return "", errors.New("provide a prompt, or pipe one on stdin (or pass '-')")
		}
		raw, err := io.ReadAll(stdin)
		if err != nil {
			return "", err
		}
		text := strings.TrimSpace(string(raw))
		if text == "" {
			return "", errors.New("prompt on stdin was empty")
		}
		return text, nil
	}
	return strings.Join(args, " "), nil
}

func answerExecPermissions(ctx context.Context, c *client.Client, policy string, denied *atomic.Bool, done <-chan struct{}) {
	for {
		select {
		case p := <-c.Permissions:
			outcome := answerPermission(p.Request, policy)
			if policy == "fail" {
				denied.Store(true)
				_ = c.Cancel()
			}
			select {
			case p.Reply <- outcome:
			default:
			}
		case <-ctx.Done():
			return
		case <-done:
			return
		}
	}
}

// cancelExecElicitations answers any elicitation that arrives even though the
// client advertised no support, so a confused agent cannot stall the run.
func cancelExecElicitations(ctx context.Context, c *client.Client, done <-chan struct{}) {
	for {
		select {
		case e := <-c.Elicitations:
			select {
			case e.Reply <- acp.CancelElicitation():
			default:
			}
		case <-ctx.Done():
			return
		case <-done:
			return
		}
	}
}

func answerPermission(request schema.RequestPermissionRequest, policy string) schema.RequestPermissionOutcome {
	cancelled := schema.RequestPermissionOutcome{Cancelled: &schema.RequestPermissionOutcomeCancelled{}}
	selectOption := func(kinds ...schema.PermissionOptionKind) schema.RequestPermissionOutcome {
		for _, kind := range kinds {
			for _, option := range request.Options {
				if option.Kind == kind {
					return schema.RequestPermissionOutcome{Selected: &schema.SelectedPermissionOutcome{OptionID: option.OptionID}}
				}
			}
		}
		return cancelled
	}
	switch policy {
	case "allow":
		return selectOption(schema.PermissionOptionKindAllowOnce, schema.PermissionOptionKindAllowAlways)
	case "deny":
		return selectOption(schema.PermissionOptionKindRejectOnce, schema.PermissionOptionKindRejectAlways)
	default:
		return cancelled
	}
}

func assistantText(messages []store.Message) string {
	var parts []string
	for _, message := range messages {
		if message.Role == "assistant" && strings.TrimSpace(message.Text) != "" {
			parts = append(parts, message.Text)
		}
	}
	return strings.Join(parts, "\n")
}

func customAgentID(command config.Command) string {
	encoded, _ := json.Marshal(command)
	hash := sha256.Sum256(encoded)
	return "custom-" + hex.EncodeToString(hash[:4])
}

// execStreamer turns the client's accumulating session snapshot into
// newline-delimited JSON, emitting only the deltas since the last poll.
type execStreamer struct {
	out  io.Writer
	mu   sync.Mutex
	base int
	seen []string
	tool map[string]string
}

func newExecStreamer(out io.Writer, base int) *execStreamer {
	return &execStreamer{out: out, base: base, seen: make([]string, base), tool: map[string]string{}}
}

func (s *execStreamer) emit(value any) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return
	}
	fmt.Fprintln(s.out, string(encoded))
}

func (s *execStreamer) poll(c *client.Client) {
	snapshot, _ := c.Snapshot()
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, message := range snapshot.Messages {
		if i < s.base {
			continue
		}
		for len(s.seen) <= i {
			s.seen = append(s.seen, "")
		}
		switch message.Role {
		case "assistant", "thought":
			previous := s.seen[i]
			if strings.HasPrefix(message.Text, previous) && len(message.Text) > len(previous) {
				s.emit(map[string]any{"type": message.Role, "text": message.Text[len(previous):]})
				s.seen[i] = message.Text
			}
		case "tool":
			key := message.ID
			if key == "" {
				key = fmt.Sprintf("#%d", i)
			}
			if s.tool[key] == message.Text {
				continue
			}
			event := map[string]any{"type": "tool", "id": message.ID}
			if message.Tool != nil {
				event["title"] = message.Tool.Title
				if message.Tool.Kind != nil {
					event["kind"] = string(*message.Tool.Kind)
				}
				if message.Tool.Status != nil {
					event["status"] = string(*message.Tool.Status)
				}
			}
			s.emit(event)
			s.tool[key] = message.Text
		case "notice":
			if s.seen[i] == "" {
				s.emit(map[string]any{"type": "notice", "text": message.Text})
				s.seen[i] = "1"
			}
		}
	}
}
