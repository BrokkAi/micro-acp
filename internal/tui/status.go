package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/x/ansi"
)

// Keep complete setting/value pairs together. Wrap once, then explicitly show
// overflow instead of silently cutting off settings with an ellipsis.
func configurationStatus(agent string, fields []string, width int) string {
	fields = append([]string{agent}, fields...)
	for count := len(fields); count >= 0; count-- {
		hint := "/config"
		if count < len(fields) {
			hint = fmt.Sprintf("+%d more · /config", len(fields)-count)
		}
		chunks := append(append([]string{}, fields[:count]...), hint)
		var rows []string
		row := ""
		for _, chunk := range chunks {
			chunk = line(chunk, width)
			if row == "" {
				row = chunk
				continue
			}
			if ansi.StringWidth(row)+3+ansi.StringWidth(chunk) > width {
				rows = append(rows, row)
				row = chunk
			} else {
				row += " · " + chunk
			}
		}
		rows = append(rows, row)
		if len(rows) <= 2 {
			return strings.Join(rows, "\n")
		}
	}
	return ""
}
