package tui

import (
	"fmt"
	"strings"

	"charm.land/bubbles/v2/list"
	tea "charm.land/bubbletea/v2"
)

var commands = []item{
	{title: "New session", description: "Start a fresh conversation", id: "/new"},
	{title: "Sessions", description: "Browse local and agent sessions", id: "/sessions"},
	{title: "Agents", description: "Choose a registry or custom agent", id: "/agents"},
	{title: "Fork session", description: "Create a native branch of the active session", id: "/fork"},
	{title: "Fork with text context", description: "New session seeded with saved user and assistant text", id: "/fork --context"},
	{title: "Delete session", description: "Delete the active session from the agent and this client", id: "/delete"},
	{title: "Forget session locally", description: "Remove only this client's saved session", id: "/forget"},
	{title: "Agent details", description: "Capabilities, auth methods, modes and model options", id: "/info"},
	{title: "Authenticate", description: "/auth <method ID> from /info", id: "/auth "},
	{title: "Select mode", description: "/mode <mode ID> from /info", id: "/mode "},
	{title: "Select model", description: "/model <model ID> from /info", id: "/model "},
	{title: "Reasoning effort", description: "/effort <value> from /info", id: "/effort "},
	{title: "Load session", description: "/load <local session ID>", id: "/load "},
	{title: "Refresh registry", description: "Check the latest published agent versions", id: "/refresh"},
	{title: "Help", description: "Commands and keyboard shortcuts", id: "/help"},
	{title: "Quit", description: "Close the agent and exit", id: "/quit"},
}

func (m *model) openCommands() {
	items := make([]list.Item, len(commands))
	for i, c := range commands {
		items[i] = c
	}
	m.openPicker("commands", items)
}
func (m *model) command(text string) tea.Cmd {
	parts := strings.Fields(text)
	if len(parts) == 0 {
		return nil
	}
	name := parts[0]
	arg := strings.TrimSpace(strings.TrimPrefix(text, name))
	m.lastError = ""
	switch name {
	case "/quit", "/exit":
		return tea.Quit
	case "/agents":
		m.openPicker("agents", m.agents)
		return nil
	case "/help":
		m.openCommands()
		return nil
	case "/refresh":
		m.busy = true
		m.status = "Refreshing registry…"
		return m.fetchCatalog()
	}
	if m.client == nil {
		m.lastError = "Choose an agent with /agents first"
		return nil
	}
	c := m.client
	switch name {
	case "/new":
		return m.perform("Creating session…", c.New)
	case "/sessions":
		m.busy = true
		m.status = "Loading sessions…"
		return func() tea.Msg { s, err := c.Sessions(); return sessionsMsg{s, err} }
	case "/load":
		s, err := m.store.Load(arg)
		if err != nil {
			m.lastError = err.Error()
			return nil
		}
		return m.perform("Loading session…", func() error { return c.Load(s) })
	case "/fork":
		if arg != "" && arg != "--context" {
			m.lastError = "Usage: /fork [--context]"
			return nil
		}
		return m.perform("Forking session…", func() error { return c.Fork(arg == "--context") })
	case "/delete", "/forget":
		s, _ := c.Snapshot()
		if s.ID == "" {
			m.lastError = "No active session"
			return nil
		}
		if name == "/delete" && !c.CanDelete() {
			m.lastError = "Agent does not support deletion; /forget removes only the local copy"
			return nil
		}
		m.confirm = &s
		m.localDelete = name == "/forget"
		return nil
	case "/auth":
		if arg == "" {
			m.lastError = "Use /info to find an auth method, then /auth <method ID>"
			return nil
		}
		return m.perform("Authenticating…", func() error { return c.Authenticate(arg) })
	case "/mode", "/model", "/effort":
		if arg == "" {
			m.lastError = fmt.Sprintf("Usage: %s <value> · /info shows available options", name)
			return nil
		}
		return m.perform("Updating "+name[1:]+"…", func() error { return c.Configure(name[1:], arg) })
	case "/info":
		m.info = c.Details()
		m.page = "info"
		m.viewport.SetContent(clean(m.info))
		m.viewport.GotoTop()
		return nil
	default:
		m.lastError = "Unknown command " + name + " · /help lists commands"
		return nil
	}
}
