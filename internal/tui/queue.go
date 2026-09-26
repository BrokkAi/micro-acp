package tui

import (
	"fmt"
	"slices"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/BrokkAi/micro-acp/internal/client"
)

type steeredMsg struct {
	client  *client.Client
	prompt  queuedPrompt
	outcome client.SteeringOutcome
	err     error
}

func (m *model) submitPrompt(text string, queue bool) tea.Cmd {
	if strings.TrimSpace(text) == "" && len(m.attachments)+len(m.resources) == 0 {
		return nil
	}
	if m.client == nil {
		m.lastError = "Choose an agent with /agents first"
		return nil
	}
	if m.busy && !m.prompting && m.steering == nil {
		m.lastError = "Wait for the agent connection to finish"
		return nil
	}
	s, _ := m.client.Snapshot()
	if m.editing != nil && m.editing.sessionID != s.ID {
		m.lastError = "Resume the queued prompt's session before sending it"
		return nil
	}
	blocks, err := m.promptBlocks(text)
	if err != nil {
		m.lastError = err.Error()
		return nil
	}
	m.queueSequence++
	q := queuedPrompt{id: m.queueSequence, text: text, blocks: blocks, attachments: m.attachments, resources: m.resources, sessionID: s.ID}
	m.lastError = ""
	m.input.Reset()
	m.completion = nil
	m.attachments, m.resources = nil, nil
	if m.editing != nil {
		q.id = m.editing.id
		m.insertQueued(q)
		m.restoreQueueDraft()
		return m.dispatchQueue()
	}
	q.steer = !queue && m.prompting && !m.queuePaused && m.client.CanSteer()
	if m.prompting || m.steering != nil || len(m.queued) > 0 {
		m.insertQueued(q)
		return m.dispatchQueue()
	}
	m.queuePaused = false
	return m.startPrompt(q)
}

func (m *model) insertQueued(q queuedPrompt) {
	m.queued = append(m.queued, q)
	// Editing or restoring a failed submission retains its original FIFO slot.
	slices.SortStableFunc(m.queued, func(a, b queuedPrompt) int {
		if a.id < b.id {
			return -1
		}
		if a.id > b.id {
			return 1
		}
		return 0
	})
}

func (m *model) dispatchQueue() tea.Cmd {
	if m.client == nil || m.steering != nil || m.editing != nil || m.queuePaused {
		return nil
	}
	if !m.prompting {
		return m.sendQueued()
	}
	s, _ := m.client.Snapshot()
	for i, q := range m.queued {
		if !q.steer || q.sessionID != s.ID {
			continue
		}
		if !m.client.CanSteer() {
			m.queued[i].steer = false
			continue
		}
		m.queued = append(m.queued[:i], m.queued[i+1:]...)
		m.steering = &q
		c := m.client
		return func() tea.Msg {
			outcome, err := c.SteerContent(q.sessionID, q.blocks)
			return steeredMsg{client: c, prompt: q, outcome: outcome, err: err}
		}
	}
	return nil
}

func (m *model) steeringResult(msg steeredMsg) tea.Cmd {
	if msg.client != m.client || m.steering == nil || msg.prompt.id != m.steering.id {
		return nil
	}
	m.steering = nil
	if !m.prompting {
		m.busy = false
	}
	if msg.err != nil || msg.outcome == client.SteeringPromptRequired {
		q := msg.prompt
		q.steer = false
		if msg.err != nil {
			q.deliveryError = msg.err.Error()
			m.queuePaused = true
			m.lastError = msg.err.Error()
		}
		m.insertQueued(q)
	}
	return m.dispatchQueue()
}

func (m *model) editQueued(index int) {
	if m.editing != nil || index < 0 || index >= len(m.queued) {
		return
	}
	q := m.queued[index]
	m.queued = append(m.queued[:index], m.queued[index+1:]...)
	m.savedDraft = &queuedPrompt{text: m.input.Value(), attachments: m.attachments, resources: m.resources}
	m.editing = &q
	m.input.SetValue(q.text)
	m.input.MoveToEnd()
	m.attachments, m.resources = q.attachments, q.resources
	m.completion = nil
	m.lastError = ""
}

func (m *model) restoreQueueDraft() {
	if m.savedDraft != nil {
		m.input.SetValue(m.savedDraft.text)
		m.input.MoveToEnd()
		m.attachments, m.resources = m.savedDraft.attachments, m.savedDraft.resources
	}
	m.editing, m.savedDraft = nil, nil
	m.completion = nil
}

func (m *model) queueView(width int) string {
	var rows []string
	if m.steering != nil {
		rows = append(rows, m.theme.amber.Render(line("› Sending guidance: "+m.steering.text, width)))
	}
	if m.editing != nil {
		rows = append(rows, m.theme.amber.Render(line("Editing queued prompt · Enter save · Esc restore", width)))
	}
	if len(m.queued) > 0 {
		header := fmt.Sprintf("%d queued · Alt+↑ edit last · /queue", len(m.queued))
		if m.queuePaused {
			header = fmt.Sprintf("%d queued · /queue send to continue", len(m.queued))
		}
		rows = append(rows, m.theme.muted.Render(line(header, width)))
		limit := min(3, max(1, m.height/8))
		for i, q := range m.queued[:min(limit, len(m.queued))] {
			label := strings.Join(strings.Fields(q.text), " ")
			if label == "" {
				label = "Attached context"
			}
			if q.steer {
				label = "steer · " + label
			}
			if q.deliveryError != "" {
				label = "check delivery · " + label
			}
			rows = append(rows, m.theme.cyan.Render(fmt.Sprintf("  %d ", i+1))+line(label, width-4))
		}
		if len(m.queued) > limit {
			rows = append(rows, m.theme.muted.Render(fmt.Sprintf("  +%d more", len(m.queued)-limit)))
		}
	}
	return strings.Join(rows, "\n")
}
