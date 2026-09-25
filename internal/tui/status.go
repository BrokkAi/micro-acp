package tui

import (
	"fmt"
	"strings"

	"github.com/BrokkAi/micro-acp/internal/client"
	"github.com/charmbracelet/x/ansi"
)

func (m *model) statusValue(field client.StatusField, width int) string {
	text, style := field.Value, plain
	switch field.Category {
	case "model":
		style = m.theme.accent.Bold(true)
	case "thought_level":
		style = m.theme.muted
	case "mode":
		style = m.theme.cyan
	case "usage":
		style = m.theme.muted
		if field.Capacity > 0 {
			used := float64(field.Used) / float64(field.Capacity)
			text = fmt.Sprintf("%.0f%% left", max(0, 1-used)*100)
			if used >= .8 {
				style = m.theme.amber
			}
			if used >= .95 {
				style = m.theme.danger
			}
		}
	case "cost":
		style = m.theme.muted
	}
	if field.Boolean {
		text, style = "○ "+field.Name, m.theme.muted
		if field.Enabled {
			text, style = "✓ "+field.Name, m.theme.mint
		}
	}
	return style.Render(line(text, width))
}

// Style values independently; separators and hints stay quiet. Measure visible
// cell widths so ANSI styles do not change wrapping or hide the overflow hint.
func (m *model) configurationStatus(agent string, fields []client.StatusField, width int) string {
	values := []string{m.theme.muted.Render(line(agent, width))}
	for _, field := range fields {
		values = append(values, m.statusValue(field, width))
	}
	separator := m.theme.rule.Render(" · ")
	for count := len(values); count >= 0; count-- {
		hint := "/config"
		if count < len(values) {
			hint = fmt.Sprintf("+%d more · /config", len(values)-count)
		}
		chunks := append(append([]string{}, values[:count]...), m.theme.muted.Render(line(hint, width)))
		var rows []string
		row := ""
		for _, chunk := range chunks {
			if row == "" {
				row = chunk
				continue
			}
			if ansi.StringWidth(row)+3+ansi.StringWidth(chunk) > width {
				rows = append(rows, row)
				row = chunk
			} else {
				row += separator + chunk
			}
		}
		rows = append(rows, row)
		if len(rows) <= 2 {
			return strings.Join(rows, "\n")
		}
	}
	return ""
}
