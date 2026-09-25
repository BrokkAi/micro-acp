package tui

import (
	"fmt"
	"sort"
	"strings"
	"unicode"

	tea "charm.land/bubbletea/v2"
)

type completion struct {
	kind, query, signature string
	title                  string
	entries                []item
	index, start, end      int // rune offsets, never byte offsets
}

func (m *model) cursorOffset() int {
	lines := strings.Split(m.input.Value(), "\n")
	row := m.input.Line()
	offset := 0
	for i := 0; i < row && i < len(lines); i++ {
		offset += len([]rune(lines[i])) + 1
	}
	info := m.input.LineInfo()
	return min(len([]rune(m.input.Value())), offset+info.StartColumn+info.ColumnOffset)
}
func (m *model) replaceRange(start, end int, replacement string) {
	value := []rune(m.input.Value())
	replacementRunes := []rune(replacement)
	next := append(append(append([]rune{}, value[:start]...), replacementRunes...), value[end:]...)
	m.input.SetValue(string(next))
	m.input.MoveToBegin()
	prefix := string(next[:start+len(replacementRunes)])
	row := strings.Count(prefix, "\n")
	for i := 0; i < row; i++ {
		m.input.CursorDown()
	}
	lines := strings.Split(prefix, "\n")
	m.input.SetCursorColumn(len([]rune(lines[len(lines)-1])))
}
func (m *model) refreshCompletion() tea.Cmd {
	if m.picker != nil || m.permission != nil || m.elicitation != nil || m.page != "chat" {
		m.completion = nil
		return nil
	}
	value := []rune(m.input.Value())
	cursor := m.cursorOffset()
	next := &completion{}
	if start, end, query, ok := referenceAt(value, cursor); ok {
		next.kind, next.start, next.end, next.query = "files", start, end, query
		for _, path := range m.files {
			next.entries = append(next.entries, item{title: path, id: path})
		}
	} else if len(value) > 0 && value[0] == '/' && !strings.ContainsAny(string(value[:cursor]), "\n\t") {
		before := string(value[:cursor])
		name, arg, hasArg := strings.Cut(before, " ")
		if !hasArg {
			next.kind, next.start, next.end, next.query = "commands", 0, cursor, strings.TrimPrefix(before, "/")
			for next.end < len(value) && !unicode.IsSpace(value[next.end]) {
				next.end++
			}
			next.entries = m.allCommands()
		} else if m.client != nil && (name == "/model" || name == "/mode" || name == "/effort") {
			category := strings.TrimPrefix(name, "/")
			if category == "effort" {
				category = "thought_level"
			}
			next.kind, next.start, next.end, next.query = "values", len([]rune(name))+1, len(value), arg
			for _, s := range m.client.Selectors() {
				if s.Category == category {
					next.title = s.Name
					next.entries = append(next.entries, choiceItems(s)...)
				}
			}
		} else if m.client != nil && (name == "/config" || name == "/settings") {
			id, query, hasValue := strings.Cut(arg, " ")
			if !hasValue {
				next.kind, next.title = "settings", "Session configuration"
				next.start, next.end, next.query = len([]rune(name))+1, cursor, id
				for next.end < len(value) && !unicode.IsSpace(value[next.end]) {
					next.end++
				}
				next.entries = settingItems(m.client.Selectors(), "")
			} else {
				for _, s := range m.client.Selectors() {
					if s.ID != id {
						continue
					}
					next.kind, next.title = "values", s.Name
					next.start, next.end, next.query = len([]rune(name))+len([]rune(id))+2, len(value), query
					next.entries = choiceItems(s)
				}
			}
		}
	}
	if next.kind == "" {
		m.completion = nil
		m.dismissedCompletion = ""
		return nil
	}
	next.signature = fmt.Sprintf("%s:%d:%d:%s:%s", next.kind, next.start, cursor, next.query, string(value))
	if next.signature == m.dismissedCompletion {
		m.completion = nil
		return nil
	}
	next.entries = filterItems(next.entries, next.query)
	if next.kind == "commands" || next.kind == "settings" || next.kind == "values" {
		// Exact commands and command-name prefixes outrank matches in prose.
		query := strings.ToLower(strings.TrimSpace(next.query))
		rank := func(e item) int {
			name := strings.ToLower(strings.TrimSpace(strings.TrimPrefix(e.id, "/")))
			if name == query {
				return 0
			}
			if strings.HasPrefix(name, query) {
				return 1
			}
			return 2
		}
		sort.SliceStable(next.entries, func(i, j int) bool { return rank(next.entries[i]) < rank(next.entries[j]) })
		// Do not surface unrelated commands just because their description
		// contains the letters of a short command prefix.
		if len(next.entries) > 0 && rank(next.entries[0]) < 2 {
			end := 0
			for end < len(next.entries) && rank(next.entries[end]) < 2 {
				end++
			}
			next.entries = next.entries[:end]
		}
	}
	if m.completion != nil && next.signature == m.completion.signature {
		next.index = min(m.completion.index, max(0, len(next.entries)-1))
	}
	m.completion = next
	if next.kind == "files" && !m.filesLoaded && !m.filesLoading {
		return m.indexFiles()
	}
	return nil
}
func (m *model) completionKey(msg tea.KeyPressMsg) (bool, tea.Cmd) {
	c := m.completion
	if c == nil {
		return false, nil
	}
	switch msg.String() {
	case "esc":
		m.dismissedCompletion = c.signature
		m.completion = nil
		return true, nil
	case "up":
		c.index = max(0, c.index-1)
		return true, nil
	case "down":
		c.index = min(max(0, len(c.entries)-1), c.index+1)
		return true, nil
	case "tab", "enter":
		if len(c.entries) == 0 {
			return c.kind == "files" || msg.String() == "tab", nil
		}
		entry := c.entries[c.index]
		m.completion = nil
		switch c.kind {
		case "files":
			replacement := quoteReference(entry.id)
			value := []rune(m.input.Value())
			if c.end == len(value) || !unicode.IsSpace(value[c.end]) {
				replacement += " "
			}
			m.replaceRange(c.start, c.end, replacement)
			m.dismissedCompletion = ""
			return true, nil
		case "commands":
			if msg.String() == "tab" || entry.arguments {
				m.replaceRange(c.start, c.end, strings.TrimSpace(entry.id)+" ")
				return true, nil
			}
			// Enter executes the selected command. It never sends a second prompt by accident.
			m.input.Reset()
			return true, m.command(entry.id)
		case "values":
			m.replaceRange(c.start, c.end, entry.id+" ")
			if msg.String() == "enter" {
				text := m.input.Value()
				m.input.Reset()
				return true, m.command(text)
			}
			return true, nil
		case "settings":
			m.replaceRange(c.start, c.end, entry.id+" ")
			return true, nil
		}
	}
	return false, nil
}

// referenceAt finds only the @ token under the cursor. Email addresses and
// completed references elsewhere in the draft cannot steal completion focus.
func referenceAt(value []rune, cursor int) (start, end int, query string, ok bool) {
	for start = cursor - 1; start >= 0; start-- {
		if value[start] != '@' || (start > 0 && !unicode.IsSpace(value[start-1])) {
			continue
		}
		end = start + 1
		quote := rune(0)
		if end < len(value) && (value[end] == '"' || value[end] == '\'') {
			quote = value[end]
			end++
		}
		contentStart := end
		for end < len(value) {
			if quote != 0 {
				if value[end] == '\\' && quote == '"' && end+1 < len(value) {
					end += 2
					continue
				}
				if value[end] == quote {
					break
				}
			} else if unicode.IsSpace(value[end]) {
				break
			}
			end++
		}
		contentEnd := end
		if quote != 0 && end < len(value) && value[end] == quote {
			end++
		}
		if cursor < contentStart || cursor > contentEnd {
			return 0, 0, "", false
		}
		return start, end, string(value[contentStart:cursor]), true
	}
	return 0, 0, "", false
}
