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
	"github.com/BrokkAi/micro-acp/internal/buildinfo"
	"github.com/BrokkAi/micro-acp/internal/client"
	"github.com/charmbracelet/x/ansi"
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
func (m *model) cropLines(s string, height int, tail bool) string {
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
		hidden := len(lines) - max(1, height-1)
		return m.theme.dim.Render(line(fmt.Sprintf("  … %d earlier lines · ctrl+o details", hidden), max(10, m.lineWidth()))) + "\n" + strings.Join(lines[len(lines)-max(1, height-1):], "\n")
	}
	return strings.Join(lines[:max(1, height-1)], "\n") + "\n" + m.theme.dim.Render("  … /logs for details")
}

// listWindow returns the visible slice of a list that keeps index in view.
func listWindow(total, index, limit int) (start, end int) {
	start = max(0, index-limit/2)
	start = min(start, max(0, total-limit))
	return start, min(total, start+limit)
}

// counter is shown in a panel's title rule when its list scrolls.
func counter(index, total, limit int) string {
	if total <= limit || total == 0 {
		return ""
	}
	return fmt.Sprintf("%d/%d", index+1, total)
}

// suggestionRows renders a list with a pointer column and an aligned
// description column. The focused row is highlighted across its full width;
// arrows in the pointer column show that more entries are out of view.
func (m *model) suggestionRows(entries []item, index, width, limit int, commands bool) string {
	if len(entries) == 0 {
		return m.theme.dim.Render("  No matches")
	}
	start, end := listWindow(len(entries), index, limit)
	name := func(e item) string {
		if commands {
			return e.id
		}
		return e.title
	}
	version := func(e item) string {
		if e.version == "" {
			return ""
		}
		return " (" + clean(e.version) + ")"
	}
	described := false
	labelWidth := 0
	for i := start; i < end; i++ {
		labelWidth = max(labelWidth, ansi.StringWidth(clean(name(entries[i])+version(entries[i]))))
		described = described || entries[i].description != ""
	}
	twoColumns := described && width > 42
	labelWidth = min(labelWidth, max(12, width*2/5))
	var rows []string
	for i := start; i < end; i++ {
		e := entries[i]
		selected := i == index
		limit := width - gutter
		if twoColumns {
			limit = labelWidth
		}
		// Keep the version visible; it distinguishes otherwise equal names.
		suffix := version(e)
		text := line(line(name(e), max(1, limit-ansi.StringWidth(suffix)))+suffix, limit)
		pointer := "  "
		switch {
		case selected:
			pointer = "› "
		case i == start && start > 0:
			pointer = "↑ "
		case i == end-1 && end < len(entries):
			pointer = "↓ "
		}
		description := ""
		if twoColumns && e.description != "" {
			description = line(e.description, max(1, width-gutter-labelWidth-2))
		}
		gap := ""
		if description != "" {
			gap = strings.Repeat(" ", max(1, labelWidth-ansi.StringWidth(text)+2))
		}
		if selected {
			bg := on(m.theme.selectBg)
			used := gutter + ansi.StringWidth(text) + ansi.StringWidth(gap) + ansi.StringWidth(description)
			row := m.theme.selection.Render(pointer+text) + bg.Render(gap) + m.hintKey().Inherit(bg).Render(description)
			rows = append(rows, row+bg.Render(strings.Repeat(" ", max(0, width-used))))
			continue
		}
		rows = append(rows, m.theme.dim.Render(pointer)+text+gap+m.theme.muted.Render(description))
	}
	return strings.Join(rows, "\n")
}
func (m *model) completionView(height int) string {
	c := m.completion
	if c == nil {
		return ""
	}
	w := m.lineWidth()
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
	limit := min(6, max(1, height-2))
	body := ""
	right := ""
	if c.kind == "files" && m.filesLoading {
		body = m.theme.dim.Render("  Finding workspace files…")
	} else if c.kind == "files" && m.filesError != "" {
		body = m.theme.danger.Render(line("  "+m.filesError, w))
	} else {
		body = m.suggestionRows(c.entries, c.index, w, limit, c.kind == "commands")
		right = counter(c.index, len(c.entries), limit)
	}
	return m.titleRule(label, right, w, m.theme.muted) + "\n" + body + "\n" + m.hints(w, "↑↓", "navigate", "tab", "complete", "enter", "select", "esc", "dismiss")
}
func (m *model) pickerView(height int) string {
	p := m.picker
	w := m.lineWidth()
	title := p.title
	if p.kind == "agents" && m.catalogLoading {
		title += " · updating registry"
	}
	limit := min(8, max(1, height-3))
	result := m.titleRule(title, counter(p.index, len(p.matches), limit), w, m.theme.accent.Bold(true)) + "\n" + p.input.View() + "\n" + m.suggestionRows(p.matches, p.index, w, limit, p.kind == "commands")
	verbs := map[string]string{"agents": "connect", "sessions": "resume", "settings": "change", "choices": "choose", "commands": "run", "queue": "edit", "subagents": "open", "auth": "sign in"}
	verb := verbs[p.kind]
	if verb == "" {
		verb = "select"
	}
	hints := []string{"↑↓", "navigate", "enter", verb, "esc", "close"}
	switch p.kind {
	case "sessions":
		hints = append(hints[:4], "ctrl+d", "delete", "esc", "close")
	case "queue":
		hints = append(hints[:4], "ctrl+d", "remove", "esc", "close")
	}
	return result + "\n" + m.hints(w, hints...)
}
func (m *model) permissionView(height int) string {
	p := m.permission
	w := m.lineWidth()
	title := "Allow this action?"
	if p.Request.ToolCall.Title != nil && strings.TrimSpace(*p.Request.ToolCall.Title) != "" {
		title = *p.Request.ToolCall.Title
	}
	heading := "Permission required"
	if p.Subagent != "" {
		heading += " · " + subagentMark + " " + p.Subagent
	}
	meta := ""
	if n := len(m.permissionQueue); n > 0 {
		meta = fmt.Sprintf("%d more waiting", n)
	} else if p.Request.ToolCall.Kind != nil {
		meta = string(*p.Request.ToolCall.Kind)
	}
	// The request itself may wrap onto a second line; it is what is being approved.
	titleLines := strings.Split(ansi.Wrap(clean(title), w-gutter, ""), "\n")
	if len(titleLines) > 2 {
		titleLines = append(titleLines[:1], line(strings.Join(titleLines[1:], " "), w-gutter))
	}
	// Show what would run or change the way the details page does: input
	// fields, output, and diffs with line numbers.
	tool := schema.ToolCall{ToolCallID: p.Request.ToolCall.ToolCallID, Content: p.Request.ToolCall.Content, RawInput: p.Request.ToolCall.RawInput, Locations: p.Request.ToolCall.Locations}
	details := strings.TrimRight(m.toolDetails(tool, w), "\n ")
	var choices []item
	for _, o := range p.Request.Options {
		choices = append(choices, item{title: o.Name})
	}
	choices = append(choices, item{title: "Cancel", description: "esc"})
	options := m.suggestionRows(choices, m.permissionChoice, w, min(len(choices), max(1, height-5)), false)
	body := m.titleRule(heading, meta, w, m.theme.amber.Bold(true)) + "\n" + hang("  ", m.hintKey().Bold(true).Render(strings.Join(titleLines, "\n"))) + "\n"
	reserved := 2 + len(titleLines) + lipgloss.Height(options)
	if strings.TrimSpace(details) != "" {
		v := viewport.New(viewport.WithWidth(w), viewport.WithHeight(min(max(1, lipgloss.Height(details)), 8, max(1, height-reserved-1))))
		v.SetContent(details)
		v.SetYOffset(m.interactionOffset)
		body += v.View() + "\n"
		reserved += v.Height()
	}
	if height > reserved {
		body += "\n"
	}
	return body + options + "\n" + m.hints(w, "↑↓", "choose", "enter", "confirm", "esc", "cancel", "pgup/pgdn", "details")
}

