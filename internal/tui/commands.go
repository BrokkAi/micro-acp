package tui

import (
	"fmt"
	"strings"

	"charm.land/bubbles/v2/list"
	tea "charm.land/bubbletea/v2"
	"github.com/BrokkAi/micro-acp/internal/client"
)

var commands = []item{
	{title: "New session", description: "Start a fresh conversation", id: "/new"},
	{title: "Close session", description: "Close the active session while keeping its history", id: "/close"},
	{title: "Log out", description: "End authentication with the connected agent", id: "/logout"},
	{title: "Sessions", description: "Browse local and agent sessions", id: "/sessions"},
	{title: "Agents", description: "Choose a registry or custom agent", id: "/agents"},
	{title: "Fork session", description: "Create a native branch of the active session", id: "/fork"},
	{title: "Fork with text context", description: "New session seeded with saved user and assistant text", id: "/fork --context"},
	{title: "Delete session", description: "Delete the active session from the agent and this client", id: "/delete"},
	{title: "Forget session locally", description: "Remove only this client's saved session", id: "/forget"},
	{title: "Agent details", description: "Capabilities, auth methods, modes and model options", id: "/info"},
	{title: "Authenticate", description: "Choose a login method", id: "/auth"},
	{title: "Select mode", description: "Choose an agent operating mode", id: "/mode"},
	{title: "Select model", description: "Choose a model", id: "/model"},
	{title: "Reasoning effort", description: "Choose a reasoning level", id: "/effort"},
	{title: "Session settings", description: "All agent-provided options, including toggles", id: "/settings"},
	{title: "Attach a file", description: "Text, images, audio, or binary resources", id: "/attach "},
	{title: "Clear attachments", description: "Remove queued attachments", id: "/detach"},
	{title: "Load session", description: "/load <local session ID>", id: "/load "},
	{title: "Refresh registry", description: "Check the latest published agent versions", id: "/refresh"},
	{title: "Help", description: "Commands and keyboard shortcuts", id: "/help"},
	{title: "Quit", description: "Close the agent and exit", id: "/quit"},
}

func (m *model) openCommands() {
	all := m.allCommands()
	items := make([]list.Item, len(all))
	for i, c := range all {
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
		m.resumeTarget = nil
		return m.perform("Creating session…", c.New)
	case "/close":
		return m.perform("Closing session…", c.CloseSession)
	case "/logout":
		return m.perform("Logging out…", c.Logout)
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
		m.resumeTarget = &s
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
			m.openAuth()
			return nil
		}
		return m.authenticate(arg)
	case "/mode", "/model", "/effort":
		if arg == "" {
			m.openSettings(name[1:])
			return nil
		}
		return m.perform("Updating "+name[1:]+"…", func() error { return c.Configure(name[1:], arg) })
	case "/settings":
		m.openSettings("")
		return nil
	case "/attach":
		if arg == "" {
			m.input.SetValue("@")
			return m.findFiles()
		}
		if _, err := c.Attachment(arg); err != nil {
			m.lastError = err.Error()
			return nil
		}
		m.attachments = append(m.attachments, arg)
		m.status = "Attached " + arg
		return nil
	case "/detach":
		m.attachments = nil
		m.status = "Attachments cleared"
		return nil
	case "/agent":
		if arg == "" {
			m.lastError = "Usage: /agent <command and arguments>"
			return nil
		}
		return m.sendPrompt("/" + strings.TrimPrefix(arg, "/"))
	case "/info":
		m.info = c.Details()
		m.page = "info"
		m.viewport.SetContent(clean(m.info))
		m.viewport.GotoTop()
		return nil
	default:
		return m.sendPrompt(text)
	}
}

func (m *model) allCommands() []item {
	all := append([]item(nil), commands...)
	if m.client != nil {
		s, _ := m.client.Snapshot()
		for _, command := range s.Commands {
			name := "/" + command.Name
			for _, local := range commands {
				if strings.TrimSpace(local.id) == name {
					name = "/agent " + command.Name
					break
				}
			}
			all = append(all, item{title: name, description: "Agent · " + command.Description, id: name})
		}
	}
	return all
}

func (m *model) openSettings(category string) {
	if category == "effort" {
		category = "thought_level"
	}
	var entries []list.Item
	for _, s := range m.client.Selectors() {
		if category != "" && s.Category != category {
			continue
		}
		entries = append(entries, item{title: s.Name, description: s.Current + " · " + s.Description, id: s.ID, value: s})
	}
	if len(entries) == 0 {
		m.lastError = "Agent does not offer " + category + " settings"
		return
	}
	if category != "" && len(entries) == 1 {
		m.chooseSetting(entries[0].(item).value.(client.Selector))
		return
	}
	m.openPicker("settings", entries)
}
func (m *model) chooseSetting(s client.Selector) {
	m.selector = s
	var entries []list.Item
	selected := 0
	for i, choice := range s.Choices {
		title := choice.Name
		if choice.Group != "" {
			title = choice.Group + " / " + title
		}
		if choice.Value == s.Current {
			title += " ✓"
			selected = i
		}
		entries = append(entries, item{title: title, description: choice.Description, id: choice.Value})
	}
	m.openPicker("choices", entries)
	m.picker.Select(selected)
}
func (m *model) sendPrompt(text string) tea.Cmd {
	c := m.client
	if c == nil {
		m.lastError = "Choose an agent first"
		return nil
	}
	blocks, err := m.promptBlocks(text)
	if err != nil {
		m.lastError = err.Error()
		m.input.SetValue(text)
		return nil
	}
	m.attachments = nil
	m.busy = true
	m.retryOperation = nil
	m.prompting = true
	m.lastError = ""
	m.status = "Working…"
	m.draft = ""
	m.viewport.GotoBottom()
	return func() tea.Msg {
		reason, err := c.PromptContent(blocks)
		return resultMsg{status: fmt.Sprintf("Turn finished · %s", reason), err: err}
	}
}
