package tui

import (
	"strconv"
	"strings"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"github.com/BrokkAi/micro-acp/internal/client"
	"github.com/BrokkAi/micro-acp/internal/store"
	"github.com/sahilm/fuzzy"
)

// Selectors live beside the composer; they never replace the conversation.
type picker struct {
	kind, title      string
	category         string
	entries, matches []item
	input            textinput.Model
	index            int
}

func filterItems(entries []item, query string) []item {
	if strings.TrimSpace(query) == "" {
		return append([]item(nil), entries...)
	}
	haystack := make([]string, len(entries))
	for i, e := range entries {
		haystack[i] = e.id + " " + e.title + " " + e.description
	}
	matches := fuzzy.Find(query, haystack)
	result := make([]item, 0, len(matches))
	for _, match := range matches {
		result = append(result, entries[match.Index])
	}
	return result
}
func (p *picker) filter() { p.matches = filterItems(p.entries, p.input.Value()); p.index = 0 }
func (m *model) openPicker(kind string, entries []item) {
	titles := map[string]string{"agents": "Choose an agent", "sessions": "Resume a session", "settings": "Session configuration", "choices": m.selector.Name, "auth": "Sign in", "queue": "Queued prompts", "commands": "Commands"}
	input := textinput.New()
	input.Placeholder = "Type to search…"
	input.Prompt = "› "
	input.SetWidth(max(10, m.width-8))
	input.Focus()
	m.picker = &picker{kind: kind, title: titles[kind], entries: entries, matches: entries, input: input}
	m.completion = nil
	m.page = "chat"
}
func (m *model) pickerKey(msg tea.KeyPressMsg) tea.Cmd {
	p := m.picker
	switch msg.String() {
	case "esc":
		m.picker = nil
		return m.input.Focus()
	case "up", "ctrl+p":
		p.index = max(0, p.index-1)
		return nil
	case "down", "ctrl+n":
		p.index = min(max(0, len(p.matches)-1), p.index+1)
		return nil
	case "enter", "tab":
		if len(p.matches) == 0 {
			return nil
		}
		selected := p.matches[p.index]
		kind := p.kind
		if kind == "agents" {
			if m.busy {
				m.lastError = "Stop the current turn with Esc first"
				return nil
			}
			return m.connect(selected)
		}
		m.picker = nil
		switch kind {
		case "commands":
			if selected.arguments {
				m.input.SetValue(strings.TrimSpace(selected.id) + " ")
				m.input.MoveToEnd()
				return nil
			}
			return m.command(selected.id)
		case "auth":
			return m.authenticate(selected.id)
		case "sessions":
			s := selected.value.(store.Session)
			m.resumeTarget = &s
			return m.perform("Resuming session", func() error { return m.client.Load(s) })
		case "settings":
			m.chooseSetting(selected.value.(client.Selector))
		case "choices":
			c, id, value := m.client, m.selector.ID, selected.id
			return m.perform("Updating "+m.selector.Name, func() error { return c.SetConfig(id, value) })
		case "queue":
			i := -1
			for index, q := range m.queued {
				if q.id == selected.value.(uint64) {
					i = index
					break
				}
			}
			if i < 0 {
				m.lastError = "This prompt has already been sent"
				return nil
			}
			m.editQueued(i)
		}
		return nil
	case "ctrl+d":
		if p.kind == "queue" && len(p.matches) > 0 {
			id := p.matches[p.index].value.(uint64)
			for i, q := range m.queued {
				if q.id == id {
					m.queued = append(m.queued[:i], m.queued[i+1:]...)
					break
				}
			}
			m.refreshQueuePicker()
			return nil
		}
		if p.kind == "sessions" && len(p.matches) > 0 {
			s := p.matches[p.index].value.(store.Session)
			m.confirm = &s
			m.localDelete = !m.client.CanDelete()
			m.picker = nil
		}
		return nil
	}
	var cmd tea.Cmd
	p.input, cmd = p.input.Update(msg)
	p.filter()
	return cmd
}

func (m *model) queueItems() []item {
	var entries []item
	for _, q := range m.queued {
		description := "Enter edit · Ctrl+D remove"
		if q.deliveryError != "" {
			description = q.deliveryError
		}
		entries = append(entries, item{title: q.text, description: description, id: strconv.FormatUint(q.id, 10), value: q.id})
	}
	return entries
}

func (m *model) refreshQueuePicker() {
	p := m.picker
	if p == nil || p.kind != "queue" {
		return
	}
	selected := ""
	if p.index < len(p.matches) {
		selected = p.matches[p.index].id
	}
	index := p.index
	p.entries = m.queueItems()
	p.filter()
	p.index = min(index, max(0, len(p.matches)-1))
	for i, entry := range p.matches {
		if entry.id == selected {
			p.index = i
			break
		}
	}
}
