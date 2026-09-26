package tui

import (
	"context"
	"sort"
	"strings"
	"time"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/spinner"
	"charm.land/bubbles/v2/textarea"
	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	acp "github.com/BrokkAi/acp-go"
	"github.com/BrokkAi/acp-go/schema"
	"github.com/BrokkAi/micro-acp/internal/client"
	"github.com/BrokkAi/micro-acp/internal/config"
	"github.com/BrokkAi/micro-acp/internal/registry"
	"github.com/BrokkAi/micro-acp/internal/store"
	"github.com/charmbracelet/x/ansi"
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
	arguments              bool
}
type catalogMsg struct {
	snapshot registry.Snapshot
	err      error
}
type connectedMsg struct {
	client *client.Client
	err    error
}
type disconnectedMsg struct{ client *client.Client }
type resultMsg struct {
	status string
	err    error
	reason schema.StopReason
	prompt bool
}
type sessionsMsg struct {
	sessions []store.Session
	err      error
}
type permissionMsg struct{ permission client.Permission }
type pulseMsg time.Time
type printedMsg struct{}
type queuedPrompt struct {
	id          uint64
	text        string
	blocks      []acp.Content
	attachments []string
	resources   []acp.Content
	sessionID   string
}

type model struct {
	theme                      palette
	ctx                        context.Context
	options                    Options
	registry                   registry.Client
	store                      store.Store
	client                     *client.Client
	catalog                    registry.Snapshot
	agents                     []item
	page                       string
	picker                     *picker
	completion                 *completion
	dismissedCompletion        string
	files                      []string
	filesLoading, filesLoaded  bool
	filesError                 string
	input                      textarea.Model
	viewport                   viewport.Model
	spinner                    spinner.Model
	width, height              int
	busy, prompting            bool
	status, lastError          string
	startedAt                  time.Time
	permission                 *client.Permission
	permissionQueue            []client.Permission
	permissionChoice           int
	confirm                    *store.Session
	localDelete                bool
	info                       string
	started, catalogLoading    bool
	selector                   client.Selector
	history                    []string
	historyIndex               int
	draft                      string
	attachments                []string
	resources                  []acp.Content
	elicitation                *elicitationUI
	elicitationQueue           []client.Elicitation
	interactionOffset          int
	retryOperation             func() error
	authWaiting                bool
	interactions               client.Interactions
	resumeTarget               *store.Session
	queued                     []queuedPrompt
	queueSequence              uint64
	queuePaused                bool
	printedSession             string
	printedIndex, streamPrefix int
	live                       string
	renderCache                map[string]string
	printQueue                 []string
	printing, quitting         bool
	exitArmed                  time.Time
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
	p := newPalette(true)
	input := textarea.New()
	input.Placeholder = "Ask anything…  / commands · @ files"
	input.Prompt = "❯ "
	input.ShowLineNumbers = false
	input.CharLimit = 128 * 1024
	input.DynamicHeight = true
	input.MinHeight = 1
	input.MaxHeight = 8
	input.MaxContentHeight = 10000
	input.SetHeight(1)
	input.SetWidth(76)
	input.SetVirtualCursor(false)
	input.KeyMap.InsertNewline = key.NewBinding(key.WithKeys("shift+enter", "alt+enter", "ctrl+j"))
	styles := input.Styles()
	styles.Focused.CursorLine = plain
	styles.Focused.Prompt = p.accent
	styles.Focused.Placeholder = p.muted
	input.SetStyles(styles)
	s := spinner.New()
	s.Spinner = spinner.Dot
	s.Style = p.accent
	m := &model{ctx: ctx, options: options, registry: registry.Client{URL: options.Config.RegistryURL, Cache: options.Paths.Cache}, store: store.Store{Directory: options.Paths.Data}, page: "chat", input: input, viewport: viewport.New(viewport.WithWidth(76), viewport.WithHeight(12)), spinner: s, width: 80, height: 30, renderCache: map[string]string{}, interactions: client.Interactions{Permissions: make(chan client.Permission, 32), Elicitations: make(chan client.Elicitation, 32)}}
	m.rebuildAgents()
	m.theme = p
	return m
}
func (m *model) Init() tea.Cmd {
	m.queueOutput(m.theme.accent.Bold(true).Render("micro-acp") + m.theme.muted.Render("  ·  "+clean(m.options.Cwd)) + "\n")
	cmds := []tea.Cmd{m.spinner.Tick, pulse(), m.input.Focus(), m.waitPermission(), m.waitElicitation()}
	if m.options.Agent != "demo" {
		m.catalogLoading = true
		cmds = append(cmds, m.fetchCatalog())
	}
	if m.options.Agent != "" {
		for _, entry := range m.agents {
			if entry.id == m.options.Agent {
				m.started = true
				cmds = append(cmds, m.connect(entry))
				break
			}
		}
	} else {
		m.openPicker("agents", m.agents)
	}
	cmds = append(cmds, m.flushOutput())
	return tea.Batch(cmds...)
}
func pulse() tea.Cmd {
	return tea.Tick(100*time.Millisecond, func(t time.Time) tea.Msg { return pulseMsg(t) })
}
func (m *model) fetchCatalog() tea.Cmd {
	r, offline, ctx := m.registry, m.options.Offline, m.ctx
	return func() tea.Msg { s, e := r.Load(ctx, offline); return catalogMsg{s, e} }
}
func (m *model) rebuildAgents() {
	m.agents = nil
	names := make([]string, 0, len(m.options.Config.Agents))
	for name := range m.options.Config.Agents {
		names = append(names, name)
	}
	sort.Strings(names)
	if m.options.Command != nil {
		m.agents = append(m.agents, item{title: m.options.Agent, description: "Custom command", id: m.options.Agent, value: *m.options.Command})
	}
	for _, name := range names {
		cmd := m.options.Config.Agents[name]
		m.agents = append(m.agents, item{title: name, description: cmd.Command, id: name, value: cmd})
	}
	for _, a := range m.catalog.Index.Agents {
		if _, custom := m.options.Config.Agents[a.ID]; !custom {
			m.agents = append(m.agents, item{title: a.Name, description: a.Description, id: a.ID, value: a})
		}
	}
}
func (m *model) waitPermission() tea.Cmd {
	requests, ctx := m.interactions.Permissions, m.ctx
	return func() tea.Msg {
		select {
		case p := <-requests:
			return permissionMsg{p}
		case <-ctx.Done():
			return nil
		}
	}
}
func (m *model) connect(selected item) tea.Cmd {
	m.busy = true
	m.startedAt = time.Now()
	m.lastError = ""
	m.status = "Starting " + selected.title
	m.page = "chat"
	m.picker = nil
	m.completion = nil
	ctx, cwd, r, st, settings, interactions := m.ctx, m.options.Cwd, m.registry, m.store, m.options.Config.Session, m.interactions
	old := m.client
	m.client = nil
	m.live = ""
	m.permission = nil
	m.permissionQueue = nil
	m.elicitation = nil
	m.elicitationQueue = nil
	m.retryOperation = nil
	return func() tea.Msg {
		if old != nil {
			if err := old.Shutdown(); err != nil {
				return connectedMsg{err: err}
			}
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
		c, err := client.OpenInteractive(ctx, selected.id, cwd, command, st, interactions, settings)
		return connectedMsg{c, err}
	}
}
func (m *model) perform(status string, fn func() error) tea.Cmd {
	m.busy = true
	m.startedAt = time.Now()
	m.status = status
	m.lastError = ""
	m.retryOperation = fn
	return func() tea.Msg { return resultMsg{status: "Ready", err: fn()} }
}
func (m *model) quit() tea.Cmd { m.quitting = true; return nil }
func (m *model) Update(msg tea.Msg) (updated tea.Model, cmd tea.Cmd) {
	updated = m
	defer func() {
		m.syncTranscript()
		m.refreshQueuePicker()
		if m.client != nil && m.picker != nil {
			m.refreshSettingPicker(m.client.Selectors())
		}
		completionCmd := m.refreshCompletion()
		outputCmd := m.flushOutput()
		if m.quitting && !m.printing && len(m.printQueue) == 0 {
			cmd = tea.Sequence(cmd, tea.Quit)
			return
		}
		cmd = tea.Batch(cmd, completionCmd, outputCmd)
	}()
	switch msg := msg.(type) {
	case tea.BackgroundColorMsg:
		m.applyTheme(msg.IsDark())
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.input.MaxHeight = min(8, max(1, m.height/3))
		m.input.SetWidth(max(10, m.width-4))
		m.viewport.SetWidth(max(10, m.width-4))
		m.viewport.SetHeight(max(3, m.height-10))
		m.renderCache = map[string]string{}
		if m.picker != nil {
			m.picker.input.SetWidth(max(10, m.width-8))
		}
		if m.elicitation != nil {
			m.elicitation.input.SetWidth(max(10, m.width-8))
		}
	case printedMsg:
		m.printing = false
	case catalogMsg:
		m.catalogLoading = false
		if m.status == "Refreshing registry…" {
			m.status = "Ready"
		}
		m.catalog = msg.snapshot
		m.rebuildAgents()
		if msg.err != nil {
			m.catalog.Warning = msg.err.Error()
			if m.client == nil && !m.busy {
				m.lastError = "Registry unavailable. Use a configured agent, /refresh, or -- /path/to/agent."
			}
		}
		if m.catalog.Warning != "" {
			m.queueOutput(m.theme.muted.Render(ansi.Wrap(clean(m.catalog.Warning), max(10, m.width-4), "")))
		}
		if !m.started && m.options.Agent != "" {
			m.started = true
			for _, entry := range m.agents {
				if entry.id == m.options.Agent {
					return m, m.connect(entry)
				}
			}
			m.lastError = "Unknown agent: " + m.options.Agent
			m.openPicker("agents", m.agents)
		}
		if m.picker != nil && m.picker.kind == "agents" {
			m.picker.entries = m.agents
			m.picker.filter()
		}
	case connectedMsg:
		m.busy = false
		if msg.err != nil {
			m.lastError = msg.err.Error()
			m.status = "Connection failed · /agents to choose another"
			return m, nil
		}
		m.client = msg.client
		m.page = "chat"
		m.printedSession = ""
		m.renderCache = map[string]string{}
		c := m.client
		var action tea.Cmd
		if m.options.Resume != nil {
			s := *m.options.Resume
			m.resumeTarget = &s
			m.options.Resume = nil
			action = m.perform("Resuming session", func() error { return c.Load(s) })
		} else {
			action = m.perform("Creating session", c.New)
		}
		return m, tea.Batch(action, func() tea.Msg { <-c.Done(); return disconnectedMsg{c} })
	case disconnectedMsg:
		if msg.client == m.client {
			m.lastError = "Agent disconnected. /logs shows details; /reconnect restarts it."
			m.status = "Disconnected"
		}
	case resultMsg:
		m.busy = false
		m.prompting = false
		if msg.prompt {
			m.filesLoaded = false
		}
		m.status = msg.status
		if msg.err != nil {
			m.lastError = msg.err.Error()
			m.status = "Action failed"
			m.queuePaused = true
			if acp.IsAuthRequired(msg.err) {
				m.openAuth()
			}
		}
		if msg.err == nil {
			m.lastError = ""
			m.retryOperation = nil
			m.resumeTarget = nil
		}
		if msg.reason == schema.StopReasonCancelled {
			m.status = "Stopped"
			m.queuePaused = true
		}
		m.syncTranscript()
		m.rebuildHistory()
		if msg.prompt && msg.err == nil && !m.queuePaused && len(m.queued) > 0 {
			return m, m.sendQueued()
		}
	case sessionsMsg:
		m.busy = false
		m.status = "Ready"
		var entries []item
		for _, s := range msg.sessions {
			entries = append(entries, item{title: s.Title, description: s.UpdatedAt.Local().Format("Jan 02 15:04") + " · " + s.ID, id: s.ID, value: s})
		}
		m.openPicker("sessions", entries)
		if msg.err != nil {
			m.lastError = msg.err.Error()
		}
	case filesMsg:
		m.filesLoading = false
		m.filesLoaded = true
		m.files = msg.paths
		if msg.err != nil {
			m.filesError = msg.err.Error()
		}
	case permissionMsg:
		m.permissionQueue = append(m.permissionQueue, msg.permission)
		m.nextPermission()
		return m, m.waitPermission()
	case elicitationMsg:
		m.elicitationQueue = append(m.elicitationQueue, msg.event)
		return m, tea.Batch(m.nextElicitation(), m.waitElicitation())
	case authDoneMsg:
		m.authWaiting = false
		m.busy = false
		m.page = "chat"
		if msg.err != nil {
			m.lastError = msg.err.Error()
			return m, nil
		}
		m.status = "Signed in"
		if msg.terminal {
			c := m.client
			if m.resumeTarget != nil {
				m.options.Resume = m.resumeTarget
			} else if s, _ := c.Snapshot(); s.ID != "" && c.CanLoad() {
				m.options.Resume = &s
			}
			return m, m.connect(item{title: c.Agent, id: c.Agent, value: c.Invocation()})
		}
		if m.retryOperation != nil {
			return m, m.perform("Continuing", m.retryOperation)
		}
		if s, _ := m.client.Snapshot(); s.ID == "" {
			return m, m.perform("Creating session", m.client.New)
		}
	case browserMsg:
		if msg.err != nil {
			m.lastError = "Could not open browser: " + msg.err.Error()
		}
	case pulseMsg:
		if m.permission != nil {
			select {
			case <-m.permission.Done:
				m.permission = nil
				m.nextPermission()
			default:
			}
		}
		if m.elicitation != nil {
			select {
			case <-m.elicitation.event.Done:
				m.elicitation = nil
				cmd = m.nextElicitation()
			default:
			}
		}
		if m.authWaiting && m.client != nil {
			m.viewport.SetContent(clean(m.client.Diagnostics()))
			m.viewport.GotoBottom()
		}
		return m, tea.Batch(cmd, pulse())
	case spinner.TickMsg:
		m.spinner, cmd = m.spinner.Update(msg)
		return m, cmd
	case tea.KeyPressMsg:
		return m, m.key(msg)
	}
	if m.permission != nil || m.confirm != nil {
		return m, nil
	}
	if m.elicitation != nil {
		m.elicitation.input, cmd = m.elicitation.input.Update(msg)
		return m, cmd
	}
	if m.picker != nil {
		m.picker.input, cmd = m.picker.input.Update(msg)
		m.picker.filter()
		return m, cmd
	}
	if m.page == "chat" {
		m.input, cmd = m.input.Update(msg)
	}
	return m, cmd
}
func (m *model) key(msg tea.KeyPressMsg) tea.Cmd {
	k := msg.String()
	if k == "ctrl+c" {
		if m.prompting && m.client != nil {
			m.queuePaused = true
			c := m.client
			m.status = "Stopping"
			return func() tea.Msg { _ = c.Cancel(); return nil }
		}
		if m.input.Value() != "" {
			m.input.Reset()
			m.completion = nil
			m.status = "Draft cleared"
			return nil
		}
		if time.Since(m.exitArmed) < 2*time.Second {
			return m.quit()
		}
		m.exitArmed = time.Now()
		m.status = "Press Ctrl+C again to exit"
		return nil
	}
	if m.permission != nil {
		if k == "pgup" || k == "pgdown" {
			m.scrollInteraction(k)
			return nil
		}
		return m.permissionKey(k)
	}
	if m.elicitation != nil {
		if k == "pgup" || k == "pgdown" {
			m.scrollInteraction(k)
			return nil
		}
		return m.elicitationKey(msg)
	}
	if m.confirm != nil {
		if k == "esc" || k == "n" {
			m.confirm = nil
		}
		if k == "y" {
			s, local, c := *m.confirm, m.localDelete, m.client
			m.confirm = nil
			return m.perform("Deleting session", func() error { return c.Delete(s, local) })
		}
		return nil
	}
	if m.picker != nil {
		return m.pickerKey(msg)
	}
	if m.page == "info" || m.page == "details" {
		if k == "esc" || k == "ctrl+o" {
			m.page = "chat"
			return nil
		}
		var cmd tea.Cmd
		m.viewport, cmd = m.viewport.Update(msg)
		return cmd
	}
	if handled, cmd := m.completionKey(msg); handled {
		return cmd
	}
	switch k {
	case "ctrl+d":
		if m.input.Value() == "" && !m.busy {
			return m.quit()
		}
	case "esc":
		if m.prompting && m.client != nil {
			m.queuePaused = true
			c := m.client
			m.status = "Stopping"
			return func() tea.Msg { _ = c.Cancel(); return nil }
		}
	case "ctrl+o":
		return m.command("/details")
	case "ctrl+p":
		m.openCommands()
		return nil
	case "ctrl+g":
		if !m.busy {
			m.openPicker("agents", m.agents)
		}
		return nil
	case "ctrl+s":
		if !m.busy {
			return m.command("/sessions")
		}
		return nil
	case "ctrl+n":
		if !m.busy {
			return m.command("/new")
		}
		return nil
	case "alt+up", "up":
		if (k == "alt+up" || m.input.Line() == 0) && len(m.history) > 0 {
			if m.historyIndex == len(m.history) {
				m.draft = m.input.Value()
			}
			m.historyIndex = max(0, m.historyIndex-1)
			m.input.SetValue(m.history[m.historyIndex])
			m.input.MoveToEnd()
			return nil
		}
	case "alt+down", "down":
		if m.historyIndex < len(m.history) && (k == "alt+down" || m.input.Line() == m.input.LineCount()-1) {
			m.historyIndex++
			if m.historyIndex == len(m.history) {
				m.input.SetValue(m.draft)
			} else {
				m.input.SetValue(m.history[m.historyIndex])
			}
			m.input.MoveToEnd()
			return nil
		}
	case "enter":
		text := strings.TrimSpace(m.input.Value())
		if text == "" && len(m.attachments) == 0 && len(m.resources) == 0 {
			return nil
		}
		if strings.HasPrefix(text, "/") {
			m.input.Reset()
			return m.command(text)
		}
		return m.sendPrompt(text)
	}
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	return cmd
}
func (m *model) rebuildHistory() {
	if m.client == nil {
		return
	}
	s, _ := m.client.Snapshot()
	m.history = nil
	for _, entry := range s.Messages {
		if entry.Role == "user" {
			text := entry.Text
			if len(entry.Content) > 0 && entry.Content[0].Text != nil {
				text = entry.Content[0].Text.Text
			}
			m.history = append(m.history, text)
		}
	}
	m.historyIndex = len(m.history)
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
		m.interactionOffset = 0
		m.permissionChoice = len(p.Request.Options)
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
func (m *model) scrollInteraction(k string) {
	if k == "pgup" {
		m.interactionOffset = max(0, m.interactionOffset-5)
	} else {
		m.interactionOffset += 5
	}
}
func (m *model) queueOutput(s string) {
	if strings.TrimSpace(s) != "" {
		m.printQueue = append(m.printQueue, s)
	}
}
func (m *model) flushOutput() tea.Cmd {
	if m.printing || len(m.printQueue) == 0 {
		return nil
	}
	text := strings.Join(m.printQueue, "\n")
	m.printQueue = nil
	m.printing = true
	return tea.Sequence(tea.Println(text), func() tea.Msg { return printedMsg{} })
}
func (m *model) startPrompt(q queuedPrompt) tea.Cmd {
	c := m.client
	m.busy = true
	m.prompting = true
	m.startedAt = time.Now()
	m.retryOperation = nil
	m.lastError = ""
	m.status = "Working"
	m.draft = ""
	return func() tea.Msg {
		reason, err := c.PromptContent(q.blocks)
		return resultMsg{status: "Ready", err: err, reason: reason, prompt: true}
	}
}

func (m *model) sendQueued() tea.Cmd {
	q := m.queued[0]
	s, _ := m.client.Snapshot()
	if q.sessionID != s.ID {
		m.queuePaused = true
		m.lastError = "Queued prompts belong to another session. Resume it or use /queue to edit them."
		return nil
	}
	m.queued = m.queued[1:]
	m.queuePaused = false
	return m.startPrompt(q)
}
