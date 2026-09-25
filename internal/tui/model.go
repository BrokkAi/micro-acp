package tui

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/list"
	"charm.land/bubbles/v2/spinner"
	"charm.land/bubbles/v2/textarea"
	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"github.com/BrokkAi/acp-go/schema"
	"github.com/BrokkAi/micro-acp/internal/client"
	"github.com/BrokkAi/micro-acp/internal/config"
	"github.com/BrokkAi/micro-acp/internal/registry"
	"github.com/BrokkAi/micro-acp/internal/store"
)

type Options struct {
	Config     config.Config
	Paths      config.Paths
	Cwd, Agent string
	Command    *config.Command
	Resume     *store.Session
	Offline    bool
}

type item struct {
	title, description, id string
	value                  any
}

func (i item) Title() string       { return clean(i.title) }
func (i item) Description() string { return clean(i.description) }
func (i item) FilterValue() string { return i.title + " " + i.id + " " + i.description }

type catalogMsg struct {
	snapshot registry.Snapshot
	err      error
}
type connectedMsg struct {
	client *client.Client
	err    error
}
type resultMsg struct {
	status string
	err    error
}
type sessionsMsg struct {
	sessions []store.Session
	err      error
}
type permissionMsg struct{ permission client.Permission }
type pulseMsg time.Time

type model struct {
	ctx              context.Context
	options          Options
	registry         registry.Client
	store            store.Store
	client           *client.Client
	catalog          registry.Snapshot
	agents           []list.Item
	page             string
	picker           list.Model
	input            textarea.Model
	viewport         viewport.Model
	spinner          spinner.Model
	width, height    int
	busy             bool
	prompting        bool
	status           string
	lastError        string
	permission       *client.Permission
	permissionQueue  []client.Permission
	permissionChoice int
	confirm          *store.Session
	localDelete      bool
	revision         uint64
	renderCache      map[string]string
	info             string
	started          bool
}

func Run(ctx context.Context, options Options) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	m := newModel(ctx, options)
	_, err := tea.NewProgram(m, tea.WithContext(ctx)).Run()
	if m.client != nil {
		if saveErr := m.client.Shutdown(); err == nil {
			err = saveErr
		}
	}
	return err
}
func newModel(ctx context.Context, options Options) *model {
	input := textarea.New()
	input.Placeholder = "Ask anything, or / for commands…"
	input.Prompt = "› "
	input.ShowLineNumbers = false
	input.CharLimit = 128 * 1024
	input.SetHeight(3)
	input.SetWidth(76)
	input.KeyMap.InsertNewline = key.NewBinding(key.WithKeys("shift+enter", "alt+enter", "ctrl+j"))
	styles := input.Styles()
	styles.Focused.CursorLine = plain
	styles.Focused.Prompt = accent
	styles.Focused.Placeholder = muted
	input.SetStyles(styles)
	s := spinner.New()
	s.Spinner = spinner.Dot
	s.Style = accent
	picker := list.New(nil, list.NewDefaultDelegate(), 76, 18)
	picker.SetShowTitle(false)
	picker.SetShowHelp(true)
	picker.DisableQuitKeybindings()
	return &model{ctx: ctx, options: options, registry: registry.Client{URL: options.Config.RegistryURL, Cache: options.Paths.Cache}, store: store.Store{Directory: options.Paths.Data}, page: "agents", input: input, viewport: viewport.New(viewport.WithWidth(76), viewport.WithHeight(18)), spinner: s, picker: picker, width: 80, height: 30, busy: true, status: "Refreshing the ACP registry…", renderCache: map[string]string{}}
}
func (m *model) Init() tea.Cmd {
	return tea.Batch(m.fetchCatalog(), m.spinner.Tick, pulse(), m.input.Focus())
}
func pulse() tea.Cmd {
	return tea.Tick(100*time.Millisecond, func(t time.Time) tea.Msg { return pulseMsg(t) })
}
func (m *model) fetchCatalog() tea.Cmd {
	r, offline, ctx := m.registry, m.options.Offline, m.ctx
	return func() tea.Msg { s, e := r.Load(ctx, offline); return catalogMsg{s, e} }
}
func (m *model) waitPermission() tea.Cmd {
	c, ctx := m.client, m.ctx
	return func() tea.Msg {
		select {
		case p := <-c.Permissions:
			return permissionMsg{p}
		case <-ctx.Done():
			return nil
		}
	}
}
func (m *model) openPicker(page string, items []list.Item) {
	m.page = page
	m.picker.ResetFilter()
	m.picker.SetItems(items)
	m.picker.Select(0)
}
func (m *model) connect(selected item) tea.Cmd {
	m.busy = true
	m.lastError = ""
	m.status = "Starting " + selected.title + "…"
	m.page = "chat"
	ctx, cwd, r, st := m.ctx, m.options.Cwd, m.registry, m.store
	old := m.client
	m.client = nil
	m.permission = nil
	m.permissionQueue = nil
	return func() tea.Msg {
		if old != nil {
			old.Close()
		}
		var command config.Command
		switch v := selected.value.(type) {
		case config.Command:
			command = v
		case registry.Agent:
			var err error
			command, err = r.Resolve(ctx, v)
			if err != nil {
				return connectedMsg{err: err}
			}
		}
		c, err := client.Open(ctx, selected.id, cwd, command, st)
		return connectedMsg{c, err}
	}
}
func (m *model) perform(status string, fn func() error) tea.Cmd {
	m.busy = true
	m.status = status
	m.lastError = ""
	return func() tea.Msg { return resultMsg{status: "Ready", err: fn()} }
}

