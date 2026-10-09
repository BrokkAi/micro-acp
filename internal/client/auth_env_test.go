package client_test

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	acp "github.com/BrokkAi/acp-go"
	"github.com/BrokkAi/micro-acp/internal/client"
	"github.com/BrokkAi/micro-acp/internal/config"
	"github.com/BrokkAi/micro-acp/internal/store"
)

// openEnvAuthTest connects to the env-auth helper agent, which advertises a
// terminal method plus an env_var method like the muse-code-acp adapter does.
// Extra entries land in the agent process environment, the same way per-agent
// config env does.
func openEnvAuthTest(t *testing.T, extraEnv map[string]string) *client.Client {
	t.Helper()
	env := map[string]string{"MICRO_ACP_TEST_HELPER": "env-auth", "MICRO_ACP_TEST_DATA": t.TempDir()}
	for k, v := range extraEnv {
		env[k] = v
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	t.Cleanup(cancel)
	c, err := client.Open(ctx, "env-auth-agent", t.TempDir(), config.Command{
		Command: os.Args[0],
		Args:    []string{"-test.run=^TestAgentProcess$"},
		Env:     env,
	}, store.Store{Directory: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := c.Shutdown(); err != nil {
			t.Error(err)
		}
	})
	return c
}

func TestEnvVarAuthSurvivesInitialize(t *testing.T) {
	c := openEnvAuthTest(t, nil)
	var envChoice *client.AuthChoice
	terminal := false
	for _, choice := range c.AuthChoices() {
		switch choice.ID {
		case "test-api-key":
			dup := choice
			envChoice = &dup
		case "test-login":
			terminal = terminal || choice.Terminal
		}
	}
	if envChoice == nil {
		t.Fatalf("env_var choice missing: %+v", c.AuthChoices())
	}
	if envChoice.Kind != client.AuthKindEnv {
		t.Fatalf("env_var kind = %q", envChoice.Kind)
	}
	if len(envChoice.Vars) != 1 || envChoice.Vars[0].Name != "MICRO_ACP_TEST_API_KEY" || !envChoice.Vars[0].Secret {
		t.Fatalf("env_var vars = %+v", envChoice.Vars)
	}
	if envChoice.Link != "https://example.com/keys" {
		t.Fatalf("env_var link = %q", envChoice.Link)
	}
	if !terminal {
		t.Fatalf("terminal choice missing: %+v", c.AuthChoices())
	}
	if methods := c.EnvAuthMethods(); len(methods) != 1 || methods[0].ID != "test-api-key" {
		t.Fatalf("env auth methods = %+v", methods)
	}
}

func TestEnvVarAuthRequiresCredential(t *testing.T) {
	c := openEnvAuthTest(t, nil)
	err := c.Authenticate("test-api-key")
	if err == nil || !strings.Contains(err.Error(), "MICRO_ACP_TEST_API_KEY") {
		t.Fatalf("authenticate without credential = %v", err)
	}
	if !acp.IsAuthRequired(err) {
		t.Fatalf("authenticate error is not auth-required: %v", err)
	}
}

func TestEnvVarAuthSucceedsWithCredential(t *testing.T) {
	c := openEnvAuthTest(t, map[string]string{"MICRO_ACP_TEST_API_KEY": "test-key"})
	if err := c.Authenticate("test-api-key"); err != nil {
		t.Fatalf("authenticate with credential = %v", err)
	}
}
