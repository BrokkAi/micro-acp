package tui

import (
	tea "charm.land/bubbletea/v2"
)

type authDoneMsg struct {
	err      error
	terminal bool
}

func (m *model) openAuth() {
	var entries []item
	for _, choice := range m.client.AuthChoices() {
		description := choice.Description
		if choice.Terminal {
			description = "Interactive terminal login · " + description
		}
		entries = append(entries, item{title: choice.Name, description: description, id: choice.ID})
	}
	if len(entries) == 0 {
		m.lastError = "Agent offers no login methods; configure its credentials and reconnect"
		return
	}
	m.openPicker("auth", entries)
	m.status = "Choose how to authenticate"
}
func (m *model) authenticate(id string) tea.Cmd {
	c := m.client
	for _, choice := range c.AuthChoices() {
		if choice.ID != id {
			continue
		}
		m.busy = true
		m.lastError = ""
		m.status = "Authenticating…"
		if choice.Terminal {
			cmd, err := c.AuthCommand(id)
			if err != nil {
				m.busy = false
				m.lastError = err.Error()
				return nil
			}
			return tea.ExecProcess(cmd, func(err error) tea.Msg { return authDoneMsg{err: err, terminal: true} })
		}
		m.authWaiting = true
		m.page = "authwait"
		return func() tea.Msg { return authDoneMsg{err: c.Authenticate(id)} }
	}
	m.lastError = "Unknown authentication method " + id
	return nil
}
