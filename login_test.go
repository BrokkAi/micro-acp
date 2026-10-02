package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

func TestLoginListsAndRunsAdvertisedMethod(t *testing.T) {
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
	base := []string{"--config", configPath, "--agent", "fake"}
	withBase := func(args ...string) []string {
		return append(append([]string{}, base...), args...)
	}

	t.Run("lists methods", func(t *testing.T) {
		var out, errOut bytes.Buffer
		if err := run(context.Background(), withBase("login"), &out, &errOut); err != nil {
			t.Fatalf("login listing: %v (%s)", err, errOut.String())
		}
		for _, want := range []string{"METHOD", "fake-login", "Fake login", "terminal", "Record a login"} {
			if !strings.Contains(out.String(), want) {
				t.Fatalf("listing missing %q:\n%s", want, out.String())
			}
		}
	})

	t.Run("lists methods without a terminal", func(t *testing.T) {
		// run() is the non-TTY entry point: the TUI guard never applies here.
		var out bytes.Buffer
		if err := run(context.Background(), withBase("login"), &out, &out); err != nil {
			t.Fatalf("listing required a terminal: %v", err)
		}
	})

	t.Run("rejects an unknown method", func(t *testing.T) {
		var out bytes.Buffer
		err := run(context.Background(), withBase("--method", "nope", "login"), &out, &out)
		var exit *exitError
		if !errors.As(err, &exit) || exit.code != exitUsage {
			t.Fatalf("want usage exit, got %v", err)
		}
		if !strings.Contains(err.Error(), "fake-login") {
			t.Fatalf("error should list available methods: %v", err)
		}
	})

	t.Run("runs the terminal login flow", func(t *testing.T) {
		marker := filepath.Join(root, "marker")
		t.Setenv("FAKE_LOGIN_MARKER", marker)
		var out bytes.Buffer
		if err := run(context.Background(), withBase("--method", "fake-login", "login"), &out, &out); err != nil {
			t.Fatalf("terminal login: %v", err)
		}
		if _, err := os.Stat(marker); err != nil {
			t.Fatalf("terminal login process did not run: %v", err)
		}
	})

	t.Run("reports an agent with no methods", func(t *testing.T) {
		t.Setenv("FAKE_NO_AUTH", "1")
		var out bytes.Buffer
		err := run(context.Background(), withBase("login"), &out, &out)
		var exit *exitError
		if !errors.As(err, &exit) || exit.code != exitAuth {
			t.Fatalf("want auth exit, got %v", err)
		}
		if !strings.Contains(err.Error(), "no login methods") {
			t.Fatalf("unexpected error: %v", err)
		}
	})
}

func buildFakeAgent(t *testing.T, dir string) string {
	t.Helper()
	name := "fakeagent"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	path := filepath.Join(dir, name)
	build := exec.Command("go", "build", "-o", path, "./testdata/fakeagent")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build fake agent: %v\n%s", err, out)
	}
	return path
}