// statusLine shows the turn in progress: what the agent is doing, how long
// it has taken, and how to stop it.
func (m *model) statusLine(w int) string {
	since := time.Since(m.startedAt)
	label := m.status
	if label == "Working" && m.activity != "" {
		label = m.activity
	}
	waiting := m.client != nil && m.client.TurnState() == client.TurnRequiresAction
	if waiting {
		label = "Waiting on you"
	}
	if n := m.runningSubagents(); n == 1 {
		label += " · 1 subagent running"
	} else if n > 1 {
		label += fmt.Sprintf(" · %d subagents running", n)
	}
	trailer := " (" + elapsed(since) + " · esc to interrupt)"
	label = line(label, max(1, w-gutter-ansi.StringWidth(trailer)))
	text := m.shimmer(label, since)
	mark := m.spinner.View()
	if waiting {
		text, mark = m.theme.amber.Render(label), m.theme.amber.Render("●")
	}
	tail := m.theme.dim.Render(" ("+elapsed(since)+" · ") + m.hintKey().Render("esc") + m.theme.dim.Render(" to interrupt)")
	if ansi.StringWidth(label)+gutter+ansi.StringWidth(trailer) > w {
		tail = ""
	}
	return mark + " " + text + tail
}

// footerHints are the few keys that matter in the current state.
func (m *model) footerHints() []string {
	typed := m.input.Value() != ""
	switch {
	case m.editing != nil:
		return []string{"enter", "save", "esc", "restore draft"}
	case m.prompting && typed && m.client != nil && m.client.CanSteer() && !m.queuePaused:
		return []string{"enter", "steer", "tab", "queue", "esc", "stop", "alt+enter", "newline"}
	case m.prompting && typed:
		return []string{"enter", "queue", "esc", "stop", "alt+enter", "newline"}
	case m.prompting:
		return []string{"esc", "stop", "ctrl+o", "details"}
	case typed:
		return []string{"enter", "send", "alt+enter", "newline", "ctrl+o", "details"}
	}
	return []string{"/", "commands", "@", "files", "ctrl+o", "details", "alt+enter", "newline"}
}

