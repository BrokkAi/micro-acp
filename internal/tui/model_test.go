package tui

import (
	"context"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/BrokkAi/acp-go/schema"
	"github.com/BrokkAi/micro-acp/internal/client"
	"github.com/BrokkAi/micro-acp/internal/config"
)

func TestPermissionDefaultsToCancel(t *testing.T) {
	m := newModel(context.Background(), Options{Config: config.Config{RegistryURL: config.RegistryURL}, Cwd: "/tmp"})
	p := client.Permission{Request: schema.RequestPermissionRequest{Options: []schema.PermissionOption{{OptionID: "allow", Name: "Allow", Kind: schema.PermissionOptionKindAllowOnce}}}, Reply: make(chan schema.RequestPermissionOutcome, 1), Done: make(chan struct{})}
	m.Update(permissionMsg{p})
	m.permissionKey("enter")
	if result := <-p.Reply; result.Cancelled == nil || result.Selected != nil {
		t.Fatalf("permission approved by default: %+v", result)
	}
}

func TestCompactPickerSearchAndSingleEnter(t *testing.T) {
	m := newModel(context.Background(), Options{})
	m.input.SetValue("keep this draft")
	m.openPicker("settings", []item{{title: "Alpha", id: "alpha", value: client.Selector{ID: "alpha"}}, {title: "Beta", id: "beta", value: client.Selector{ID: "beta", Name: "Beta", Choices: []client.Choice{{Value: "yes", Name: "Yes"}}}}})
	m.Update(tea.PasteMsg{Content: "beta"})
	if len(m.picker.matches) != 1 || m.picker.matches[0].id != "beta" {
		t.Fatal("typing did not filter choices")
	}
	m.Update(keyPress(tea.KeyEnter, 0))
	if m.picker == nil || m.picker.kind != "choices" {
		t.Fatal("Enter did not select immediately")
	}
	m.Update(keyPress(tea.KeyEscape, 0))
	if m.picker != nil || m.input.Value() != "keep this draft" {
		t.Fatal("selector lost the draft")
	}
}
func TestCancelledPermissionDoesNotBlockNext(t *testing.T) {
	m := newModel(context.Background(), Options{})
	done := make(chan struct{})
	close(done)
	m.permissionQueue = []client.Permission{{Done: done}, {Done: make(chan struct{}), Reply: make(chan schema.RequestPermissionOutcome, 1)}}
	m.nextPermission()
	if m.permission == nil || len(m.permissionQueue) != 0 {
		t.Fatal("cancelled request blocked queue")
	}
}
func TestViewFitsTerminalAndStripsControls(t *testing.T) {
	for _, size := range [][2]int{{80, 24}, {120, 40}, {35, 14}} {
		m := newModel(context.Background(), Options{Cwd: "/tmp/workspace"})
		m.Update(tea.WindowSizeMsg{Width: size[0], Height: size[1]})
		view := m.View().Content
		if width := lipgloss.Width(view); width > size[0] {
			t.Errorf("width %d exceeds %d", width, size[0])
		}
		if height := lipgloss.Height(view); height > size[1] {
			t.Errorf("height %d exceeds %d", height, size[1])
		}
	}
	value := clean("hello\x1b]52;c;secret\a\x1b[31m world\x00")
	if value != "hello world" || strings.Contains(value, "\x1b") {
		t.Fatalf("terminal control leak: %q", value)
	}
}

func TestDialogsFitWithLongDraftAndLongContext(t *testing.T) {
	for _, size := range [][2]int{{35, 14}, {80, 24}} {
		m, e := formModel(t)
		m.Update(tea.WindowSizeMsg{Width: size[0], Height: size[1]})
		m.input.SetValue(strings.Repeat("long unsent draft\n", 12))
		m.elicitation.event.Request.Message = strings.Repeat("Read this explanation. ", 100)
		m.elicitation.index = len(m.elicitation.fields)
		view := m.View().Content
		if lipgloss.Height(view) > size[1] || lipgloss.Width(view) > size[0] || !strings.Contains(view, "› Cancel") || !strings.Contains(view, "Submit") {
			t.Fatalf("form controls hidden at %v:\n%s", size, view)
		}
		m.Update(keyPress(tea.KeyEscape, 0))
		<-e.Reply
		m.Update(permissionMsg{client.Permission{Request: schema.RequestPermissionRequest{Options: []schema.PermissionOption{{Name: "Allow once"}, {Name: "Reject"}}}, Reply: make(chan schema.RequestPermissionOutcome, 1), Done: make(chan struct{})}})
		view = m.View().Content
		if lipgloss.Height(view) > size[1] || !strings.Contains(view, "› Cancel") {
			t.Fatalf("permission controls hidden:\n%s", view)
		}
	}
}
func TestCommandsDoNotLaunchWithoutAgent(t *testing.T) {
	m := newModel(context.Background(), Options{})
	if cmd := m.command("/new"); cmd != nil || m.lastError == "" {
		t.Fatal("new session should require an agent")
	}
	m.command("/help")
	if m.picker == nil || m.picker.kind != "commands" || len(m.picker.entries) < 10 {
		t.Fatal("command palette missing")
	}
}

func TestAgentPickerIncludesBuiltinsAndCustomOverrides(t *testing.T) {
	m := newModel(context.Background(), Options{Config: config.Config{Agents: map[string]config.Command{"anvil": {Command: "/custom/anvil"}}}})
	for _, id := range []string{"anvil", "muse-acp", "draupnir"} {
		count := 0
		for _, entry := range m.agents {
			if entry.id == id {
				count++
				if id == "anvil" {
					command, ok := entry.value.(config.Command)
					if !ok || command.Command != "/custom/anvil" {
						t.Fatal("built-in replaced the custom Anvil command")
					}
				}
			}
		}
		if count != 1 {
			t.Fatalf("expected one %s entry, got %d", id, count)
		}
	}
}
