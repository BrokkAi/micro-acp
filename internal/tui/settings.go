package tui

import (
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/BrokkAi/micro-acp/internal/client"
)

func currentChoice(s client.Selector) string {
	for _, choice := range s.Choices {
		if choice.Value == s.Current {
			return choice.Name
		}
	}
	return s.Current
}

func settingItems(selectors []client.Selector, category string) []item {
	var entries []item
	for _, s := range selectors {
		if category != "" && s.Category != category {
			continue
		}
		description := currentChoice(s) + " · " + s.ID
		if s.Description != "" {
			description += " · " + s.Description
		}
		entries = append(entries, item{title: s.Name, description: description, id: s.ID, value: s})
	}
	return entries
}

func choiceItems(s client.Selector) []item {
	var entries []item
	for _, choice := range s.Choices {
		title := choice.Name
		if choice.Group != "" {
			title = choice.Group + " / " + title
		}
		if choice.Value == s.Current {
			title += " ✓"
		}
		entries = append(entries, item{title: title, description: choice.Description, id: choice.Value})
	}
	return entries
}

func (m *model) configureSession(arg string) tea.Cmd {
	if arg == "" {
		m.openSettings("")
		return nil
	}
	id, value, hasValue := strings.Cut(arg, " ")
	for _, s := range m.client.Selectors() {
		if s.ID != id {
			continue
		}
		value = strings.TrimSpace(value)
		if !hasValue || value == "" {
			m.chooseSetting(s)
			return nil
		}
		c := m.client
		return m.perform("Updating "+s.Name, func() error { return c.SetConfig(id, value) })
	}
	m.lastError = "Unknown session option " + id + ". Use /config to see the agent's options."
	return nil
}

func (m *model) openSettings(category string) {
	if category == "effort" {
		category = "thought_level"
	}
	entries := settingItems(m.client.Selectors(), category)
	if len(entries) == 0 {
		m.lastError = "Agent does not offer session configuration options"
		if category != "" {
			m.lastError = "Agent does not offer " + category + " settings"
		}
		return
	}
	if category != "" && len(entries) == 1 {
		m.chooseSetting(entries[0].value.(client.Selector))
		return
	}
	m.openPicker("settings", entries)
	m.picker.category = category
}

func (m *model) chooseSetting(s client.Selector) {
	m.selector = s
	m.openPicker("choices", choiceItems(s))
	for i, e := range m.picker.matches {
		if e.id == s.Current {
			m.picker.index = i
			break
		}
	}
}

// Configuration notifications can replace options, values, and even entire
// settings while a selector is open. Keep the search and selection when possible.
func (m *model) refreshSettingPicker(selectors []client.Selector) {
	p := m.picker
	if p == nil || (p.kind != "settings" && p.kind != "choices") {
		return
	}
	selected := ""
	if p.index < len(p.matches) {
		selected = p.matches[p.index].id
	}
	if p.kind == "settings" {
		p.entries = settingItems(selectors, p.category)
	} else {
		found := false
		for _, s := range selectors {
			if s.ID != m.selector.ID {
				continue
			}
			m.selector = s
			p.title, p.entries = s.Name, choiceItems(s)
			found = true
			break
		}
		if !found {
			m.lastError = "The agent removed this option. Use /config for current settings."
			m.picker = nil
			return
		}
	}
	p.filter()
	for i, e := range p.matches {
		if e.id == selected {
			p.index = i
			break
		}
	}
}
