package tui

import (
	"fmt"
	"strings"

	"charm.land/glamour/v2"
	"github.com/BrokkAi/acp-go/schema"
	"github.com/BrokkAi/micro-acp/internal/store"
	"github.com/charmbracelet/x/ansi"
)

type markdownRenderer = glamour.TermRenderer

// lineWidth is the widest row the client prints. The last column stays free:
// erasing to the end of a line that filled it would clip its final cell.
func (m *model) lineWidth() int { return max(10, m.width-1) }

// proseWidth leaves the gutter on the left and a small margin on the right.
func (m *model) proseWidth() int { return max(12, m.width-gutter-2) }

func (m *model) markdown(text string, width int) string {
	key := fmt.Sprintf("%d:%s", width, text)
	if cached, ok := m.renderCache[key]; ok {
		return cached
	}
	rendered, need := hangLists(m.renderMarkdown(text, width), width)
	if need > 0 {
		// Wrapped list items need room to hang; render a little narrower.
		rendered, _ = hangLists(m.renderMarkdown(text, max(12, width-need)), width)
	}
	if len(m.renderCache) > 128 {
		m.renderCache = map[string]string{}
	}
	m.renderCache[key] = rendered
	return rendered
}

func (m *model) renderMarkdown(text string, width int) string {
	text = strings.ReplaceAll(clean(text), listMark, "")
	renderer, ok := m.renderers[width]
	var err error
	if !ok {
		renderer, err = glamour.NewTermRenderer(glamour.WithStyles(m.theme.markdownStyle(width)), glamour.WithWordWrap(width), glamour.WithPreservedNewLines(), glamour.WithChromaFormatter("terminal16m"))
		if err == nil {
			m.renderers[width] = renderer
		}
	}
	rendered := ""
	if err == nil {
		rendered, err = renderer.Render(text)
	}
	if err != nil {
		rendered = ansi.Hardwrap(text, width, true)
	}
	// Glamour pads every line to the wrap width and frames blocks with blank
	// lines. Streaming renders one paragraph at a time, so remove both and
	// let the transcript decide the spacing.
	return trimRight(keepEscapes(rendered))
}

// band renders a tinted row padded to width. Every segment carries the
// background itself, so inner resets cannot punch holes in it.
func (m *model) band(width int, segments ...string) string {
	bg := on(m.theme.surface)
	var b strings.Builder
	used := 0
	for i := 0; i+1 < len(segments); i += 2 {
		style := bg
		switch segments[i] {
		case "accent":
			style = m.theme.accent.Inherit(bg)
		case "muted":
			style = m.theme.muted.Inherit(bg)
		}
		b.WriteString(style.Render(segments[i+1]))
		used += ansi.StringWidth(segments[i+1])
	}
	if used < width && m.theme.surface != nil {
		b.WriteString(bg.Render(strings.Repeat(" ", width-used)))
	}
	return b.String()
}

func (m *model) messageView(message store.Message, details bool, continuation bool) string {
	text := clean(message.Text)
	width := m.proseWidth()
	switch message.Role {
	case "user":
		if len(message.Content) > 0 && message.Content[0].Text != nil {
			text = clean(message.Content[0].Text.Text)
		}
		if text == "" {
			text = "Attached context"
		}
		// The tint is sized to the text rather than the window, so it still
		// reads as one block after the terminal is resized.
		full := m.lineWidth()
		lines := strings.Split(ansi.Wrap(strings.TrimRight(strings.ReplaceAll(text, "\t", "    "), "\n"), max(8, width-1), ""), "\n")
		bandWidth := 0
		for i, row := range lines {
			lines[i] = ansi.Truncate(row, full-gutter-1, "…")
			bandWidth = max(bandWidth, gutter+ansi.StringWidth(lines[i])+1)
		}
		var rows []string
		for i, row := range lines {
			marker := strings.Repeat(" ", gutter)
			if i == 0 {
				marker = "❯ "
			}
			rows = append(rows, m.band(bandWidth, "accent", marker, "", row))
		}
		if n := len(message.Content) - 1; n > 0 {
			label := "1 attachment"
			if n > 1 {
				label = fmt.Sprintf("%d attachments", n)
			}
			rows = append(rows, m.theme.dim.Render("  └ ")+m.theme.muted.Render(label))
		}
		return strings.Join(rows, "\n") + "\n"
	case "assistant":
		marker := strings.Repeat(" ", gutter)
		if !continuation {
			marker = m.hintKey().Render("●") + " "
		}
		return hang(marker, m.markdown(text, width)) + "\n"
	case "tool":
		return m.toolView(message, details, width)
	case "subagent":
		return m.subagentView(message, details, width)
	case "message":
		return m.sessionMessageView(message, width)
	case "thought":
		thinking := m.theme.muted.Italic(true)
		if details {
			return hang(m.theme.muted.Render("·")+" ", thinking.Render("Thinking")+"\n"+thinking.Render(ansi.Hardwrap(text, width, true))) + "\n"
		}
		summary := line(strings.Join(strings.Fields(text), " "), max(10, width-11))
		return m.theme.muted.Render("·") + " " + thinking.Render("Thinking ") + m.theme.dim.Italic(true).Render(summary)
	case "terminal":
		lines := strings.Split(text, "\n")
		summary := m.theme.dim.Render("$") + " " + m.hintKey().Bold(true).Render(line(lines[0], width-gutter))
		if details {
			return hang(m.theme.dim.Render("$")+" ", m.hintKey().Bold(true).Render(ansi.Hardwrap(lines[0], width-gutter, true))) + "\n" + m.toolText(strings.Join(lines[1:], "\n"), width) + "\n"
		}
		if len(lines) > 1 {
			summary += "\n" + m.theme.dim.Render("  └ ") + m.toolText(line(lines[len(lines)-1], width-4), width-4)
		}
		return summary
	case "plan":
		return m.theme.muted.Bold(true).Render("Plan") + "\n" + m.theme.muted.Render(ansi.Wrap(text, width, ""))
	default:
		return m.theme.muted.Render(ansi.Wrap(text, width, ""))
	}
}

