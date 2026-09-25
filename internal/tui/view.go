package tui

import (
	"fmt"
	"path/filepath"
	"strings"
	"unicode"

	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"charm.land/glamour/v2"
	"charm.land/lipgloss/v2"
	"github.com/BrokkAi/acp-go/schema"
	"github.com/BrokkAi/micro-acp/internal/client"
	"github.com/charmbracelet/x/ansi"
)

var (
	plain  = lipgloss.NewStyle()
	accent = lipgloss.NewStyle().Foreground(lipgloss.Color("#BB9AF7"))
	mint   = lipgloss.NewStyle().Foreground(lipgloss.Color("#9ECE6A"))
	muted  = lipgloss.NewStyle().Foreground(lipgloss.Color("#8993AE"))
	danger = lipgloss.NewStyle().Foreground(lipgloss.Color("#F7768E"))
	border = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(lipgloss.Color("#414868")).Padding(0, 1)
)

// Agent output is text, never terminal control sequences (OSC links, clipboard, etc.).
func clean(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) && r != '\n' && r != '\t' {
			return -1
		}
		return r
	}, ansi.Strip(s))
}
func line(s string, width int) string {
	return ansi.Truncate(strings.ReplaceAll(clean(s), "\n", " "), max(1, width), "…")
}

func (m *model) renderTranscript(force bool) {
	if m.client == nil || m.page == "info" || m.authWaiting {
		return
	}
	s, revision := m.client.Snapshot()
	if !force && revision == m.revision {
		return
	}
	m.revision = revision
	atBottom := m.viewport.AtBottom()
	var out strings.Builder
	if len(s.Messages) == 0 {
		out.WriteString("\n" + accent.Bold(true).Render("  A small client. Room for big ideas.") + "\n\n")
		out.WriteString(muted.Render("  Start a conversation in " + filepath.Base(m.options.Cwd) + ".\n  /help opens commands · Ctrl+S opens sessions\n\n  Your agent's tools and permissions appear here."))
	}
	width := max(12, m.viewport.Width()-4)
	for i, message := range s.Messages {
		text := clean(message.Text)
		label := message.Role
		style := muted
		switch message.Role {
		case "user":
			label = "YOU"
			style = accent
		case "assistant":
			label = "AGENT"
			style = mint
		case "thought":
			label = "THINKING"
		case "tool":
			label = "TOOL"
		case "plan":
			label = "PLAN"
		}
		out.WriteString(style.Bold(true).Render(label) + "\n")
		if message.Role == "assistant" {
			cacheKey := fmt.Sprintf("%d:%d:%s", width, i, text)
			rendered, ok := m.renderCache[cacheKey]
			if !ok {
				r, err := glamour.NewTermRenderer(glamour.WithStandardStyle("dark"), glamour.WithWordWrap(width))
				if err == nil {
					rendered, err = r.Render(text)
				}
				if err != nil {
					rendered = ansi.Hardwrap(text, width, true)
				}
				if len(m.renderCache) > 256 {
					m.renderCache = map[string]string{}
				}
				m.renderCache[cacheKey] = rendered
			}
			out.WriteString(strings.TrimSpace(rendered))
		} else {
			out.WriteString(ansi.Hardwrap(text, width, true))
		}
		out.WriteString("\n\n")
	}
	m.viewport.SetContent(out.String())
	if atBottom || force {
		m.viewport.GotoBottom()
	}
}

