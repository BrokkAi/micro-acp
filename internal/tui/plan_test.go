package tui

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/BrokkAi/acp-go/agent"
	"github.com/BrokkAi/acp-go/schema"
	"github.com/BrokkAi/micro-acp/internal/client"
	"github.com/BrokkAi/micro-acp/internal/demo"
	"github.com/charmbracelet/x/ansi"
)

type planAgent struct{ demo.Agent }

func (a *planAgent) Prompt(ctx context.Context, c agent.Client, r schema.PromptRequest, updates agent.SessionUpdater) (schema.PromptResponse, error) {
	if r.Prompt[0].Text.Text == "clear" {
		return schema.PromptResponse{StopReason: schema.StopReasonEndTurn}, updates.Update(schema.SessionUpdate{Plan: &schema.Plan{Entries: []schema.PlanEntry{}}})
	}
	status := schema.ToolCallStatusInProgress
	if err := updates.Update(schema.SessionUpdate{ToolCall: &schema.ToolCall{ToolCallID: "work", Title: "Working", Status: &status}}); err != nil {
		return schema.PromptResponse{}, err
	}
	entries := []schema.PlanEntry{
		{Content: "Review the code", Priority: schema.PlanEntryPriorityHigh, Status: schema.PlanEntryStatusInProgress},
		{Content: "Run the tests", Priority: schema.PlanEntryPriorityMedium, Status: schema.PlanEntryStatusPending},
		{Content: "Update the docs", Priority: schema.PlanEntryPriorityLow, Status: schema.PlanEntryStatusPending},
	}
	for stage := range 3 {
		if stage == 1 {
			entries[0].Status = schema.PlanEntryStatusCompleted
			entries[1].Status = schema.PlanEntryStatusInProgress
		}
		if stage == 2 {
			entries[1].Status = schema.PlanEntryStatusCompleted
			entries[2].Status = schema.PlanEntryStatusCompleted
		}
		if err := updates.Update(schema.SessionUpdate{Plan: &schema.Plan{Entries: entries}}); err != nil {
			return schema.PromptResponse{}, err
		}
		// Hold each snapshot until the test has inspected it, without timing races.
		_, err := c.RequestPermission(ctx, schema.RequestPermissionRequest{SessionID: r.SessionID, ToolCall: schema.ToolCallUpdate{ToolCallID: "work"}, Options: []schema.PermissionOption{{OptionID: "next", Name: "Next", Kind: schema.PermissionOptionKindAllowOnce}}})
		if err != nil {
			return schema.PromptResponse{}, err
		}
	}
	status = schema.ToolCallStatusCompleted
	return schema.PromptResponse{StopReason: schema.StopReasonEndTurn}, updates.Update(schema.SessionUpdate{ToolCallUpdate: &schema.ToolCallUpdate{ToolCallID: "work", Status: &status}})
}

func TestLivePlanUpdatesAndSessionLifecycle(t *testing.T) {
	t.Setenv("MICRO_ACP_TUI_PLAN", "yes")
	m := connectedComposer(t)
	m.syncTranscript()
	m.printQueue = nil
	m.busy, m.prompting = true, true
	m.startedAt = time.Now()
	done := make(chan error, 1)
	go func() { _, err := m.client.Prompt("plan"); done <- err }()
	for stage, want := range []string{"■ Review the code · high", "■ Run the tests", "Plan · 3/3 complete"} {
		var gate client.Permission
		select {
		case gate = <-m.client.Permissions:
		case <-time.After(5 * time.Second):
			t.Fatal("plan update did not arrive")
		}
		m.syncTranscript()
		view := ansi.Strip(m.View().Content)
		if !strings.Contains(view, want) || strings.Count(view, "Plan ·") != 1 || !strings.Contains(view, "Update the docs") {
			t.Errorf("stage %d: missing or duplicated checklist:\n%s", stage, view)
		}
		if stage > 0 && strings.Contains(view, "■ Review the code") {
			t.Error("old plan status remained visible")
		}
		if strings.Contains(strings.Join(m.printQueue, "\n"), "Review the code") {
			t.Error("plan snapshot was committed to scrollback")
		}
		gate.Reply <- schema.RequestPermissionOutcome{Cancelled: &schema.RequestPermissionOutcomeCancelled{}}
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	m.busy, m.prompting = false, false
	m.syncTranscript()
	if !strings.Contains(ansi.Strip(m.View().Content), "Plan · 3/3 complete") {
		t.Fatal("completed plan disappeared")
	}
	m.refreshDetails()
	if details := ansi.Strip(m.viewport.GetContent()); !strings.Contains(details, "in_progress  [high] Review the code") || !strings.Contains(details, "completed  [low] Update the docs") {
		t.Fatalf("plan history or priorities missing from details: %s", details)
	}
	saved, _ := m.client.Snapshot()
	if err := m.client.New(); err != nil {
		t.Fatal(err)
	}
	m.syncTranscript()
	if m.plan != nil || strings.Contains(ansi.Strip(m.View().Content), "Plan ·") {
		t.Fatal("plan leaked into a new session")
	}
	if err := m.client.Load(saved); err != nil {
		t.Fatal(err)
	}
	m.syncTranscript()
	if !strings.Contains(ansi.Strip(m.View().Content), "Plan · 3/3 complete") {
		t.Fatal("saved plan was not restored")
	}
	if _, err := m.client.Prompt("clear"); err != nil {
		t.Fatal(err)
	}
	m.syncTranscript()
	if strings.Contains(ansi.Strip(m.View().Content), "Plan ·") {
		t.Fatal("empty plan did not clear the checklist")
	}
}

func TestPlanLayoutKeepsActiveStepAndComposerVisible(t *testing.T) {
	for _, size := range [][2]int{{80, 24}, {35, 14}, {24, 12}} {
		m := composer(t)
		m.Update(tea.WindowSizeMsg{Width: size[0], Height: size[1]})
		m.input.SetValue("my draft")
		m.live = strings.Repeat("Streaming output\n", 30)
		m.plan = &schema.Plan{}
		for i := range 20 {
			status := schema.PlanEntryStatusCompleted
			if i == 16 {
				status = schema.PlanEntryStatusInProgress
			} else if i > 16 {
				status = schema.PlanEntryStatusPending
			}
			m.plan.Entries = append(m.plan.Entries, schema.PlanEntry{Content: fmt.Sprintf("Step %02d 🦊 very long text\nwith controls\x1b]52;c;secret\a", i), Status: status, Priority: schema.PlanEntryPriorityHigh})
		}
		view := m.View()
		text := ansi.Strip(view.Content)
		if lipgloss.Width(view.Content) > size[0] || lipgloss.Height(view.Content) > size[1] {
			t.Errorf("layout exceeds %v:\n%s", size, text)
		}
		for _, want := range []string{"Plan · 16/20", "■ Step 16", "my draft", "Streaming output"} {
			if !strings.Contains(text, want) {
				t.Errorf("%v: missing %q:\n%s", size, want, text)
			}
		}
		if strings.Contains(view.Content, "secret") || view.Cursor == nil || view.Cursor.Y >= size[1] {
			t.Errorf("invalid cursor or unsanitized plan: %+v", view.Cursor)
		}
		m.page = "details"
		if strings.Contains(ansi.Strip(m.View().Content), "Plan ·") {
			t.Fatal("checklist displaced the details panel")
		}
	}
}
