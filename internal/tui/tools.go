package tui

import (
	"bytes"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"slices"
	"strconv"
	"strings"
	"sync"

	"charm.land/lipgloss/v2"
	"github.com/BrokkAi/acp-go/schema"
	"github.com/BrokkAi/micro-acp/internal/client"
	"github.com/BrokkAi/micro-acp/internal/store"
	"github.com/aymanbagabas/go-udiff"
	"github.com/charmbracelet/x/ansi"
)

type toolDiff struct {
	path, label      string
	added, removed   int
	counted, renamed bool
}

type toolSummary struct {
	mark    string
	title   string
	diffs   []toolDiff
	preview []string
	failed  bool
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
				summary.failed = true
			}
		}
		for _, part := range tool.Content {
			if part.Diff == nil {
				continue
			}
			label, old := "updated", ""
			if part.Diff.OldText == nil {
				label = "created"
			} else {
				old = *part.Diff.OldText
			}
			added, removed := diffStat(old, part.Diff.NewText)
			summary.diffs = append(summary.diffs, toolDiff{path: strings.ReplaceAll(clean(part.Diff.Path), "\n", " "), label: label, added: added, removed: removed, counted: true})
		}
		for _, change := range message.Changes {
			summary.diffs = append(summary.diffs, toolDiff{path: strings.ReplaceAll(clean(change.Path), "\n", " "), label: clean(change.Label())})
		}
		if len(message.Changes) == 1 && message.Patch != "" && len(summary.diffs) == 1 {
			added, removed := patchStat(message.Patch)
			summary.diffs[0].added, summary.diffs[0].removed, summary.diffs[0].counted = added, removed, true
		}
		summary.preview = toolPreview(*tool, summary.failed)
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

// toolPreview picks the few output lines worth keeping in scrollback: why a
// call failed, or how a command finished. Everything else waits for Ctrl+O.
func toolPreview(tool schema.ToolCall, failed bool) []string {
	execute := tool.Kind != nil && *tool.Kind == schema.ToolKindExecute
	if !failed && !execute {
		return nil
	}
	var lines []string
	for _, part := range tool.Content {
		if part.Content == nil || part.Content.Content.Text == nil {
			continue
		}
		for _, row := range strings.Split(clean(part.Content.Content.Text.Text), "\n") {
			row = strings.TrimRight(strings.ReplaceAll(row, "\t", "    "), " ")
			trimmed := strings.TrimSpace(row)
			if trimmed == "" || strings.HasPrefix(trimmed, "```") || strings.HasPrefix(trimmed, "~~~") {
				continue
			}
			lines = append(lines, row)
		}
	}
	if len(lines) == 0 && failed {
		// Without readable output, a failure may still carry its reason in
		// the raw result.
		var fields map[string]any
		var text string
		if json.Unmarshal(tool.RawOutput, &fields) == nil {
			for _, key := range []string{"error", "message", "stderr"} {
				if value, ok := fields[key].(string); ok && strings.TrimSpace(value) != "" {
					text = value
					break
				}
			}
		} else if json.Unmarshal(tool.RawOutput, &text) != nil {
			text = ""
		}
		for _, row := range strings.Split(clean(text), "\n") {
			if strings.TrimSpace(row) != "" {
				return []string{strings.TrimRight(row, " ")}
			}
		}
	}
	if len(lines) == 0 {
		return nil
	}
	// A failure explains itself up front; a command's verdict comes last.
	if failed {
		return lines[:1]
	}
	return lines[max(0, len(lines)-2):]
}

// toolSignature is the committed-transcript form of summarizeTool.
func toolSignature(message store.Message) string {
	summary := summarizeTool(message)
	rows := []string{summary.mark + " " + summary.title}
	for _, diff := range summary.diffs {
		rows = append(rows, fmt.Sprintf("%s · %s · +%d -%d", diff.path, diff.label, diff.added, diff.removed))
	}
	rows = append(rows, summary.preview...)
	return strings.Join(rows, "\n")
}

var diffStats sync.Map

// diffStat counts changed lines. Committed rows are re-summarized on every
// update, so results are memoized by content.
func diffStat(old, new string) (added, removed int) {
	h := fnv.New64a()
	_, _ = h.Write([]byte(old))
	_, _ = h.Write([]byte{0})
	_, _ = h.Write([]byte(new))
	key := h.Sum64()
	if cached, ok := diffStats.Load(key); ok {
		counts := cached.([2]int)
		return counts[0], counts[1]
	}
	added, removed = patchStat(udiff.Unified("a", "b", old, new))
	diffStats.Store(key, [2]int{added, removed})
	return added, removed
}