func (m *model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.input.SetWidth(max(10, m.width-6))
		m.picker.SetSize(max(10, m.width-4), max(4, m.height-10))
		m.viewport.SetWidth(max(10, m.width-4))
		m.viewport.SetHeight(max(3, m.height-13))
		m.renderCache = map[string]string{}
		m.renderTranscript(true)
	case catalogMsg:
		m.busy = false
		m.catalog = msg.snapshot
		m.agents = nil
		names := make([]string, 0, len(m.options.Config.Agents))
		for name := range m.options.Config.Agents {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			cmd := m.options.Config.Agents[name]
			m.agents = append(m.agents, item{title: name, description: "Custom · " + cmd.Command, id: name, value: cmd})
		}
		for _, a := range msg.snapshot.Index.Agents {
			if _, custom := m.options.Config.Agents[a.ID]; !custom {
				m.agents = append(m.agents, item{title: a.Name, description: a.Version + " · " + a.Kind() + " · " + a.Description, id: a.ID, value: a})
			}
		}
		if m.options.Command != nil {
			m.agents = append([]list.Item{item{title: m.options.Agent, description: "Custom command", id: m.options.Agent, value: *m.options.Command}}, m.agents...)
		}
		m.status = fmt.Sprintf("%d agents · registry checked %s", len(m.agents), msg.snapshot.FetchedAt.Local().Format("15:04"))
		if msg.snapshot.Warning != "" {
			m.status = msg.snapshot.Warning
		}
		if msg.err != nil {
			m.lastError = msg.err.Error()
			m.status = "Registry unavailable · custom agents are still available"
		}
		if !m.started {
			m.started = true
			if m.options.Agent != "" {
				for _, entry := range m.agents {
					i := entry.(item)
					if i.id == m.options.Agent {
						return m, m.connect(i)
					}
				}
				m.lastError = "Unknown agent: " + m.options.Agent
			}
		}
		if m.page == "agents" {
			m.openPicker("agents", m.agents)
		}
	case connectedMsg:
		m.busy = false
		if msg.err != nil {
			m.lastError = msg.err.Error()
			m.status = "Connection failed · /agents to choose again"
			return m, nil
		}
		m.client = msg.client
		m.revision = 0
		m.renderCache = map[string]string{}
		m.page = "chat"
		c := m.client
		var cmd tea.Cmd
		if m.options.Resume != nil {
			s := *m.options.Resume
			m.options.Resume = nil
			cmd = m.perform("Loading session…", func() error { return c.Load(s) })
		} else {
			cmd = m.perform("Creating session…", c.New)
		}
		return m, tea.Batch(cmd, m.waitPermission())
	case resultMsg:
		m.busy = false
		m.prompting = false
		m.status = msg.status
		if msg.err != nil {
			m.lastError = msg.err.Error()
			m.status = "Action failed"
		} else {
			m.lastError = ""
		}
		m.renderTranscript(true)
	case sessionsMsg:
		m.busy = false
		var items []list.Item
		for _, s := range msg.sessions {
			items = append(items, item{title: s.Title, description: s.ID + " · " + s.UpdatedAt.Local().Format("Jan 02 15:04"), id: s.ID, value: s})
		}
		m.openPicker("sessions", items)
		m.status = "Enter load · Ctrl+D delete · Esc back"
		if msg.err != nil {
			m.lastError = msg.err.Error()
		}
	case permissionMsg:
		m.permissionQueue = append(m.permissionQueue, msg.permission)
		m.nextPermission()
		return m, m.waitPermission()
	case pulseMsg:
		if m.permission != nil {
			select {
			case <-m.permission.Done:
				m.permission = nil
				m.nextPermission()
			default:
			}
		}
		m.renderTranscript(false)
		return m, pulse()
	case spinner.TickMsg:
		var cmd tea.Cmd
		m.spinner, cmd = m.spinner.Update(msg)
		return m, cmd
	case tea.KeyPressMsg:
		k := msg.String()
		if k == "ctrl+c" {
			if m.prompting && m.client != nil {
				c := m.client
				m.status = "Cancelling…"
				return m, func() tea.Msg {
					if err := c.Cancel(); err != nil {
						return resultMsg{err: err}
					}
					return nil
				}
			}
			return m, tea.Quit
		}
		if m.permission != nil {
			return m, m.permissionKey(k)
		}
		if m.confirm != nil {
			if k == "esc" || k == "n" {
				m.confirm = nil
				return m, nil
			}
			if k == "y" {
				s, local, c := *m.confirm, m.localDelete, m.client
				m.confirm = nil
				m.page = "chat"
				return m, m.perform("Deleting session…", func() error { return c.Delete(s, local) })
			}
			return m, nil
		}
		if k == "esc" {
			if m.prompting && m.client != nil {
				c := m.client
				m.status = "Cancelling…"
				return m, func() tea.Msg { _ = c.Cancel(); return nil }
			}
			if m.page != "chat" && m.picker.FilterState() != list.Filtering {
				m.page = "chat"
				return m, nil
			}
		}
		if !m.busy {
			switch k {
			case "ctrl+g":
				m.openPicker("agents", m.agents)
				return m, nil
			case "ctrl+s":
				return m, m.command("/sessions")
			case "ctrl+n":
				return m, m.command("/new")
			case "ctrl+p":
				m.openCommands()
				return m, nil
			}
		}
		if m.page == "info" {
			if k == "esc" || k == "enter" {
				m.page = "chat"
				m.renderTranscript(true)
				return m, nil
			}
			var cmd tea.Cmd
			m.viewport, cmd = m.viewport.Update(msg)
			return m, cmd
		}
		if m.page == "agents" || m.page == "sessions" || m.page == "commands" {
			if m.busy {
				return m, nil
			}
			if k == "enter" && m.picker.FilterState() != list.Filtering {
				if selected, ok := m.picker.SelectedItem().(item); ok {
					switch m.page {
					case "agents":
						return m, m.connect(selected)
					case "sessions":
						s, c := selected.value.(store.Session), m.client
						m.page = "chat"
						return m, m.perform("Loading session…", func() error { return c.Load(s) })
					case "commands":
						m.page = "chat"
						m.input.SetValue(selected.id)
						m.input.CursorEnd()
						return m, nil
					}
				}
			}
			if k == "ctrl+d" && m.page == "sessions" {
				if selected, ok := m.picker.SelectedItem().(item); ok {
					s := selected.value.(store.Session)
					m.confirm = &s
					m.localDelete = !m.client.CanDelete()
				}
				return m, nil
			}
			var cmd tea.Cmd
			m.picker, cmd = m.picker.Update(msg)
			return m, cmd
		}
		if k == "pgup" || k == "pgdown" || k == "ctrl+up" || k == "ctrl+down" {
			var cmd tea.Cmd
			m.viewport, cmd = m.viewport.Update(msg)
			return m, cmd
		}
		if k == "tab" && strings.HasPrefix(m.input.Value(), "/") {
			for _, c := range commands {
				if strings.HasPrefix(c.id, m.input.Value()) {
					m.input.SetValue(c.id)
					m.input.CursorEnd()
					break
				}
			}
			return m, nil
		}
		if k == "enter" && !m.busy {
			text := strings.TrimSpace(m.input.Value())
			if text == "" {
				return m, nil
			}
			m.input.Reset()
			if strings.HasPrefix(text, "/") {
				return m, m.command(text)
			}
			if m.client == nil {
				m.lastError = "Choose an agent with /agents first"
				return m, nil
			}
			c := m.client
			m.busy = true
			m.lastError = ""
			m.status = "Working…"
			m.prompting = true
			m.viewport.GotoBottom()
			return m, func() tea.Msg {
				reason, err := c.Prompt(text)
				return resultMsg{status: "Turn finished · " + string(reason), err: err}
			}
		}
	}
	if m.page == "chat" {
		var cmd tea.Cmd
		switch msg.(type) {
		case tea.MouseWheelMsg:
			m.viewport, cmd = m.viewport.Update(msg)
		default:
			m.input, cmd = m.input.Update(msg)
		}
		return m, cmd
	}
	return m, nil
}

func (m *model) nextPermission() {
	for m.permission == nil && len(m.permissionQueue) > 0 {
		p := m.permissionQueue[0]
		m.permissionQueue = m.permissionQueue[1:]
		select {
		case <-p.Done:
			continue
		default:
		}
		m.permission = &p
		m.permissionChoice = len(p.Request.Options) // Default to cancel, never approval.
	}
}
func (m *model) permissionKey(k string) tea.Cmd {
	p := m.permission
	switch k {
	case "up", "k":
		m.permissionChoice = max(0, m.permissionChoice-1)
	case "down", "j":
		m.permissionChoice = min(len(p.Request.Options), m.permissionChoice+1)
	case "esc":
		m.permissionChoice = len(p.Request.Options)
		fallthrough
	case "enter":
		outcome := schema.RequestPermissionOutcome{Cancelled: &schema.RequestPermissionOutcomeCancelled{}}
		if m.permissionChoice < len(p.Request.Options) {
			outcome = schema.RequestPermissionOutcome{Selected: &schema.SelectedPermissionOutcome{OptionID: p.Request.Options[m.permissionChoice].OptionID}}
		}
		p.Reply <- outcome
		m.permission = nil
		m.nextPermission()
	}
	return nil
}
