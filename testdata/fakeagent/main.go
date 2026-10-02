// Command fakeagent is a minimal ACP agent used by micro-acp's tests. It serves
// one session that always replies "fake reply", and advertises one terminal
// login method that, when invoked with --login, records the login in the file
// named by FAKE_LOGIN_MARKER. Set FAKE_NO_AUTH to advertise no login methods.
package main

import (
	"context"
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
	load := true
	response.AgentCapabilities = &schema.AgentCapabilities{LoadSession: &load}
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
	return schema.NewSessionResponse{SessionID: "fake-session"}, nil
}

func (fakeAgent) LoadSession(context.Context, agent.Client, schema.LoadSessionRequest) (schema.LoadSessionResponse, error) {
	return schema.LoadSessionResponse{}, nil
}

func (fakeAgent) Prompt(_ context.Context, _ agent.Client, _ schema.PromptRequest, updates agent.SessionUpdater) (schema.PromptResponse, error) {
	chunk := schema.ContentChunk{Content: acp.NewTextContent("fake reply")}
	if err := updates.Update(schema.SessionUpdate{AgentMessageChunk: &chunk}); err != nil {
		return schema.PromptResponse{}, err
	}
	return schema.PromptResponse{StopReason: schema.StopReasonEndTurn}, nil
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
