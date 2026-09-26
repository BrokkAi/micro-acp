package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
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

func TestAgentsListsBuiltinsOfflineAndHonorsCustomOverrides(t *testing.T) {
	root := t.TempDir()
	t.Setenv("MICRO_ACP_HOME", root)
	if err := os.WriteFile(filepath.Join(root, "config.json"), []byte(`{"agents":{"anvil":{"command":"/custom/anvil"}}}`), 0600); err != nil {
		t.Fatal(err)
	}
	var out, warnings bytes.Buffer
	if err := run(context.Background(), []string{"--offline", "agents"}, &out, &warnings); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"anvil", "muse-acp", "draupnir"} {
		count := 0
		for _, row := range strings.Split(out.String(), "\n") {
			if fields := strings.Fields(row); len(fields) > 0 && fields[0] == id {
				count++
			}
		}
		if count != 1 {
			t.Fatalf("expected one %s entry, got %d: %s", id, count, out.String())
		}
	}
	if !strings.Contains(out.String(), "/custom/anvil") || warnings.Len() == 0 {
		t.Fatalf("missing custom override or unavailable-registry warning: %s / %s", out.String(), warnings.String())
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
