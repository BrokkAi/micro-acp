package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

func TestCLIWithoutTerminal(t *testing.T) {
	t.Setenv("MICRO_ACP_HOME", t.TempDir())
	for _, args := range [][]string{{"--help"}, {"--version"}, {"config"}, {"sessions"}} {
		var out, stderr bytes.Buffer
		if err := run(context.Background(), args, &out, &stderr); err != nil {
			t.Fatalf("%v: %v", args, err)
		}
		if out.Len()+stderr.Len() == 0 {
			t.Fatalf("%v produced no output", args)
		}
	}
}
func TestCLIRejectsAmbiguousLaunchAndTraversal(t *testing.T) {
	t.Setenv("MICRO_ACP_HOME", t.TempDir())
	for _, args := range [][]string{{"--agent", "x", "--demo"}, {"--", ""}, {"sessions", "forget", "../../secret"}, {"unknown"}} {
		var out bytes.Buffer
		if err := run(context.Background(), args, &out, &out); err == nil {
			t.Fatalf("accepted %v", args)
		}
	}
	var out bytes.Buffer
	err := run(context.Background(), []string{"--config", "/missing/config"}, &out, &out)
	if err == nil || !strings.Contains(err.Error(), "/missing/config") {
		t.Fatalf("missing explicit config: %v", err)
	}
}