func (m *model) View() tea.View {
	if m.width < 35 || m.height < 14 {
		v := tea.NewView("micro-acp\nResize the terminal to at least 35 × 14.\nCtrl+C exits.")
		v.AltScreen = true
		return v
	}
	w := m.width - 4
	agent := "choose an agent"
	title := "New conversation"
	sessionID := ""
	if m.client != nil {
		s, _ := m.client.Snapshot()
		agent = m.client.Agent
		if s.Title != "" {
			title = s.Title
		}
		sessionID = s.ID
	}
	header := accent.Bold(true).Render("μ micro-acp") + muted.Render("  /  ") + line(agent, max(8, w-16))
	subtitle := line(m.options.Cwd, w)
	if m.client != nil {
		if state := m.client.Status(); state != "" {
			subtitle = line(state+" · "+m.options.Cwd, w)
		}
	}
	if m.catalog.Warning != "" {
		subtitle = "cached registry · " + line(m.options.Cwd, max(1, w-18))
	}
	header += "\n" + muted.Render(subtitle) + "\n" + muted.Render(strings.Repeat("─", w)) + "\n"
	var body string
	switch {
	case m.permission != nil:
		p := m.permission
		body = accent.Bold(true).Render("Permission requested") + "\n\n"
		if p.Request.ToolCall.Title != nil {
			body += ansi.Hardwrap(clean(*p.Request.ToolCall.Title), w-4, true) + "\n"
		}
		tool := schema.ToolCall{ToolCallID: p.Request.ToolCall.ToolCallID, Content: p.Request.ToolCall.Content, RawInput: p.Request.ToolCall.RawInput, Locations: p.Request.ToolCall.Locations}
		body += ansi.Hardwrap(clean(client.ToolText(tool)), w-4, true) + "\n"
		body += "\n"
		for i, opt := range p.Request.Options {
			prefix := "  "
			if i == m.permissionChoice {
				prefix = "› "
			}
			body += line(prefix+opt.Name+" ("+string(opt.Kind)+")", w) + "\n"
		}
		prefix := "  "
		if m.permissionChoice == len(p.Request.Options) {
			prefix = "› "
		}
		body += prefix + "Cancel\n\n" + muted.Render("↑/↓ choose · Enter confirm · Esc cancel")
	case m.elicitation != nil:
		body = m.elicitationView()
	case m.confirm != nil:
		action := "Delete this session from the agent and local history?"
		if m.localDelete {
			action = "Forget this session locally? The agent keeps its copy."
		}
		body = danger.Bold(true).Render("Delete session") + "\n\n" + ansi.Hardwrap(action, w, true) + "\n\n" + line(m.confirm.Title, w) + "\n" + muted.Render(m.confirm.ID) + "\n\n" + muted.Render("y confirm · n / Esc keep session")
	case m.page == "agents":
		body = accent.Bold(true).Render("AGENTS") + muted.Render("  Search with / · Enter connect") + "\n" + m.picker.View()
	case m.page == "auth":
		body = accent.Bold(true).Render("AUTHENTICATION") + "\n" + m.picker.View()
	case m.page == "authwait":
		body = accent.Bold(true).Render("AUTHENTICATING · agent output") + "\n" + m.viewport.View()
	case m.page == "sessions":
		body = accent.Bold(true).Render("SESSIONS") + muted.Render("  "+line(agent, w-12)) + "\n" + m.picker.View()
	case m.page == "commands":
		body = accent.Bold(true).Render("COMMANDS") + "\n" + m.picker.View()
	case m.page == "settings":
		body = accent.Bold(true).Render("SESSION SETTINGS") + "\n" + m.picker.View()
	case m.page == "choices":
		body = accent.Bold(true).Render(clean(m.selector.Name)) + "\n" + m.picker.View()
	case m.page == "files":
		body = accent.Bold(true).Render("ATTACH FILE") + "\n" + m.picker.View()
	case m.page == "info":
		body = accent.Bold(true).Render("AGENT & SESSION") + muted.Render("  Esc back") + "\n" + m.viewport.View()
	default:
		body = plain.Bold(true).Render(line(title, w-20)) + muted.Render("  "+sessionID) + "\n" + m.viewport.View()
	}
	if m.permission != nil || m.elicitation != nil {
		v := viewport.New(viewport.WithWidth(w), viewport.WithHeight(max(3, m.height-12)))
		v.SetContent(body)
		v.SetYOffset(m.interactionOffset)
		body = v.View()
	}
	body = lipgloss.NewStyle().Width(w).MaxWidth(w).Height(max(3, m.height-12)).MaxHeight(max(3, m.height-12)).Render(body)
	status := m.status
	if n := len(m.attachments) + len(m.resources); n > 0 {
		status = fmt.Sprintf("%d attachments · %s", n, status)
	}
	if m.lastError != "" {
		status = m.lastError
	}
	status = line(status, w-3)
	if m.lastError != "" {
		status = danger.Render(status)
	} else {
		status = muted.Render(status)
	}
	if m.busy {
		status = m.spinner.View() + " " + status
	} else {
		status = mint.Render("• ") + status
	}
	prompt := border.Width(m.width - 4).Render(m.input.View())
	foot := muted.Render(line("Enter send · Alt+Enter newline · Ctrl+P commands · Esc stop · Ctrl+C quit", w))
	if m.permission != nil || m.elicitation != nil {
		foot = muted.Render(line("PgUp/PgDn review details · Esc cancel · Ctrl+D decline form", w))
	}
	content := lipgloss.NewStyle().Padding(0, 2).Render(header+body+"\n"+status) + "\n" + lipgloss.NewStyle().Padding(0, 1).Render(prompt) + "\n" + lipgloss.NewStyle().Padding(0, 2).Render(foot)
	v := tea.NewView(content)
	v.AltScreen = true
	v.MouseMode = tea.MouseModeCellMotion
	v.WindowTitle = "micro-acp"
	return v
}

func (m *model) scrollInteraction(key string) {
	if key == "pgup" {
		m.interactionOffset = max(0, m.interactionOffset-max(3, m.height-14))
	} else {
		m.interactionOffset += max(3, m.height-14)
	}
}
