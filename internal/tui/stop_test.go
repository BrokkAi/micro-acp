package tui

import (
	"errors"
	"strings"
	"testing"

	"github.com/BrokkAi/acp-go/schema"
	"github.com/BrokkAi/micro-acp/internal/client"
	"github.com/charmbracelet/x/ansi"
)

func TestPromptStopReasonsPauseQueueAndRemainVisible(t *testing.T) {
	for _, test := range []struct {
		reason schema.StopReason
		want   string
	}{
		{schema.StopReasonCancelled, "Stopped"},
		{schema.StopReasonMaxTokens, "token limit reached"},
		{schema.StopReasonMaxTurnRequests, "agent request limit reached"},
		{schema.StopReasonRefusal, "agent declined to continue"},
		{"future_reason", "future_reason"},
	} {
		t.Run(string(test.reason), func(t *testing.T) {
			m := composer(t)
			m.client = &client.Client{Agent: "test"}
			m.busy, m.prompting = true, true
			m.queued = []queuedPrompt{{text: "follow-up"}}
			m.Update(resultMsg{status: "Ready", reason: test.reason, prompt: true})
			if !m.queuePaused || len(m.queued) != 1 || m.prompting || m.busy || m.lastError != "" {
				t.Fatal("non-normal stop dispatched the queue, remained busy or became an error")
			}
			view := ansi.Strip(m.View().Content)
			if !strings.Contains(view, test.want) || !strings.Contains(view, "/queue send to continue") {
				t.Fatalf("missing stop reason or queue recovery hint:\n%s", view)
			}
			m.sendQueued()
			if m.queuePaused || len(m.queued) != 0 || !m.prompting {
				t.Fatal("explicit queue resume failed")
			}
		})
	}
}

func TestNormalStopContinuesQueueAndErrorsKeepTheirMessage(t *testing.T) {
	m := composer(t)
	m.client = &client.Client{Agent: "test"}
	m.queued = []queuedPrompt{{text: "follow-up"}}
	m.Update(resultMsg{status: "Ready", reason: schema.StopReasonEndTurn, prompt: true})
	if len(m.queued) != 0 || !m.prompting || m.queuePaused {
		t.Fatal("normal end of turn stopped automatic queue dispatch")
	}
	m.Update(resultMsg{status: "Ready", reason: schema.StopReasonMaxTokens, prompt: true, err: errors.New("connection failed")})
	if m.status != "Action failed" || m.lastError != "connection failed" || !m.queuePaused {
		t.Fatal("stop reason obscured the request error")
	}
}