func patchStat(patch string) (added, removed int) {
	for _, row := range parsePatch(patch) {
		switch row.kind {
		case '+':
			added++
		case '-':
			removed++
		}
	}
	return added, removed
}

// diffRow is one line of a parsed patch. kind is '+', '-', ' ', '@' for a
// hunk break, or 0 for text outside any hunk.
type diffRow struct {
	kind   byte
	number int
	text   string
}

// parsePatch walks hunks by their declared sizes, so content such as a
// removed "-- comment" line is never mistaken for a file header. Removed
// lines carry their old line number and the rest their new one.
func parsePatch(patch string) []diffRow {
	rows := strings.Split(strings.TrimSuffix(strings.ReplaceAll(patch, "\t", "    "), "\n"), "\n")
	var parsed []diffRow
	oldLine, newLine, oldLeft, newLeft := 0, 0, 0, 0
	for _, row := range rows {
		inHunk := oldLeft > 0 || newLeft > 0
		switch {
		case !inHunk && strings.HasPrefix(row, "@@"):
			hunk, ok := parseHunk(row)
			if !ok {
				parsed = append(parsed, diffRow{text: row})
				continue
			}
			oldLine, newLine, oldLeft, newLeft = hunk[0], hunk[2], hunk[1], hunk[3]
			parsed = append(parsed, diffRow{kind: '@'})
		case !inHunk:
			if !strings.HasPrefix(row, "--- ") && !strings.HasPrefix(row, "+++ ") && !strings.HasPrefix(row, "diff ") && !strings.HasPrefix(row, "index ") {
				parsed = append(parsed, diffRow{text: row})
			}
		case strings.HasPrefix(row, "+"):
			parsed = append(parsed, diffRow{'+', newLine, row[1:]})
			newLine, newLeft = newLine+1, newLeft-1
		case strings.HasPrefix(row, "-"):
			parsed = append(parsed, diffRow{'-', oldLine, row[1:]})
			oldLine, oldLeft = oldLine+1, oldLeft-1
		case strings.HasPrefix(row, `\`):
			parsed = append(parsed, diffRow{text: row})
		default:
			parsed = append(parsed, diffRow{' ', newLine, strings.TrimPrefix(row, " ")})
			oldLine, newLine, oldLeft, newLeft = oldLine+1, newLine+1, oldLeft-1, newLeft-1
		}
	}
	return parsed
}

// diffCounts renders "+3 -1" in diff colors, omitting zero sides.
func (m *model) diffCounts(added, removed int) string {
	var parts []string
	if added > 0 {
		parts = append(parts, m.theme.mint.Render("+"+strconv.Itoa(added)))
	}
	if removed > 0 {
		parts = append(parts, m.theme.danger.Render("-"+strconv.Itoa(removed)))
	}
	return strings.Join(parts, " ")
}

func (m *model) toolView(message store.Message, details bool, width int) string {
	tool := message.Tool
	summary := summarizeTool(message)
	markStyle, titleStyle := m.theme.muted, m.hintKey().Bold(true)
	switch {
	case summary.failed:
		markStyle = m.theme.danger
	case tool != nil && tool.Status != nil && *tool.Status == schema.ToolCallStatusCompleted:
		markStyle = m.theme.mint
	}
	if message.Cancelled {
		markStyle, titleStyle = m.theme.amber, m.theme.amber
	}
	if details {
		// Details wrap the full title; compact scrollback keeps a single row.
		rendered := hang(markStyle.Render(summary.mark)+" ", titleStyle.Render(ansi.Hardwrap(clean(summary.title), width-gutter, true)))
		if message.Cancelled {
			rendered += "\n" + indentTool(m.theme.amber.Render(ansi.Hardwrap("Cancelled by client", width-2, true)), 2)
		}
		if tool == nil {
			return rendered + "\n" + m.toolText(message.Text, width) + "\n"
		}
		return rendered + "\n" + m.toolDetails(*tool, width) + m.fileChanges(message, width) + "\n"
	}
	rendered := markStyle.Render(summary.mark) + " " + titleStyle.Render(line(summary.title, width-gutter))
	child := 0
	connector := func() string {
		child++
		if child == 1 {
			return m.theme.dim.Render("  └ ")
		}
		return "    "
	}
	for _, diff := range summary.diffs {
		path := displayPath(diff.path, m.options.Cwd)
		meta := m.theme.muted.Render(" · " + diff.label)
		if counts := m.diffCounts(diff.added, diff.removed); diff.counted && counts != "" {
			meta += "  " + counts
		}
		// Keep the end of the path: the file name is the useful part.
		row := m.hintKey().Render(truncateLeft(path, max(8, width-4-ansi.StringWidth(meta))))
		rendered += "\n" + connector() + ansi.Truncate(row+meta, width-4, "…")
	}
	previewStyle := m.theme.dim
	if summary.failed {
		previewStyle = m.theme.danger
	}
	for _, row := range summary.preview {
		rendered += "\n" + connector() + previewStyle.Render(line(row, width-4))
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
		path := displayPath(location.Path, m.options.Cwd)
		if location.Line != nil {
			path += fmt.Sprintf(":%d", *location.Line)
		}
		locations = append(locations, m.theme.cyan.Render(ansi.Hardwrap(path, w, true)))
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
			body := m.theme.muted.Render("No textual changes")
			if patch != "" {
				body = m.diffView(patch, w)
			}
			label := "Changes · " + truncateLeft(displayPath(d.Path, m.options.Cwd), max(8, w-10))
			if counts := m.diffCounts(patchStat(patch)); counts != "" {
				label = m.theme.muted.Bold(true).Render(label) + "  " + counts
				sections = append(sections, "  "+label+"\n"+indentTool(body, 4))
				continue
			}
			section(label, body)
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
		rows = append(rows, m.theme.cyan.Render(ansi.Hardwrap(displayPath(change.Path, m.options.Cwd), w, true))+m.theme.muted.Render(" · "+clean(change.Label())))
	}
	body := strings.Join(rows, "\n")
	if message.Patch != "" {
		if body != "" {
			body += "\n"
		}
		body += m.diffView(strings.TrimSuffix(message.Patch, "\n"), w)
	}
	label := m.theme.muted.Bold(true).Render("Changes")
	if counts := m.diffCounts(patchStat(message.Patch)); counts != "" {
		label += "  " + counts
	}
	return "\n\n  " + label + "\n" + indentTool(body, 4)
}

// diffView renders a unified patch with line numbers and tinted added and
// removed rows, like a code review. File headers are dropped because the
// section label already names the file.
func (m *model) diffView(patch string, width int) string {
	parsed := parsePatch(clean(patch))
	widest := 1
	for _, row := range parsed {
		widest = max(widest, row.number)
	}
	digits := len(strconv.Itoa(widest))
	content := max(1, width-digits-3)
	// Without background tints, color the changed text itself instead.
	add, del := on(m.theme.addBg), on(m.theme.delBg)
	tinted := m.theme.addBg != nil
	if !tinted {
		add, del = m.theme.mint, m.theme.danger
	}
	var out []string
	for i, row := range parsed {
		switch row.kind {
		case '@':
			if i > 0 && len(out) > 0 {
				out = append(out, m.theme.dim.Render(strings.Repeat(" ", digits)+" ⋮"))
			}
			continue
		case 0:
			out = append(out, m.theme.dim.Render(ansi.Truncate(row.text, width, "…")))
			continue
		}
		var tint *lipgloss.Style
		signStyle := m.theme.dim
		switch row.kind {
		case '+':
			tint, signStyle = &add, m.theme.mint.Inherit(add)
		case '-':
			tint, signStyle = &del, m.theme.danger.Inherit(del)
		}
		// Long lines wrap under a blank gutter so the numbers stay aligned.
		for j, piece := range strings.Split(ansi.Hardwrap(row.text, content, true), "\n") {
			number, sign := fmt.Sprintf("%*d", digits, row.number), string(row.kind)
			if j > 0 {
				number, sign = strings.Repeat(" ", digits), " "
			}
			prefix := m.theme.dim.Render(number) + " "
			if tint == nil {
				out = append(out, prefix+m.theme.dim.Render(sign+" ")+piece)
				continue
			}
			fill := ""
			if tinted {
				fill = strings.Repeat(" ", max(0, content-ansi.StringWidth(piece)))
			}
			out = append(out, prefix+signStyle.Render(sign+" ")+tint.Render(piece+fill))
		}
	}
	return strings.Join(out, "\n")
}

// parseHunk reads "@@ -a,b +c,d @@" as [a, b, c, d]; omitted sizes are 1.
func parseHunk(row string) ([4]int, bool) {
	var hunk [4]int
	fields := strings.Fields(row)
	if len(fields) < 3 || !strings.HasPrefix(fields[1], "-") || !strings.HasPrefix(fields[2], "+") {
		return hunk, false
	}
	for i, field := range fields[1:3] {
		start, size, found := strings.Cut(field[1:], ",")
		a, err := strconv.Atoi(start)
		if err != nil {
			return hunk, false
		}
		b := 1
		if found {
			if b, err = strconv.Atoi(size); err != nil {
				return hunk, false
			}
		}
		hunk[i*2], hunk[i*2+1] = a, b
	}
	return hunk, true
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
