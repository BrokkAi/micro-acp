package tui

import (
	"fmt"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/BrokkAi/micro-acp/internal/store"
	"github.com/charmbracelet/x/ansi"
)

const subagentMark = "◇"

// subagentName adds the run number of a resumed child to its name.
func subagentName(child store.Subagent) string {
	name := child.Name()
	if n := child.Generation(); n > 1 {
		name += fmt.Sprintf(" (run %d)", n)
	}
	return name
}

func (m *model) subagentStyle(child store.Subagent) lipgloss.Style {
	switch child.State {
	case store.SubagentRunning:
		return m.theme.accent
	case store.SubagentRequiresAction, store.SubagentCancelled:
		return m.theme.amber
	case store.SubagentFailed, store.SubagentDisconnected:
		return m.theme.danger
	case store.SubagentCompleted:
		return m.theme.mint
	case store.SubagentIdle:
		switch child.StopReason {
		case "end_turn":
			return m.theme.mint
		case "cancelled":
			return m.theme.amber
		case "":
		default:
			return m.theme.danger
		}
	}
	return m.theme.muted
}

// subagentSignature is what a committed child row shows, so the transcript
// can reprint the row when it changes.
func (m *model) subagentSignature(message store.Message) string {
	child := m.subagents[message.ID]
	return subagentName(child) + "\n" + child.StateLabel() + "\n" + child.Description
}

func (m *model) subagentView(message store.Message, details bool, width int) string {
	child, ok := m.subagents[message.ID]
	if !ok {
		child = store.Subagent{ID: message.ID, Title: message.Text}
	}
	style := m.subagentStyle(child)
	name := clean(subagentName(child))
	state := m.theme.muted.Render(" · ") + style.Render(child.StateLabel())
	if !details {
		row := style.Render(subagentMark+" ") + m.hintKey().Bold(true).Render(line(name, max(1, width-2-ansi.StringWidth(state)))) + state
		// A reprinted row only reports the new state; the task was shown once.
		if child.Description != "" && child.Description != child.Title && !m.reprinting {
			row += "\n" + m.theme.dim.Render("  └ ") + m.theme.muted.Render(line(child.Description, width-4))
		}
		return row
	}
	rendered := style.Render(subagentMark+" ") + m.hintKey().Bold(true).Render(ansi.Hardwrap(name, width-2, true)) + state
	if child.Description != "" && child.Description != child.Title {
		rendered += "\n" + indentTool(m.theme.muted.Render(ansi.Hardwrap(clean(child.Description), width-2, true)), 2)
	}
	return rendered + "\n" + indentTool(m.theme.muted.Render(line("/subagents opens its transcript", width-2)), 2) + "\n"
}

// sessionName names a session in the active tree for message headers.
func (m *model) sessionName(id string) string {
	if child, ok := m.subagents[id]; ok {
		return subagentName(child)
	}
	if m.client != nil {
		return m.client.Agent
	}
	return id
}

func (m *model) sessionMessageView(message store.Message, width int) string {
	header := m.theme.dim.Render("»") + " " + m.theme.muted.Render(line(m.sessionName(message.Sender)+" → "+m.sessionName(message.Recipient), width-gutter))
	return header + "\n" + hang("  ", m.markdown(message.Text, width)) + "\n"
}

// subagentItems lists the active tree depth first, children under parents.
func (m *model) subagentItems() []item {
	if m.client == nil {
		return nil
	}
	s, _ := m.client.Snapshot()
	children := map[string][]store.Subagent{}
	for _, child := range s.Subagents {
		children[child.ParentID] = append(children[child.ParentID], child)
	}
	var entries []item
	var walk func(parent string, depth int)
	walk = func(parent string, depth int) {
		for _, child := range children[parent] {
			title := strings.Repeat("  ", depth) + subagentName(child)
			if depth > 0 {
				title = strings.Repeat("  ", depth-1) + "↳ " + subagentName(child)
			}
			description := child.StateLabel()
			if child.Description != "" && child.Description != child.Title {
				description += " · " + child.Description
			}
			entries = append(entries, item{title: title, description: description, id: child.ID, value: child.ID})
			walk(child.ID, depth+1)
		}
	}
	walk(s.RemoteID, 0)
	return entries
}

func (m *model) openSubagent(id string) {
	m.page = "subagent"
	m.subagent = id
	m.refreshSubagent()
	m.viewport.GotoTop()
}

// refreshSubagent shows one child's transcript with full tool details.
func (m *model) refreshSubagent() {
	if m.client == nil {
		return
	}
	s, _ := m.client.Snapshot()
	child, ok := s.Subagent(m.subagent)
	if !ok {
		m.page = "chat"
		return
	}
	bottom := m.viewport.AtBottom()
	width := max(12, m.width-4)
	var parts []string
	if child.Description != "" && child.Description != child.Title {
		parts = append(parts, m.theme.muted.Render(ansi.Hardwrap(clean(child.Description), width, true))+"\n")
	}
	for _, message := range child.Messages {
		parts = append(parts, m.messageView(message, true, false))
	}
	if len(child.Messages) == 0 {
		parts = append(parts, m.theme.muted.Render("No output yet."))
	}
	m.viewport.SetContent(strings.Join(parts, "\n"))
	if bottom {
		m.viewport.GotoBottom()
	}
}

func (m *model) subagentTitle() string {
	child := m.subagents[m.subagent]
	return "Subagent · " + subagentName(child) + " · " + child.StateLabel()
}

// runningSubagents counts children still working or waiting on the user.
func (m *model) runningSubagents() int {
	n := 0
	for _, child := range m.subagents {
		if child.Active() {
			n++
		}
	}
	return n
}
