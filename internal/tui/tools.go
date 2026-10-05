package tui

import (
	"bytes"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/BrokkAi/acp-go/schema"
	"github.com/BrokkAi/micro-acp/internal/client"
	"github.com/BrokkAi/micro-acp/internal/store"
	"github.com/aymanbagabas/go-udiff"
	"github.com/charmbracelet/x/ansi"
)

func (m *model) toolStyle(tool *schema.ToolCall) lipgloss.Style {
	if tool != nil && tool.Kind != nil {
		switch *tool.Kind {
		case schema.ToolKindRead, schema.ToolKindSearch, schema.ToolKindFetch:
			return m.theme.cyan
		case schema.ToolKindExecute, schema.ToolKindMove:
			return m.theme.amber
		case schema.ToolKindDelete:
			return m.theme.danger
		}
	}
	return m.theme.accent
}

type toolDiff struct{ path, label string }

type toolSummary struct {
	mark  string
	title string
	diffs []toolDiff
}

// summarizeTool extracts what the compact tool line shows. It is independent
// of terminal width, so the transcript can detect real updates without
// mistaking a resize for one.
func summarizeTool(message store.Message) toolSummary {
	summary := toolSummary{mark: "◦", title: "Tool"}
	tool := message.Tool
	if tool != nil {
		summary.title = tool.Title
		if tool.Status != nil {
			switch *tool.Status {
			case schema.ToolCallStatusCompleted:
				summary.mark = "✓"
			case schema.ToolCallStatusFailed:
				summary.mark = "×"
			}
		}
		for _, part := range tool.Content {
			if part.Diff == nil {
				continue
			}
			label := "updated"
			if part.Diff.OldText == nil {
				label = "created"
			}
			summary.diffs = append(summary.diffs, toolDiff{path: strings.ReplaceAll(clean(part.Diff.Path), "\n", " "), label: label})
		}
		for _, change := range message.Changes {
			summary.diffs = append(summary.diffs, toolDiff{path: strings.ReplaceAll(clean(change.Path), "\n", " "), label: clean(change.Label())})
		}
	} else if message.Text != "" {
		summary.title = strings.Split(message.Text, "\n")[0]
	}
	if message.Cancelled {
		summary.mark = "×"
		summary.title += " · cancelled"
	}
	summary.title = strings.ReplaceAll(clean(summary.title), "\n", " ")
	return summary
}

// toolSignature is the committed-transcript form of summarizeTool.
func toolSignature(message store.Message) string {
	summary := summarizeTool(message)
	rows := []string{summary.mark + " " + summary.title}
	for _, diff := range summary.diffs {
		rows = append(rows, diff.path+" · "+diff.label)
	}
	return strings.Join(rows, "\n")
}

func (m *model) toolView(message store.Message, details bool, width int) string {
	tool := message.Tool
	summary := summarizeTool(message)
	markStyle, titleStyle := m.theme.muted, m.toolStyle(tool)
	switch {
	case tool != nil && tool.Status != nil && *tool.Status == schema.ToolCallStatusFailed:
		markStyle, titleStyle = m.theme.danger, m.theme.danger
	case tool != nil && tool.Status != nil && *tool.Status == schema.ToolCallStatusCompleted:
		markStyle = m.theme.mint
	}
	if message.Cancelled {
		markStyle, titleStyle = m.theme.amber, m.theme.amber
	}
	if details {
		// Details wrap the full title; compact scrollback keeps a single row.
		styledTitle := titleStyle.Bold(true).Render(ansi.Hardwrap(clean(summary.title), width-2, true))
		rendered := markStyle.Render(summary.mark+" ") + strings.ReplaceAll(styledTitle, "\n", "\n  ")
		if message.Cancelled {
			rendered += "\n" + indentTool(m.theme.amber.Render(ansi.Hardwrap("Cancelled by client", width-2, true)), 2)
		}
		if tool == nil {
			return rendered + "\n" + m.toolText(message.Text, width) + "\n"
		}
		return rendered + "\n" + m.toolDetails(*tool, width) + m.fileChanges(message, width) + "\n"
	}
	rendered := markStyle.Render(summary.mark+" ") + titleStyle.Bold(true).Render(line(summary.title, width-2))
	for _, diff := range summary.diffs {
		style := m.theme.accent
		switch diff.label {
		case "created":
			style = m.theme.mint
		case "deleted":
			style = m.theme.danger
		}
		row := m.theme.cyan.Render(diff.path) + m.theme.muted.Render(" · ") + style.Render(diff.label) + m.theme.muted.Render(" · Ctrl+O for diff")
		rendered += "\n  " + ansi.Truncate(row, width-2, "…")
	}
	return rendered
}

