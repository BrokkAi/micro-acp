package main

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/BrokkAi/acp-go/schema"
)

func TestExecRejectsInvalidFlags(t *testing.T) {
	t.Setenv("MICRO_ACP_HOME", t.TempDir())
	for _, args := range [][]string{
		{"--demo", "--format", "bogus", "exec", "hi"},
		{"--demo", "--permission", "whatever", "exec", "hi"},
	} {
		var out bytes.Buffer
		err := run(context.Background(), args, &out, &out)
		var exit *exitError
		if !errors.As(err, &exit) || exit.code != exitUsage {
			t.Fatalf("%v: want usage exit, got %v", args, err)
		}
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
