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
	count := fmt.Sprintf(" · %d/%d complete", completed, len(entries))
	header := m.hintKey().Bold(true).Render("Plan") + m.theme.muted.Render(line(count, max(1, width-4)))
	if height == 1 {
		return header + m.theme.dim.Render(line(" · ctrl+o details", max(1, width-4-ansi.StringWidth(count))))
	}
	rows := []string{header}
	limit := height - 1
	truncated := len(entries) > limit
	if truncated && limit > 1 {
		limit-- // Reserve a row for the visible range and details shortcut.
	}
	start := min(max(0, active-limit/2), max(0, len(entries)-limit))
	end := min(len(entries), start+limit)
	for _, entry := range entries[start:end] {
		// Only the step in progress draws the eye; finished steps recede.
		mark, style := "□", m.theme.muted
		textStyle := plain
		switch entry.Status {
		case schema.PlanEntryStatusCompleted:
			mark, style, textStyle = "✓", m.theme.mint, m.theme.dim.Strikethrough(true)
		case schema.PlanEntryStatusInProgress:
			mark, style, textStyle = "■", m.theme.accent, m.theme.accent.Bold(true)
		}
		priority := ""
		if entry.Priority == schema.PlanEntryPriorityHigh {
			priority = " · high"
		}
		prefix := "  " + style.Render(mark) + " "
		content := line(entry.Content, max(1, width-ansi.StringWidth(prefix)-ansi.StringWidth(priority)))
		rows = append(rows, ansi.Truncate(prefix+textStyle.Render(content)+m.theme.amber.Render(priority), width, "…"))
	}
	if truncated && len(rows) < height {
		hint := fmt.Sprintf("    %d–%d of %d · ctrl+o details", start+1, end, len(entries))
		rows = append(rows, m.theme.dim.Render(line(hint, width)))
	}
	return strings.Join(rows, "\n")
}
