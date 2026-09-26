package tui

import (
	"fmt"
	"strings"

	"github.com/BrokkAi/acp-go/schema"
	"github.com/charmbracelet/x/ansi"
)

// Keep the active step visible without letting a long plan displace the composer.
// The details view retains every update and the full step text.
func (m *model) planView(width, height int) string {
	if m.plan == nil || len(m.plan.Entries) == 0 || height <= 0 {
		return ""
	}
	entries := m.plan.Entries
	completed, active := 0, -1
	for i, entry := range entries {
		if entry.Status == schema.PlanEntryStatusCompleted {
			completed++
		}
		if active < 0 && entry.Status == schema.PlanEntryStatusInProgress {
			active = i
		}
	}
	if active < 0 {
		active = len(entries) - 1
		for i, entry := range entries {
			if entry.Status != schema.PlanEntryStatusCompleted {
				active = i
				break
			}
		}
	}
	header := fmt.Sprintf("Plan · %d/%d complete", completed, len(entries))
	if height == 1 {
		return m.theme.accent.Render(line(header+" · Ctrl+O details", width))
	}
	rows := []string{m.theme.accent.Bold(true).Render(line(header, width))}
	limit := height - 1
	truncated := len(entries) > limit
	if truncated && limit > 1 {
		limit-- // Reserve a row for the visible range and details shortcut.
	}
	start := min(max(0, active-limit/2), max(0, len(entries)-limit))
	end := min(len(entries), start+limit)
	for _, entry := range entries[start:end] {
		mark, style := "○", m.theme.muted
		textStyle := plain
		switch entry.Status {
		case schema.PlanEntryStatusCompleted:
			mark, style, textStyle = "✓", m.theme.mint, m.theme.muted
		case schema.PlanEntryStatusInProgress:
			mark, style, textStyle = "›", m.theme.accent, m.theme.accent
		}
		priorityStyle := m.theme.muted
		switch entry.Priority {
		case schema.PlanEntryPriorityHigh:
			priorityStyle = m.theme.amber
		case schema.PlanEntryPriorityMedium:
			priorityStyle = m.theme.cyan
		}
		prefix := style.Render(mark+" ") + priorityStyle.Render("["+line(string(entry.Priority), 6)+"] ")
		row := prefix + textStyle.Render(line(entry.Content, max(1, width-ansi.StringWidth(prefix))))
		rows = append(rows, ansi.Truncate(row, width, "…"))
	}
	if truncated && len(rows) < height {
		hint := fmt.Sprintf("%d–%d of %d · Ctrl+O details", start+1, end, len(entries))
		rows = append(rows, m.theme.muted.Render(line(hint, width)))
	}
	return strings.Join(rows, "\n")
}
