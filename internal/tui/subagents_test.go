package tui

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	acp "github.com/BrokkAi/acp-go"
	"github.com/charmbracelet/x/ansi"
)

func TestSubagentRowsPickerAndTranscript(t *testing.T) {
	m := connectedComposer(t)
	m.width, m.height = 100, 40
	s, _ := m.client.Snapshot()
	run := m.startPrompt(queuedPrompt{text: "subagent", blocks: []acp.Content{acp.NewTextContent("subagent")}, sessionID: s.ID})
	results := make(chan tea.Msg, 1)
	go func() { results <- run() }()

	// The child asks for permission while the turn runs.
	var p permissionMsg
	select {
	case permission := <-m.client.Permissions:
		p = permissionMsg{permission}
	case <-time.After(10 * time.Second):
		t.Fatal("child permission never arrived")
	}
	m.syncTranscript()
	if printed := ansi.Strip(strings.Join(m.printQueue, "\n")); !strings.Contains(printed, "◇ Survey the workspace · waiting on you") {
		t.Fatalf("child row was not committed while it runs:\n%s", printed)
	}
	m.printQueue = nil
	m.Update(p)
	if view := ansi.Strip(m.View().Content); !strings.Contains(view, "Permission required · ◇ Survey the workspace") || !strings.Contains(view, "Let the subagent") {
		t.Fatalf("permission does not name the subagent:\n%s", view)
	}
	m.permissionChoice = 0
	m.Update(keyPress(tea.KeyEnter, 0))

	result := (<-results).(resultMsg)
	if result.err != nil {
		t.Fatal(result.err)
	}
	m.busy, m.prompting = false, false
	m.syncTranscript()
	printed := ansi.Strip(strings.Join(m.printQueue, "\n"))
	if !strings.Contains(printed, "◇ Survey the workspace · done") || !strings.Contains(printed, "The subagent finished") {
		t.Fatalf("child row was not reprinted with its final state:\n%s", printed)
	}
	if strings.Contains(printed, "Demo report") {
		t.Fatalf("child output leaked into the parent transcript:\n%s", printed)
	}

	m.command("/subagents")
	if m.picker == nil || m.picker.kind != "subagents" || len(m.picker.entries) != 2 {
		t.Fatalf("picker = %+v", m.picker)
	}
	if nested := m.picker.entries[1].title; nested != "↳ Check the README" {
		t.Fatalf("nested child row = %q", nested)
	}
	m.Update(keyPress(tea.KeyEnter, 0))
	if m.page != "subagent" {
		t.Fatalf("page = %q", m.page)
	}
	view := ansi.Strip(m.View().Content)
	if !strings.Contains(view, "Subagent · Survey the workspace · done") {
		t.Fatalf("child page title missing:\n%s", view)
	}
	body := ansi.Strip(m.viewport.GetContent())
	for _, want := range []string{"Look around the workspace and summarize it.", "List workspace files", "Check the README", "Demo report"} {
		if !strings.Contains(body, want) {
			t.Fatalf("child transcript lacks %q:\n%s", want, body)
		}
	}
	if strings.Contains(view, "Ctrl+X") {
		t.Fatal("a finished child offers to stop")
	}
	m.Update(keyPress(tea.KeyEscape, 0))
	if m.page != "chat" {
		t.Fatalf("Esc left page %q", m.page)
	}
}