// Only complete paragraphs outside code fences are committed while streaming.
// The unfinished tail remains editable; committed output belongs to terminal scrollback.
func stablePrefix(text string) int {
	fence := ""
	offset, boundary := 0, 0
	for _, line := range strings.SplitAfter(text, "\n") {
		if !strings.HasSuffix(line, "\n") {
			break
		}
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "```") || strings.HasPrefix(trimmed, "~~~") {
			marker := trimmed[:3]
			if fence == "" {
				fence = marker
			} else if fence == marker {
				fence = ""
			}
		}
		offset += len(line)
		if fence == "" && (trimmed == "" || offset > 4096) {
			boundary = offset
		}
	}
	return boundary
}
func (m *model) syncTranscript() {
	if m.client == nil {
		m.plan = nil
		return
	}
	// Session operations can replay several chunks into the same message.
	// Wait for completion before committing that history to scrollback.
	// Until the theme is known, nothing is rendered: scrollback cannot be
	// restyled once printed.
	if m.busy && !m.prompting || m.headerPending {
		return
	}
	s, _ := m.client.Snapshot()
	if s.ID == "" || s.RemoteID == "" {
		m.live = ""
		m.plan = nil
		m.committedIndex, m.committedText = -1, ""
		m.committedTools = map[int]string{}
		return
	}
	m.plan = s.Plan
	m.subagents = make(map[string]store.Subagent, len(s.Subagents))
	for _, child := range s.Subagents {
		m.subagents[child.ID] = child
	}
	if s.ID != m.printedSession {
		inherited := 0
		if s.ParentID == m.printedSession {
			inherited = min(m.printedIndex, len(s.Messages))
		}
		m.printedSession = s.ID
		m.printedIndex = inherited
		m.streamPrefix = 0
		m.committedIndex, m.committedText = -1, ""
		m.committedTools = map[int]string{}
		agent := line(m.client.Agent, max(1, m.lineWidth()/2))
		label := m.hintKey().Bold(true).Render(agent) + m.theme.muted.Render(line(" · "+s.Title, max(1, m.lineWidth()-ansi.StringWidth(agent)-7)))
		if s.ParentID != "" {
			label += m.theme.dim.Render(" · fork")
		}
		m.queueOutput(label + "\n")
		m.lastRow = false
	}
	if m.page == "details" {
		m.refreshDetails()
	}
	if m.page == "subagent" {
		m.refreshSubagent()
	}
	for m.printedIndex < len(s.Messages) {
		i := m.printedIndex
		message := s.Messages[i]
		if message.Pending {
			break
		}
		if message.Role == "plan" {
			// Plans update the live checklist; retain snapshots only in details.
			m.printedIndex++
			m.streamPrefix = 0
			continue
		}
		last := i == len(s.Messages)-1
		final := !m.prompting || m.quitting
		if message.Role == "user" {
			final = true
		}
		if message.Role == "assistant" || message.Role == "thought" {
			final = final || !last
		}
		if message.Role == "tool" && message.Tool != nil && message.Tool.Status != nil {
			status := *message.Tool.Status
			final = final || status == schema.ToolCallStatusCompleted || status == schema.ToolCallStatusFailed
		}
		if message.Role == "notice" || message.Role == "message" {
			final = true
		}
		// A child can run for the rest of the turn. Commit its row at once
		// and reprint it when its state changes, so later output is not held.
		if message.Role == "subagent" {
			final = true
		}
		committed := message.Text
		if message.Role == "assistant" && m.streamPrefix <= len(message.Text) {
			message.Text = message.Text[m.streamPrefix:]
		}
		if final {
			if message.Text != "" {
				m.printMessage(message, m.streamPrefix > 0)
			}
			m.committedIndex, m.committedText = i, committed
			if signature, ok := m.rowSignature(message); ok {
				m.committedTools[i] = signature
			}
			m.printedIndex++
			m.streamPrefix = 0
			continue
		}
		if message.Role == "assistant" {
			if n := stablePrefix(message.Text); n > 0 {
				prefix := message
				prefix.Text = message.Text[:n]
				m.printMessage(prefix, m.streamPrefix > 0)
				m.streamPrefix += n
			}
		}
		break
	}
	// Late updates can land after a message was committed, once the prompt
	// response is already on the wire. Scrollback cannot be edited, so reprint
	// what changed instead of leaving the transcript stale. Late text merges
	// into the newest message; late tool updates are addressed by ID and can
	// target any committed message.
	if m.committedIndex >= 0 && m.committedIndex == len(s.Messages)-1 {
		message := s.Messages[m.committedIndex]
		textRole := message.Role == "assistant" || message.Role == "thought"
		if textRole && message.Text != m.committedText && strings.HasPrefix(message.Text, m.committedText) {
			delta := message
			delta.Text = message.Text[len(m.committedText):]
			// Keep the assistant bullet when nothing was committed yet.
			m.printMessage(delta, m.committedText != "")
			m.committedText = message.Text
		}
	}
	for i := 0; i < m.printedIndex && i < len(s.Messages); i++ {
		message := s.Messages[i]
		committed, ok := m.committedTools[i]
		if !ok {
			continue
		}
		if current, _ := m.rowSignature(message); current != committed {
			// A subagent's task line is shown again only when it changed.
			m.reprinting = message.Role == "subagent" && subagentTask(current) == subagentTask(committed)
			m.printMessage(message, false)
			m.reprinting = false
			m.committedTools[i] = current
		}
	}
	var live []string
	previousRow := m.lastRow
	for i := m.printedIndex; i < len(s.Messages); i++ {
		message := s.Messages[i]
		if message.Pending {
			break
		}
		if message.Role == "plan" {
			continue
		}
		continued := i == m.printedIndex && m.streamPrefix > 0
		if continued && message.Role == "assistant" {
			message.Text = message.Text[min(m.streamPrefix, len(message.Text)):]
		}
		if message.Text != "" {
			view := m.messageView(message, false, continued)
			row := compactRole(message.Role)
			if previousRow && !row && !continued {
				view = "\n" + view
			}
			live = append(live, view)
			previousRow = row
		}
	}
	m.live = strings.Join(live, "\n")
	m.activity = activity(s.Messages, m.plan)
}

