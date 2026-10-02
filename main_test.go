package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/BrokkAi/micro-acp/internal/buildinfo"
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
		if args[0] == "--version" && strings.TrimSpace(out.String()) != buildinfo.Version {
			t.Fatalf("CLI version %q does not match build %q", out.String(), buildinfo.Version)
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
	if strings.Contains(out.String(), "latest") || !strings.Contains(out.String(), "unavailable") || strings.Contains(warnings.String(), "resolve @brokkai/anvil") {
		t.Fatalf("unresolved versions or custom overrides were mishandled: %s / %s", out.String(), warnings.String())
	}
}

func TestAgentsAcceptsTrailingFlagsAndPrintsJSON(t *testing.T) {
	root := t.TempDir()
	t.Setenv("MICRO_ACP_HOME", root)
	if err := os.WriteFile(filepath.Join(root, "config.json"), []byte(`{"agents":{"local":{"command":"/custom/local"}}}`), 0600); err != nil {
		t.Fatal(err)
	}

	// Flags may follow the subcommand instead of preceding it.
	var text, warnings bytes.Buffer
	if err := run(context.Background(), []string{"agents", "--offline"}, &text, &warnings); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"anvil", "muse-acp", "draupnir", "local"} {
		if !strings.Contains(text.String(), id) {
			t.Fatalf("trailing flags did not list %s:\n%s", id, text.String())
		}
	}

	var out bytes.Buffer
	if err := run(context.Background(), []string{"agents", "--offline", "--json"}, &out, &warnings); err != nil {
		t.Fatal(err)
	}
	var rows []struct {
		ID, Name, Version, Kind, Launch string
	}
	if err := json.Unmarshal(out.Bytes(), &rows); err != nil {
		t.Fatalf("agents --json is not valid JSON: %v\n%s", err, out.String())
	}
	seen := map[string]bool{}
	for _, row := range rows {
		seen[row.ID] = true
	}
	for _, id := range []string{"anvil", "muse-acp", "draupnir", "local"} {
		if !seen[id] {
			t.Fatalf("agents --json missing %s:\n%s", id, out.String())
		}
	}
}

func TestExecInlineCommandSurvivesTrailingFlags(t *testing.T) {
	t.Setenv("MICRO_ACP_HOME", t.TempDir())
	original := stdin
	t.Cleanup(func() { stdin = original })
	stdin = strings.NewReader("hi\n")

	var out bytes.Buffer
	missing := filepath.Join(t.TempDir(), "missing-agent")
	err := run(context.Background(), []string{"exec", "--format", "json", "--", missing}, &out, &out)
	if err == nil {
		t.Fatal("expected the missing inline agent to fail")
	}
	if strings.Contains(err.Error(), "provide --agent") {
		t.Fatalf("trailing flags swallowed the inline command: %v", err)
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