func (m *model) toolDetails(tool schema.ToolCall, width int) string {
	var sections []string
	section := func(label, body string) {
		if body != "" {
			sections = append(sections, "  "+m.theme.muted.Bold(true).Render(label)+"\n"+indentTool(body, 4))
		}
	}
	w := max(1, width-4)
	var metadata []string
	if tool.Name != nil {
		metadata = append(metadata, *tool.Name)
	}
	if tool.Kind != nil {
		metadata = append(metadata, string(*tool.Kind))
	}
	if tool.Status != nil {
		metadata = append(metadata, strings.ReplaceAll(string(*tool.Status), "_", " "))
	}
	if len(metadata) > 0 {
		sections = append(sections, indentTool(m.theme.muted.Render(ansi.Hardwrap(clean(strings.Join(metadata, " · ")), width-2, true)), 2))
	}
	var locations []string
	for _, location := range tool.Locations {
		path := location.Path
		if location.Line != nil {
			path += fmt.Sprintf(":%d", *location.Line)
		}
		locations = append(locations, m.theme.cyan.Render(ansi.Hardwrap(clean(path), w, true)))
	}
	section("Files", strings.Join(locations, "\n"))
	section("Input", m.toolRaw(tool.RawInput, w))
	for _, part := range tool.Content {
		switch {
		case part.Content != nil:
			block := part.Content.Content
			body := m.toolText(client.ContentText(block), w)
			if block.Text != nil {
				text := strings.TrimSpace(block.Text.Text)
				if (strings.HasPrefix(text, "{") || strings.HasPrefix(text, "[")) && json.Valid([]byte(text)) {
					body = m.toolRaw(json.RawMessage(text), w)
				}
			}
			section("Output", body)
		case part.Diff != nil:
			d := part.Diff
			old, from := "", d.Path
			if d.OldText != nil {
				old = *d.OldText
			} else {
				from = "/dev/null"
			}
			patch := udiff.Unified(clean(from), clean(d.Path), clean(old), clean(d.NewText))
			if patch == "" {
				patch = "No textual changes"
			}
			section("Changes · "+line(d.Path, w-10), m.toolLines(strings.TrimSuffix(patch, "\n"), w, true))
		case part.Terminal != nil:
			section("Terminal", m.theme.amber.Render(ansi.Hardwrap(clean(string(part.Terminal.TerminalID)), w, true)))
		}
	}
	section("Result", m.toolRaw(tool.RawOutput, w))
	return ansi.Hardwrap(strings.Join(sections, "\n\n"), width, true)
}

// fileChanges lists an ACP v2 tool call's structured file changes and shows
// its patch with diff colors.
func (m *model) fileChanges(message store.Message, width int) string {
	if len(message.Changes) == 0 && message.Patch == "" {
		return ""
	}
	w := max(1, width-4)
	var rows []string
	for _, change := range message.Changes {
		rows = append(rows, m.theme.cyan.Render(ansi.Hardwrap(clean(change.Path), w, true))+m.theme.muted.Render(" · "+clean(change.Label())))
	}
	body := strings.Join(rows, "\n")
	if message.Patch != "" {
		if body != "" {
			body += "\n"
		}
		body += m.toolLines(strings.TrimSuffix(message.Patch, "\n"), w, true)
	}
	return "\n\n  " + m.theme.muted.Bold(true).Render("Changes") + "\n" + indentTool(body, 4)
}

func indentTool(text string, spaces int) string {
	indent := strings.Repeat(" ", spaces)
	return indent + strings.ReplaceAll(text, "\n", "\n"+indent)
}

