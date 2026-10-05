package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/BrokkAi/micro-acp/internal/client"
)

func TestSessionConfigurationCompletionAndBooleanRoundTrip(t *testing.T) {
	m := connectedComposer(t)
	paste(m, "/")
	if m.completion.entries[0].id != "/config" {
		t.Fatal("session configuration is not discoverable")
	}
	m.input.SetValue("/config str")
	m.input.MoveToEnd()
	m.refreshCompletion()
	if m.completion.kind != "settings" || m.completion.entries[0].id != "stream" {
		t.Fatal("custom boolean option missing")
	}
	m.Update(keyPress(tea.KeyTab, 0))
	if m.input.Value() != "/config stream " || m.completion.kind != "values" {
		t.Fatal("option did not complete to its choices")
	}
	paste(m, "off")
	_, cmd := m.completionKey(keyPress(tea.KeyEnter, 0))
	if cmd == nil {
		t.Fatal("choice was not applied")
	}
	result := cmd().(resultMsg)
	if result.err != nil {
		t.Fatal(result.err)
	}
	m.Update(result)
	if !strings.Contains(m.client.Status(), "Stream words Off") {
		t.Fatal("agent did not confirm the boolean change")
	}
	s, _ := m.client.Snapshot()
	if len(s.Messages) != 0 {
		t.Fatal("configuration was sent as a prompt")
	}
	cmd = m.command("/settings stream true")
	result = cmd().(resultMsg)
	if result.err != nil {
		t.Fatal(result.err)
	}
	m.Update(result)
	if !strings.Contains(m.client.Status(), "Stream words On") {
		t.Fatal("settings alias did not update the toggle")
	}
}

func TestConfigurationPickerRefreshPreservesSearchAndHandlesRemoval(t *testing.T) {
	m := composer(t)
	s := client.Selector{ID: "custom", Name: "Custom setting", Category: "custom-category", Current: "a", Choices: []client.Choice{{Value: "a", Name: "Alpha", Group: "Provider"}, {Value: "b", Name: "Beta", Group: "Provider"}}}
	m.chooseSetting(s)
	m.picker.input.SetValue("Beta")
	m.picker.filter()
	s.Current = "b"
	m.refreshSettingPicker([]client.Selector{s})
	if m.picker.input.Value() != "Beta" || len(m.picker.matches) != 1 || m.picker.matches[0].title != "Provider / Beta ✓" {
		t.Fatal("configuration update lost the search or retained a stale value")
	}
	m.refreshSettingPicker(nil)
	if m.picker != nil || !strings.Contains(m.lastError, "removed") {
		t.Fatal("removed option remained selectable")
	}
}

func TestConfigurationStatusPersistsDuringWorkAndErrors(t *testing.T) {
	m := connectedComposer(t)
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	for _, state := range []string{"Working", "Stopped", "Action failed"} {
		m.status = state
		m.busy, m.prompting = state == "Working", state == "Working"
		m.input.SetValue("draft for next turn")
		if state == "Action failed" {
			m.lastError = "test error"
		}
		view := m.View().Content
		for _, want := range []string{"demo", "Walkthrough", "● Stream words", "/config"} {
			if !strings.Contains(view, want) {
				t.Fatalf("%s lost %q:\n%s", state, want, view)
			}
		}
	}
}

func TestConfigurationStatusResponsiveAndExplicitOverflow(t *testing.T) {
	m := composer(t)
	fields := []client.StatusField{
		{Name: "Model", Value: "Example", Category: "model"},
		{Name: "Reasoning", Value: "High", Category: "thought_level"},
		{Name: "Mode", Value: "Ask", Category: "mode"},
		{Name: "Review", Value: "On", Boolean: true, Enabled: true},
		{Name: "Streaming", Value: "Off", Boolean: true},
		{Name: "Custom", Value: "Fast"},
	}
	for _, width := range []int{31, 76, 116} {
		view := m.configurationStatus("agent", fields, width)
		if lipgloss.Width(view) > width || lipgloss.Height(view) > 2 || !strings.Contains(view, "/config") {
			t.Fatalf("status does not fit %d:\n%s", width, view)
		}
		if width == 31 && !strings.Contains(view, "more") {
			t.Fatal("overflow was silently hidden")
		}
		if width == 116 && (!strings.Contains(view, "● Review") || !strings.Contains(view, "○ Streaming") || !strings.Contains(view, "Fast")) {
			t.Fatal("nonstandard settings missing")
		}
		if strings.Contains(view, "Model Example") || strings.Contains(view, "Reasoning High") || strings.Contains(view, "Mode Ask") {
			t.Fatal("redundant status labels remain")
		}
	}
}
