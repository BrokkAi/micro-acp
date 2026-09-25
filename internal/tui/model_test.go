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
func TestCommandsDoNotLaunchWithoutAgent(t *testing.T) {
	m := newModel(context.Background(), Options{})
	if cmd := m.command("/new"); cmd != nil || m.lastError == "" {
		t.Fatal("new session should require an agent")
	}
	m.command("/help")
	if m.page != "commands" || len(m.picker.Items()) < 10 {
		t.Fatal("command palette missing")
	}
}
