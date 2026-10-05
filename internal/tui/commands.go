package tui

import (
	"net/url"
	"strings"

	tea "charm.land/bubbletea/v2"
	acp "github.com/BrokkAi/acp-go"
)

var commands = []item{
	{title: "Session configuration", description: "All agent options and current values · /config <option> <value>", id: "/config"},
	{title: "Model", description: "Choose the model", id: "/model"},
	{title: "Mode", description: "Choose the agent's operating mode", id: "/mode"},
	{title: "Reasoning effort", description: "Choose how much the model reasons", id: "/effort"},
	{title: "Tool details", description: "Expand tools, reasoning and full output · Ctrl+O", id: "/details"},
	{title: "Queued prompts", description: "Edit or remove prompts · /queue send resumes · /queue clear removes all", id: "/queue"},
	{title: "Subagents", description: "Child sessions the agent started · open one to read its transcript", id: "/subagents"},

	{title: "New session", description: "Start a fresh conversation", id: "/new"},
	{title: "Close session", description: "Close the active session while keeping its history", id: "/close"},
	{title: "Log out", description: "End authentication with the connected agent", id: "/logout"},
	{title: "Sessions", description: "Browse local and agent sessions", id: "/sessions"},
	{title: "Agents", description: "Choose a registry or custom agent", id: "/agents"},
	{title: "Reconnect", description: "Restart the agent and reload the saved session when supported", id: "/reconnect"},
	{title: "Fork session", description: "Create a native branch of the active session", id: "/fork"},
	{title: "Fork with text context", description: "New session seeded with saved user and assistant text", id: "/fork --context"},
	{title: "Delete session", description: "Delete the active session from the agent and this client", id: "/delete"},
	{title: "Forget session locally", description: "Remove only this client's saved session", id: "/forget"},
	{title: "Agent details", description: "Capabilities, auth methods, modes and model options", id: "/info"},
	{title: "Agent logs", description: "View recent stderr and the last error", id: "/logs"},
	{title: "Authenticate", description: "Choose a login method", id: "/auth"},
	{title: "Session settings", description: "Alias for /config · all agent options and current values", id: "/settings"},
	{title: "Attach a file", description: "Text, images, audio, or binary resources", id: "/attach "},
	{title: "Attach a resource link", description: "/resource <URI> adds a reference to the next prompt", id: "/resource "},
	{title: "Clear attachments", description: "Remove queued attachments", id: "/detach"},
	{title: "Load session", description: "/load <local session ID>", id: "/load "},
	{title: "Refresh registry", description: "Check the latest published agent versions", id: "/refresh"},
	{title: "Help", description: "Commands and keyboard shortcuts", id: "/help"},
	{title: "Quit", description: "Close the agent and exit", id: "/quit"},
}

func (m *model) openCommands() { m.openPicker("commands", m.allCommands()) }
func (m *model) command(text string) tea.Cmd {
	parts := strings.Fields(text)
	if len(parts) == 0 {
		return nil
	}
	name := parts[0]
	arg := strings.TrimSpace(strings.TrimPrefix(text, name))
	lastError := m.lastError
	m.lastError = ""
	switch name {
	case "/quit", "/exit":
		return m.quit()
	case "/agents":
		if arg != "" {
			for _, agent := range m.agents {
				if agent.id == arg {
					if m.busy {
						m.lastError = "Stop the current turn with Esc first"
						return nil
					}
					return m.connect(agent)
				}
			}
		}
		m.openPicker("agents", m.agents)
		m.picker.input.SetValue(arg)
		m.picker.filter()
		return nil
	case "/help":
		m.openCommands()
		return nil
	case "/refresh":
		m.catalogLoading = true
		m.filesLoaded = false
		m.status = "Refreshing registry…"
		return m.fetchCatalog()
	}
	if m.client == nil {
		m.lastError = "Choose an agent with /agents first"
		return nil
	}
	c := m.client
	if m.busy && name != "/info" && name != "/logs" && name != "/details" && name != "/queue" && name != "/attach" && name != "/detach" && name != "/resource" && name != "/subagents" {
		m.lastError = "Stop the current turn with Esc before changing the session"
		return nil
	}
	switch name {
	case "/reconnect":
		if s, _ := c.Snapshot(); s.ID != "" && c.CanLoad() {
			m.options.Resume = &s
		}
		return m.connect(item{title: c.Agent, id: c.Agent, value: c.Invocation()})
	case "/new":
		m.resumeTarget = nil
		return m.perform("Creating session…", c.New)
	case "/close":
		return m.perform("Closing session…", c.CloseSession)
	case "/logout":
		return m.perform("Logging out…", c.Logout)
	case "/sessions", "/resume":
		m.busy = true
		m.status = "Loading sessions…"
		return func() tea.Msg { s, err := c.Sessions(); return sessionsMsg{s, err} }
	case "/load":
		if arg == "" {
			return m.command("/sessions")
		}
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
	case "/config", "/settings":
		return m.configureSession(arg)
	case "/attach":
		if arg == "" {
			m.input.SetValue("@")
			return m.refreshCompletion()
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
		m.resources = nil
		m.status = "Attachments cleared"
		return nil
	case "/resource":
		u, err := url.Parse(arg)
		if err != nil || u.Scheme == "" {
			m.lastError = "Usage: /resource <absolute URI>"
			return nil
		}
		m.resources = append(m.resources, acp.NewResourceLinkContent(arg, arg))
		m.status = "Attached resource " + arg
		return nil
	case "/agent":
		if arg == "" {
			m.lastError = "Usage: /agent <command and arguments>"
			return nil
		}
		return m.sendPrompt("/" + strings.TrimPrefix(arg, "/"))
	case "/subagents":
		entries := m.subagentItems()
		if len(entries) == 0 {
			m.status = "No subagents in this session"
			return nil
		}
		if arg != "" {
			for _, entry := range entries {
				if entry.id == arg {
					m.openSubagent(arg)
					return nil
				}
			}
		}
		m.openPicker("subagents", entries)
		return nil
	case "/details":
		m.page = "details"
		m.refreshDetails()
		m.viewport.GotoBottom()
		return nil
	case "/queue":
		if arg == "send" {
			m.queuePaused = false
			return m.dispatchQueue()
		}
		if arg == "clear" {
			m.queued = nil
			m.queuePaused = false
			if m.editing != nil {
				m.restoreQueueDraft()
			}
			return nil
		}
		m.openPicker("queue", m.queueItems())
		return nil
	case "/info":
		m.info = c.Details()
		m.page, m.pageTitle = "info", "Agent details"
		m.viewport.SetContent(clean(m.info))
		m.viewport.GotoTop()
		return nil
	case "/logs":
		m.info = strings.TrimSpace(lastError + "\n" + c.Diagnostics())
		if m.info == "" {
			m.info = "No agent errors or stderr output."
		}
		m.page, m.pageTitle = "info", "Agent logs"
		m.viewport.SetContent(clean(m.info))
		m.viewport.GotoTop()
		return nil
	default:
		return m.sendPrompt(text)
	}
}

func (m *model) allCommands() []item {
	all := append([]item(nil), commands...)
	for i := range all {
		all[i].arguments = strings.HasSuffix(all[i].id, " ")
	}
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
			all = append(all, item{title: name, description: "Agent · " + command.Description, id: name, arguments: command.Input != nil})
		}
	}
	return all
}

func (m *model) sendPrompt(text string) tea.Cmd {
	return m.submitPrompt(text, false)
}
