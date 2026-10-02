// Command fakeagent is a minimal ACP agent used by micro-acp's login tests. It
// advertises one terminal login method and, when invoked with --login, records
// the login in the file named by FAKE_LOGIN_MARKER. Set FAKE_NO_AUTH to make it
// advertise no methods at all.
package main

import (
	"context"
	"errors"
	"os"

	acp "github.com/BrokkAi/acp-go"
	"github.com/BrokkAi/acp-go/agent"
	"github.com/BrokkAi/acp-go/schema"
)

type fakeAgent struct{}

func (fakeAgent) Initialize(context.Context, agent.Client, schema.InitializeRequest) (schema.InitializeResponse, error) {
	response := schema.InitializeResponse{
		ProtocolVersion: acp.Version,
		AgentInfo:       &schema.Implementation{Name: "fake agent", Version: "1.0.0"},
	}
	if os.Getenv("FAKE_NO_AUTH") == "" {
		description := "Record a login"
		response.AuthMethods = []schema.AuthMethod{{
			Terminal: &schema.AuthMethodTerminal{
				ID:          "fake-login",
				Name:        "Fake login",
				Description: &description,
				Args:        []string{"--login"},
			},
		}}
	}
	return response, nil
}

func (fakeAgent) NewSession(context.Context, agent.Client, schema.NewSessionRequest) (schema.NewSessionResponse, error) {
	return schema.NewSessionResponse{}, errors.New("fake agent does not create sessions")
}

func (fakeAgent) Prompt(context.Context, agent.Client, schema.PromptRequest, agent.SessionUpdater) (schema.PromptResponse, error) {
	return schema.PromptResponse{}, errors.New("fake agent does not run prompts")
}

func main() {
	for _, arg := range os.Args[1:] {
		if arg != "--login" {
			continue
		}
		if path := os.Getenv("FAKE_LOGIN_MARKER"); path != "" {
			if err := os.WriteFile(path, []byte("signed in\n"), 0o600); err != nil {
				os.Exit(1)
			}
		}
		return
	}
	if err := agent.New(fakeAgent{}).Serve(context.Background(), os.Stdin, os.Stdout); err != nil {
		os.Exit(1)
	}
}