// Decode raw values for presentation only. Session files retain the original
// ACP payload, including fields unknown to this client.
func (m *model) toolRaw(raw json.RawMessage, width int) string {
	if len(bytes.TrimSpace(raw)) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return ""
	}
	if !json.Valid(raw) {
		return m.toolText(string(raw), width)
	}
	var value any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&value); err != nil {
		return m.toolText(string(raw), width)
	}
	return m.toolValue(value, width)
}

func (m *model) toolValue(value any, width int) string {
	// Limit indentation on narrow terminals without dropping nested fields.
	indent := min(2, max(0, width-12))
	field := func(key string, value any) string {
		label := m.theme.cyan.Render(ansi.Hardwrap(clean(key)+":", width, true))
		body := m.toolValue(value, width-indent)
		if text, ok := value.(string); ok && text != "" {
			style := plain
			switch strings.ToLower(key) {
			case "command", "cmd", "shell":
				style = m.theme.amber
			case "path", "file", "file_path", "filepath", "cwd", "url", "uri":
				style = m.theme.cyan
			case "error", "stderr":
				style = m.theme.danger
			}
			body = style.Render(body)
		}
		if !strings.Contains(body, "\n") && ansi.StringWidth(label)+1+ansi.StringWidth(body) <= width {
			return label + " " + body
		}
		return label + "\n" + indentTool(body, indent)
	}
	switch value := value.(type) {
	case map[string]any:
		if len(value) == 0 {
			return m.theme.muted.Render("(empty object)")
		}
		keys := make([]string, 0, len(value))
		for key := range value {
			keys = append(keys, key)
		}
		slices.Sort(keys)
		var rows []string
		for _, key := range keys {
			rows = append(rows, field(key, value[key]))
		}
		return strings.Join(rows, "\n")
	case []any:
		if len(value) == 0 {
			return m.theme.muted.Render("(empty list)")
		}
		var rows []string
		for i, item := range value {
			rows = append(rows, field(fmt.Sprintf("[%d]", i+1), item))
		}
		return strings.Join(rows, "\n")
	case string:
		if value == "" {
			return m.theme.muted.Render("(empty string)")
		}
		return m.toolText(value, width)
	case json.Number:
		return m.theme.amber.Render(ansi.Hardwrap(string(value), width, true))
	case bool:
		return m.theme.accent.Render(fmt.Sprint(value))
	default:
		return m.theme.muted.Render("null")
	}
}

// Style complete lines before wrapping so continuations retain their meaning.
// Never replay terminal control sequences supplied by an agent.
func (m *model) toolText(text string, width int) string {
	return m.toolLines(text, width, false)
}

func (m *model) toolLines(text string, width int, diff bool) string {
	rows := strings.Split(strings.ReplaceAll(clean(text), "\t", "    "), "\n")
	for i, row := range rows {
		style := plain
		trimmed := strings.TrimSpace(row)
		lower := strings.ToLower(trimmed)
		word := ""
		if fields := strings.Fields(lower); len(fields) > 0 {
			word = strings.TrimSuffix(fields[0], ":")
		}
		switch {
		case diff && (strings.HasPrefix(row, "--- ") || strings.HasPrefix(row, "+++ ") || strings.HasPrefix(row, "@@")):
			style = m.theme.cyan
		case diff && strings.HasPrefix(row, "+"):
			style = m.theme.mint
		case diff && strings.HasPrefix(row, "-"):
			style = m.theme.danger
		case word == "error", word == "fatal", word == "fail", word == "failed", word == "panic", word == "signal", strings.HasPrefix(lower, "--- fail:"), strings.HasPrefix(lower, "exit:") && lower != "exit: 0":
			style = m.theme.danger
		case word == "warning", word == "warn", strings.HasPrefix(row, "$ "):
			style = m.theme.amber
		case lower == "exit: 0", word == "ok", word == "pass", word == "success", strings.HasPrefix(lower, "--- pass:"):
			style = m.theme.mint
		}
		rows[i] = style.Render(ansi.Hardwrap(row, max(1, width), true))
	}
	return strings.Join(rows, "\n")
}
