package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/BrokkAi/acp-go/schema"
	"github.com/BrokkAi/micro-acp/internal/store"
)

func TestExecRejectsInvalidFlags(t *testing.T) {
	t.Setenv("MICRO_ACP_HOME", t.TempDir())
	for _, args := range [][]string{
		{"--demo", "--format", "bogus", "exec", "hi"},
		{"--demo", "--permission", "whatever", "exec", "hi"},
		{"--demo", "--agent", "anvil", "exec", "hi"},
	} {
		var out bytes.Buffer
		err := run(context.Background(), args, &out, &out)
		var exit *exitError
		if !errors.As(err, &exit) || exit.code != exitUsage {
			t.Fatalf("%v: want usage exit, got %v", args, err)
		}
	}
}

func TestExecDoesNotCaptureTopLevelCustomCommand(t *testing.T) {
	t.Setenv("MICRO_ACP_HOME", t.TempDir())
	var out bytes.Buffer
	// `micro-acp -- exec` launches a custom agent binary named exec, so it must
	// reach the TUI path (and its terminal check) rather than the subcommand.
	err := run(context.Background(), []string{"--", "exec"}, &out, &out)
	if err == nil || !strings.Contains(err.Error(), "needs a terminal") {
		t.Fatalf("-- exec should keep the custom-command meaning, got %v", err)
	}
}

func TestExecUnknownAgentPointsAtAgentsCommand(t *testing.T) {
	t.Setenv("MICRO_ACP_HOME", t.TempDir())
	var out bytes.Buffer
	err := run(context.Background(), []string{"--offline", "--agent", "nope", "exec", "hi"}, &out, &out)
	if err == nil || !strings.Contains(err.Error(), "micro-acp agents") {
		t.Fatalf("unknown agent error lacks a discovery hint: %v", err)
	}
	var exit *exitError
	if !errors.As(err, &exit) || exit.code != exitUsage {
		t.Fatalf("want usage exit, got %v", err)
	}
}

func TestExecPromptJoinsArgsAndReadsStdin(t *testing.T) {
	if got, err := execPrompt([]string{"one", "two"}); err != nil || got != "one two" {
		t.Fatalf("joined prompt = %q, %v", got, err)
	}
	original := stdin
	t.Cleanup(func() { stdin = original })
	stdin = strings.NewReader("  piped prompt\n")
	got, err := execPrompt(nil)
	if err != nil || got != "piped prompt" {
		t.Fatalf("stdin prompt = %q, %v", got, err)
	}
	stdin = strings.NewReader("also piped\n")
	if got, err := execPrompt([]string{"-"}); err != nil || got != "also piped" {
		t.Fatalf("dash prompt = %q, %v", got, err)
	}
}

func TestExecEphemeralByDefaultAndSavePersists(t *testing.T) {
	if testing.Short() {
		t.Skip("builds a helper agent")
	}
	root := t.TempDir()
	t.Setenv("MICRO_ACP_HOME", root)
	agentPath := buildFakeAgent(t, root)
	configPath := filepath.Join(root, "config.json")
	contents := `{"agents":{"fake":{"command":` + strconv.Quote(agentPath) + `}}}`
	if err := os.WriteFile(configPath, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	base := []string{"--config", configPath, "--agent", "fake", "--cwd", root}
	args := func(extra ...string) []string {
		return append(append([]string{}, base...), extra...)
	}
	st := store.Store{Directory: filepath.Join(root, "data")}

	var out bytes.Buffer
	if err := run(context.Background(), args("exec", "hi"), &out, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "fake reply") {
		t.Fatalf("exec did not print the reply: %s", out.String())
	}
	if sessions, err := st.List(); err != nil {
		t.Fatal(err)
	} else if len(sessions) != 0 {
		t.Fatalf("default run persisted %d sessions, want none", len(sessions))
	}

	out.Reset()
	if err := run(context.Background(), args("--save", "exec", "hi"), &out, &out); err != nil {
		t.Fatal(err)
	}
	sessions, err := st.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 1 {
		t.Fatalf("--save persisted %d sessions, want 1", len(sessions))
	}
	before := len(sessions[0].Messages)

	// Resuming an existing session keeps updating it.
	out.Reset()
	if err := run(context.Background(), args("--session", sessions[0].ID, "exec", "again"), &out, &out); err != nil {
		t.Fatal(err)
	}
	resumed, err := st.Load(sessions[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(resumed.Messages) <= before {
		t.Fatalf("resume stored %d messages, want more than %d", len(resumed.Messages), before)
	}
}

func TestExecRejectsEmptyPrompt(t *testing.T) {
	t.Setenv("MICRO_ACP_HOME", t.TempDir())
	var out bytes.Buffer
	err := run(context.Background(), []string{"--demo", "exec", " "}, &out, &out)
	var exit *exitError
	if !errors.As(err, &exit) || exit.code != exitUsage {
		t.Fatalf("want usage exit, got %v", err)
	}
}

func TestAnswerPermissionHonorsPolicy(t *testing.T) {
	request := schema.RequestPermissionRequest{Options: []schema.PermissionOption{
		{Kind: schema.PermissionOptionKindAllowOnce, Name: "Allow", OptionID: "allow"},
		{Kind: schema.PermissionOptionKindRejectOnce, Name: "Reject", OptionID: "reject"},
	}}
	allowed := answerPermission(request, "allow")
	if allowed.Selected == nil || allowed.Selected.OptionID != "allow" {
		t.Fatalf("allow policy selected %+v", allowed)
	}
	rejected := answerPermission(request, "deny")
	if rejected.Selected == nil || rejected.Selected.OptionID != "reject" {
		t.Fatalf("deny policy selected %+v", rejected)
	}
	failed := answerPermission(request, "fail")
	if failed.Cancelled == nil {
		t.Fatalf("fail policy should cancel, got %+v", failed)
	}
}
