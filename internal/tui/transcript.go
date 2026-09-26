package tui

import (
	"fmt"
	"strings"

	"charm.land/glamour/v2"
	"github.com/BrokkAi/acp-go/schema"
	"github.com/BrokkAi/micro-acp/internal/store"
	"github.com/charmbracelet/x/ansi"
)

func (m *model) markdown(text string) string {
	width := max(12, m.width-4)
	key := fmt.Sprintf("%d:%s", width, text)
	if cached, ok := m.renderCache[key]; ok {
		return cached
	}
	style := "dark"
	if !m.theme.dark {
		style = "light"
	}
	renderer, err := glamour.NewTermRenderer(glamour.WithStandardStyle(style), glamour.WithWordWrap(width), glamour.WithPreservedNewLines())
	rendered := ""
	if err == nil {
		rendered, err = renderer.Render(clean(text))
	}
	if err != nil {
		rendered = ansi.Hardwrap(clean(text), width, true)
	}
	// Remove vertical padding, preserving code indentation and ANSI styling.
	rendered = strings.Trim(rendered, "\n")
	if len(m.renderCache) > 128 {
		m.renderCache = map[string]string{}
	}
	m.renderCache[key] = rendered
	return rendered
}
func (m *model) messageView(message store.Message, details bool, continuation bool) string {
	text := clean(message.Text)
	width := max(12, m.width-4)
	switch message.Role {
	case "user":
		if len(message.Content) > 0 && message.Content[0].Text != nil {
			text = clean(message.Content[0].Text.Text)
		}
		if text == "" {
			text = "Attached context"
		}
		result := m.theme.accent.Render("❯ ") + strings.ReplaceAll(ansi.Wrap(text, max(10, width-2), ""), "\n", "\n  ")
		if len(message.Content) > 1 {
			result += "\n" + m.theme.muted.Render(fmt.Sprintf("  %d attachment(s)", len(message.Content)-1))
		}
		return result + "\n"
	case "assistant":
		prefix := ""
		if !continuation {
			prefix = m.theme.mint.Render("● ")
		}
		return prefix + m.markdown(text) + "\n"
	case "tool":
		title, status := "Tool", ""
		if message.Tool != nil {
			title = message.Tool.Title
			if message.Tool.Status != nil {
				status = string(*message.Tool.Status)
			}
		} else if text != "" {
			title = strings.Split(text, "\n")[0]
		}
		mark := "◦"
		style := m.theme.muted
		if status == "completed" {
			mark = "✓"
			style = m.theme.mint
		}
		if status == "failed" {
			mark = "×"
			style = m.theme.danger
		}
		summary := style.Render(mark+" ") + line(title, width-2)
		if details {
			return summary + "\n" + ansi.Hardwrap(text, width, true) + "\n"
		}
		if message.Tool != nil {
			for _, part := range message.Tool.Content {
				if part.Diff != nil {
					d := part.Diff
					label := "updated"
					if d.OldText == nil {
						label = "created"
					}
					summary += "\n" + m.theme.muted.Render(line("  "+d.Path+" · "+label+" · Ctrl+O for diff", width))
				}
			}
		}
		return summary
	case "thought":
		if details {
			return m.theme.muted.Render("Thinking\n"+ansi.Hardwrap(text, width, true)) + "\n"
		}
		return m.theme.muted.Render("· Thinking  " + line(strings.Join(strings.Fields(text), " "), max(10, width-13)))
	case "terminal":
		if details {
			return m.theme.muted.Render("Terminal\n") + ansi.Hardwrap(text, width, true) + "\n"
		}
		lines := strings.Split(text, "\n")
		summary := m.theme.muted.Render("$ " + line(lines[0], width-2))
		if len(lines) > 1 {
			summary += "\n" + m.theme.muted.Render("  "+line(lines[len(lines)-1], width-2))
		}
		return summary
	case "plan":
		return m.theme.muted.Render("Plan\n" + ansi.Hardwrap(text, width, true))
	default:
		return m.theme.muted.Render(ansi.Hardwrap(text, width, true))
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
		return
	}
	// Session operations can replay several chunks into the same message.
	// Wait for completion before committing that history to scrollback.
	if m.busy && !m.prompting {
		return
	}
	s, _ := m.client.Snapshot()
	if s.ID == "" || s.RemoteID == "" {
		m.live = ""
		return
	}
	if s.ID != m.printedSession {
		inherited := 0
		if s.ParentID == m.printedSession {
			inherited = min(m.printedIndex, len(s.Messages))
		}
		m.printedSession = s.ID
		m.printedIndex = inherited
		m.streamPrefix = 0
		label := m.client.Agent + " · " + s.Title
		if s.ParentID != "" {
			label += " · fork"
		}
		m.queueOutput(m.theme.muted.Render(line(label, m.width-4)) + "\n")
	}
	if m.page == "details" {
		m.refreshDetails()
	}
	for m.printedIndex < len(s.Messages) {
		i := m.printedIndex
		message := s.Messages[i]
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
		if message.Role == "plan" || message.Role == "notice" {
			final = true
		}
		if message.Role == "assistant" && m.streamPrefix <= len(message.Text) {
			message.Text = message.Text[m.streamPrefix:]
		}
		if final {
			if message.Text != "" {
				m.queueOutput(m.messageView(message, false, m.streamPrefix > 0))
			}
			m.printedIndex++
			m.streamPrefix = 0
			continue
		}
		if message.Role == "assistant" {
			if n := stablePrefix(message.Text); n > 0 {
				prefix := message
				prefix.Text = message.Text[:n]
				m.queueOutput(m.messageView(prefix, false, m.streamPrefix > 0))
				m.streamPrefix += n
			}
		}
		break
	}
	var live []string
	for i := m.printedIndex; i < len(s.Messages); i++ {
		message := s.Messages[i]
		continued := i == m.printedIndex && m.streamPrefix > 0
		if continued && message.Role == "assistant" {
			message.Text = message.Text[min(m.streamPrefix, len(message.Text)):]
		}
		if message.Text != "" {
			live = append(live, m.messageView(message, false, continued))
		}
	}
	m.live = strings.Join(live, "\n")
}
func (m *model) refreshDetails() {
	if m.client == nil {
		return
	}
	s, _ := m.client.Snapshot()
	bottom := m.viewport.AtBottom()
	var parts []string
	for _, message := range s.Messages {
		parts = append(parts, m.messageView(message, true, false))
	}
	m.viewport.SetContent(strings.Join(parts, "\n"))
	if bottom {
		m.viewport.GotoBottom()
	}
}
