package tui

import (
	"fmt"
	"strings"
	"time"
	"unicode"

	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
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
)

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
func cropLines(s string, height int, tail bool) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if height <= 1 {
		if tail {
			return lines[len(lines)-1]
		}
		return lines[0]
	}
	if len(lines) <= height {
		return strings.Join(lines, "\n")
	}
	if tail {
		return muted.Render("  … Ctrl+O for full output") + "\n" + strings.Join(lines[len(lines)-max(1, height-1):], "\n")
	}
	return strings.Join(lines[:max(1, height-1)], "\n") + "\n" + muted.Render("… /logs for details")
}
func suggestionRows(entries []item, index, width, limit int, commands bool) string {
	if len(entries) == 0 {
		return muted.Render("  No matches")
	}
	start := max(0, index-limit/2)
	start = min(start, max(0, len(entries)-limit))
	end := min(len(entries), start+limit)
	var rows []string
	labelWidth := min(24, max(12, width/3))
	for i := start; i < end; i++ {
		e := entries[i]
		label := e.title
		if commands {
			label = e.id
		}
		description := e.description
		prefix := "  "
		style := plain
		if i == index {
			prefix = "› "
			style = accent.Bold(true)
		}
		if description == "" {
			label = line(label, width-2)
		} else {
			label = line(label, labelWidth)
		}
		row := prefix + label
		if description != "" && width > 42 {
			row += strings.Repeat(" ", max(1, labelWidth-ansi.StringWidth(label)+2)) + line(description, max(1, width-labelWidth-4))
		} else if width <= 42 {
			row = prefix + line(label, width-2)
		}
		rows = append(rows, style.Render(row))
	}
	if len(entries) > limit {
		rows = append(rows, muted.Render(fmt.Sprintf("  %d/%d", index+1, len(entries))))
	}
	return strings.Join(rows, "\n")
}
func (m *model) completionView(height int) string {
	c := m.completion
	if c == nil {
		return ""
	}
	w := max(10, m.width-4)
	label := "Commands"
	if c.kind == "files" {
		label = "Files"
	}
	if c.kind == "values" {
		label = "Options"
	}
	if c.title != "" {
		label = c.title
	}
	body := muted.Render(line(label, w)) + "\n"
	if c.kind == "files" && m.filesLoading {
		body += muted.Render("  Finding workspace files…")
	} else if c.kind == "files" && m.filesError != "" {
		body += danger.Render(line(m.filesError, w))
	} else {
		body += suggestionRows(c.entries, c.index, w, min(5, max(1, height-3)), c.kind == "commands")
	}
	return body + "\n" + muted.Render(line("↑↓ navigate · Tab complete · Enter select · Esc dismiss", w))
}
func (m *model) pickerView(height int) string {
	p := m.picker
	w := max(10, m.width-4)
	title := p.title
	if p.kind == "agents" && m.catalogLoading {
		title += " · updating registry"
	}
	result := accent.Bold(true).Render(line(title, w)) + "\n" + p.input.View() + "\n" + suggestionRows(p.matches, p.index, w, min(5, max(1, height-4)), p.kind == "commands")
	hint := "↑↓ navigate · Enter select · Esc back"
	if p.kind == "sessions" {
		hint += " · Ctrl+D delete"
	}
	return result + "\n" + muted.Render(line(hint, w))
}
func (m *model) permissionView(height int) string {
	p := m.permission
	w := max(10, m.width-4)
	title := "Allow this action?"
	if p.Request.ToolCall.Title != nil {
		title = *p.Request.ToolCall.Title
	}
	tool := schema.ToolCall{ToolCallID: p.Request.ToolCall.ToolCallID, Content: p.Request.ToolCall.Content, RawInput: p.Request.ToolCall.RawInput, Locations: p.Request.ToolCall.Locations}
	details := ansi.Hardwrap(clean(client.ToolText(tool)), w, true)
	var choices []item
	for _, o := range p.Request.Options {
		choices = append(choices, item{title: o.Name})
	}
	choices = append(choices, item{title: "Cancel"})
	options := suggestionRows(choices, m.permissionChoice, w, min(len(choices), max(1, height-5)), false)
	v := viewport.New(viewport.WithWidth(w), viewport.WithHeight(min(max(1, lipgloss.Height(details)), 6, max(1, height-lipgloss.Height(options)-2))))
	v.SetContent(details)
	v.SetYOffset(m.interactionOffset)
	body := accent.Bold(true).Render(line(title, w)) + "\n" + v.View() + "\n"
	body += options + "\n" + muted.Render(line("↑↓ choose · Enter confirm · Esc cancel · PgUp/PgDn details", w))
	return body
}
func (m *model) View() tea.View {
	if m.quitting {
		return tea.NewView("")
	}
	w := max(10, m.width-4)
	modal := m.picker != nil || m.permission != nil || m.elicitation != nil || m.confirm != nil || m.page != "chat" || m.authWaiting
	inputView := m.input.View()
	if modal {
		value := m.input.Value()
		if value == "" {
			value = m.input.Placeholder
		}
		inputView = muted.Render(line("❯ "+value, w))
	}
	var statusParts []string
	if m.busy && !modal {
		elapsed := time.Since(m.startedAt).Round(time.Second)
		statusParts = append(statusParts, m.spinner.View()+" "+muted.Render(line(m.status+" · "+elapsed.String()+" · Esc stop", w-2)))
	}
	if !m.busy && m.status != "" && m.status != "Ready" && m.lastError == "" {
		statusParts = append(statusParts, muted.Render(line(m.status, w)))
	}
	if m.lastError != "" {
		statusParts = append(statusParts, danger.Render(cropLines(ansi.Hardwrap(clean(m.lastError), w, true), 3, false)))
	}
	var badges []string
	if !modal {
		if n := len(m.attachments) + len(m.resources); n > 0 {
			badges = append(badges, muted.Render(fmt.Sprintf("%d attachment(s) · /detach to clear", n)))
		}
		if len(m.queued) > 0 {
			hint := fmt.Sprintf("%d queued · /queue to edit", len(m.queued))
			if m.queuePaused {
				hint += " · /queue send to continue"
			}
			badges = append(badges, muted.Render(line(hint, w)))
		}
	}
	footer := "/ commands · @ files · Ctrl+O details · Alt+Enter newline"
	if m.prompting && m.input.Value() != "" {
		footer = "Enter queue prompt · Esc stop · Alt+Enter newline"
	}
	footer = muted.Render(line(footer, w))
	if m.client != nil {
		footer = muted.Render(configurationStatus(m.client.Agent, m.client.StatusFields(), w)) + "\n" + footer
	}
	baseHeight := lipgloss.Height(inputView) + 2 + lipgloss.Height(footer)
	for _, part := range append(append([]string{}, statusParts...), badges...) {
		baseHeight += lipgloss.Height(part)
	}
	panelHeight := max(1, m.height-baseHeight-1)
	var panel string
	switch {
	case m.permission != nil:
		panel = m.permissionView(panelHeight)
	case m.elicitation != nil:
		panel = m.elicitationPanel(panelHeight)
	case m.confirm != nil:
		action := "Delete this session from the agent and local history?"
		if m.localDelete {
			action = "Remove this session from local history?"
		}
		panel = danger.Render(ansi.Hardwrap(action, w, true)) + "\n" + line(m.confirm.Title, w) + "\n" + muted.Render("y delete · n / Esc keep")
	case m.page == "details" || m.page == "info" || m.authWaiting:
		title := "Details"
		if m.authWaiting {
			title = "Signing in"
		}
		v := m.viewport
		bottom := v.AtBottom()
		v.SetHeight(max(1, panelHeight-2))
		if bottom {
			v.GotoBottom()
		}
		panel = accent.Render(title) + "\n" + v.View() + "\n" + muted.Render("PgUp/PgDn scroll · Esc back")
	case m.picker != nil:
		panel = m.pickerView(panelHeight)
	case m.completion != nil:
		panel = m.completionView(panelHeight)
	}
	var parts []string
	// The only managed transcript is the currently streaming tail. Completed
	// output is printed above the UI, preserving native scrollback and selection.
	overhead := baseHeight
	if panel != "" {
		overhead += lipgloss.Height(panel)
	}
	if m.live != "" && !modal && m.height > overhead {
		available := m.height - overhead
		parts = append(parts, cropLines(m.live, available, true))
	}
	parts = append(parts, statusParts...)
	if panel != "" {
		parts = append(parts, panel)
	}
	parts = append(parts, badges...)
	parts = append(parts, muted.Render(strings.Repeat("─", w)))
	before := strings.Join(parts, "\n")
	inputY := 0
	if before != "" {
		inputY = lipgloss.Height(before)
	}
	parts = append(parts, inputView, muted.Render(strings.Repeat("─", w)))
	parts = append(parts, footer)
	content := strings.Join(parts, "\n")
	v := tea.NewView(lipgloss.NewStyle().PaddingLeft(1).Render(content))
	// Do not capture mouse events: copying and terminal scrollback should work.
	if !modal {
		v.Cursor = m.input.Cursor()
		if v.Cursor != nil {
			v.Cursor.X++
			v.Cursor.Y += inputY
		}
	}
	return v
}