// activity names what the agent is doing right now for the status line: the
// newest running tool call in this turn, else the plan step in progress.
func activity(messages []store.Message, plan *schema.Plan) string {
	for i := len(messages) - 1; i >= 0 && messages[i].Role != "user"; i-- {
		message := messages[i]
		if message.Role != "tool" || message.Tool == nil || message.Cancelled || strings.TrimSpace(message.Tool.Title) == "" {
			continue
		}
		if status := message.Tool.Status; status == nil || *status == schema.ToolCallStatusPending || *status == schema.ToolCallStatusInProgress {
			return message.Tool.Title
		}
	}
	if plan != nil {
		for _, entry := range plan.Entries {
			if entry.Status == schema.PlanEntryStatusInProgress {
				return entry.Content
			}
		}
	}
	return ""
}

// compactRole reports rows that stack without blank lines between them.
func compactRole(role string) bool {
	switch role {
	case "tool", "thought", "subagent", "terminal":
		return true
	}
	return false
}

// printMessage commits a message to scrollback. Tool and thinking rows stack
// tightly; prose that follows them gets a blank line, like the prose before.
func (m *model) printMessage(message store.Message, continuation bool) {
	view := m.messageView(message, false, continuation)
	row := compactRole(message.Role)
	if m.lastRow && !row && !continuation {
		view = "\n" + view
	}
	m.queueOutput(view)
	m.lastRow = row
}

// rowSignature is what a committed tool or subagent row shows. Rows are
// reprinted when it changes.
func (m *model) rowSignature(message store.Message) (string, bool) {
	switch message.Role {
	case "tool":
		return toolSignature(message), true
	case "subagent":
		return m.subagentSignature(message), true
	}
	return "", false
}

func (m *model) refreshDetails() {
	if m.client == nil {
		return
	}
	s, _ := m.client.Snapshot()
	bottom := m.viewport.AtBottom()
	var parts []string
	for _, message := range s.Messages {
		if message.Pending {
			break
		}
		parts = append(parts, m.messageView(message, true, false))
	}
	m.viewport.SetContent(strings.Join(parts, "\n"))
	if bottom {
		m.viewport.GotoBottom()
	}
}