// footer puts the session settings on the left and hints on the right, on
// one row when both fit. Panels carry their own hints, so these step aside.
func (m *model) footer(w int, modal bool) string {
	inner := w - gutter
	status := ""
	if m.client != nil {
		status = m.configurationStatus(m.client.Agent, m.client.StatusFields(), inner)
	}
	if modal {
		return hang("  ", status)
	}
	hints := m.hints(inner, m.footerHints()...)
	if status == "" {
		return "  " + hints
	}
	if !strings.Contains(status, "\n") {
		gap := inner - ansi.StringWidth(status) - ansi.StringWidth(hints)
		if gap >= 4 {
			return "  " + status + strings.Repeat(" ", gap) + hints
		}
	}
	return hang("  ", status) + "\n  " + hints
}

// placeholder says what sending will do right now.
func (m *model) placeholder() string {
	switch {
	case m.editing != nil:
		return "Edit the queued prompt…"
	case m.client == nil && !m.busy && m.picker == nil:
		return "Choose an agent with /agents…"
	case m.prompting && m.client != nil && m.client.CanSteer() && !m.queuePaused:
		return "Steer or queue a follow-up…"
	case m.prompting:
		return "Queue a follow-up…"
	}
	return "Ask anything…"
}

func (m *model) View() tea.View {
	if m.quitting {
		return tea.NewView("")
	}
	w := m.lineWidth()
	modal := m.picker != nil || m.permission != nil || m.elicitation != nil || m.confirm != nil || m.page != "chat" || m.authWaiting
	inputView := m.input.View()
	if modal {
		value := m.input.Value()
		if value == "" {
			value = m.input.Placeholder
		}
		inputView = m.theme.dim.Render(line("❯ "+value, w))
	}
	var statusParts []string
	if m.busy && !modal {
		statusParts = append(statusParts, m.statusLine(w))
	}
	if !m.busy && m.status != "" && m.status != "Ready" && m.lastError == "" {
		statusParts = append(statusParts, m.theme.muted.Render(line("  "+m.status, w)))
	}
	if m.lastError != "" {
		message := m.cropLines(ansi.Hardwrap(clean(m.lastError), w-gutter, true), 3, false)
		statusParts = append(statusParts, hang(m.theme.danger.Render("×")+" ", m.theme.danger.Render(message)))
	}
	var badges []string
	if !modal {
		if n := len(m.attachments) + len(m.resources); n > 0 {
			label := "1 attachment"
			if n > 1 {
				label = fmt.Sprintf("%d attachments", n)
			}
			badges = append(badges, m.theme.muted.Render("  + "+label)+m.theme.dim.Render(" · ")+m.hints(w, "/detach", "to clear"))
		}
		if queue := m.queueView(w); queue != "" {
			badges = append(badges, queue)
		}
	}
	footer := m.footer(w, modal)
	baseHeight := lipgloss.Height(inputView) + 2
	if footer != "" {
		baseHeight += lipgloss.Height(footer)
	}
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
		name := line(strings.TrimSpace(m.confirm.Title), max(8, w/2))
		if name == "" {
			name = "this session"
		}
		action := fmt.Sprintf("Delete “%s” from the agent and local history?", name)
		if m.localDelete {
			action = fmt.Sprintf("Remove “%s” from local history?", name)
		}
		keys := m.theme.danger.Bold(true).Render("y") + m.theme.muted.Render(" delete") + m.theme.dim.Render(" · ") + m.hints(max(1, w-12), "n/esc", "keep")
		panel = m.titleRule("Delete session", "", w, m.theme.danger.Bold(true)) + "\n" + hang("  ", ansi.Wrap(action, w-gutter, "")) + "\n" + keys
	case m.page == "details" || m.page == "info" || m.page == "subagent" || m.authWaiting:
		title := "Details"
		hints := []string{"pgup/pgdn", "scroll", "esc", "back"}
		if m.page == "info" && m.pageTitle != "" {
			title = m.pageTitle
		}
		if m.authWaiting {
			title = "Signing in"
		}
		if m.page == "subagent" {
			title = m.subagentTitle()
			if child := m.subagents[m.subagent]; child.CanCancel && child.Active() {
				hints = append(hints, "ctrl+x", "stop subagent")
			}
		}
		v := m.viewport
		bottom := v.AtBottom()
		v.SetHeight(max(1, panelHeight-2))
		if bottom {
			v.GotoBottom()
		}
		right := ""
		if total := v.TotalLineCount(); total > v.Height() {
			right = fmt.Sprintf("%d%%", int(v.ScrollPercent()*100))
		}
		panel = m.titleRule(title, right, w, m.theme.accent.Bold(true)) + "\n" + v.View() + "\n" + m.hints(w, hints...)
	case m.picker != nil:
		panel = m.pickerView(panelHeight)
	case m.completion != nil:
		panel = m.completionView(panelHeight)
	}
	// A panel opened right under a tool row gets a gap, as prose already has.
	if panel != "" && modal && m.lastRow && m.height-baseHeight-lipgloss.Height(panel) > 1 {
		panel = "\n" + panel
	}
	var parts []string
	// The streaming tail and current plan stay in the managed area. Completed
	// conversation output is printed above it for native scrollback and selection.
	overhead := baseHeight
	if panel != "" {
		overhead += lipgloss.Height(panel)
	}
	var plan string
	if !modal {
		available := m.height - overhead
		if m.live != "" {
			available -= 3 // Leave room to follow the streaming response.
		}
		// Between turns the checklist shrinks to its summary line.
		if !m.busy && m.live == "" {
			available = min(available, 1)
		}
		plan = m.planView(w, min(8, available))
		if plan != "" {
			overhead += lipgloss.Height(plan)
		}
	}
	// Keep one row spare: a view as tall as the terminal pushes a stale frame
	// into scrollback.
	if m.live != "" && !modal && m.height-1 > overhead {
		parts = append(parts, m.cropLines(m.live, m.height-1-overhead, true))
	}
	if plan != "" {
		parts = append(parts, plan)
	}
	parts = append(parts, statusParts...)
	if panel != "" {
		parts = append(parts, panel)
	}
	parts = append(parts, badges...)
	parts = append(parts, m.theme.rule.Render(strings.Repeat("─", w)))
	before := strings.Join(parts, "\n")
	inputY := 0
	if before != "" {
		inputY = lipgloss.Height(before)
	}
	version := line("micro-acp "+buildinfo.Version, max(1, w-4))
	bottomRule := m.theme.rule.Render(strings.Repeat("─", max(0, w-ansi.StringWidth(version)-1))+" ") + m.theme.dim.Render(version)
	parts = append(parts, inputView, bottomRule)
	if footer != "" {
		parts = append(parts, footer)
	}
	v := tea.NewView(strings.Join(parts, "\n"))
	// Do not capture mouse events: copying and terminal scrollback should work.
	if !modal {
		v.Cursor = m.input.Cursor()
		if v.Cursor != nil {
			v.Cursor.Y += inputY
		}
	}
	return v
}
