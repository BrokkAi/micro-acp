package tui

import (
	"io/fs"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"charm.land/bubbles/v2/list"
	tea "charm.land/bubbletea/v2"
	acp "github.com/BrokkAi/acp-go"
)

var fileReference = regexp.MustCompile(`(?:^|\s)@(?:"([^"]+)"|'([^']+)'|([^\s]+))`)

type filesMsg struct {
	entries []list.Item
	prefix  string
	err     error
}

func (m *model) findFiles() tea.Cmd {
	input := m.input.Value()
	at := strings.LastIndex(input, "@")
	if at < 0 {
		return nil
	}
	prefix := input[:at]
	query := strings.Trim(input[at+1:], "\"'")
	root := m.options.Cwd
	m.busy = true
	m.status = "Finding files…"
	return func() tea.Msg {
		var entries []list.Item
		count := 0
		err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			if d.IsDir() {
				switch d.Name() {
				case ".git", "node_modules", ".cache", "vendor":
					return filepath.SkipDir
				}
				return nil
			}
			count++
			if count > 50000 {
				return fs.SkipAll
			}
			rel, _ := filepath.Rel(root, path)
			if strings.Contains(strings.ToLower(rel), strings.ToLower(query)) {
				entries = append(entries, item{title: rel, description: "Attach to the next prompt", id: rel})
			}
			if len(entries) >= 200 {
				return fs.SkipAll
			}
			return nil
		})
		sort.Slice(entries, func(i, j int) bool { return entries[i].(item).id < entries[j].(item).id })
		return filesMsg{entries, prefix, err}
	}
}
func quoteReference(path string) string {
	if strings.ContainsAny(path, " \t\n") {
		return "@" + strconv.Quote(path)
	}
	return "@" + path
}
func (m *model) promptBlocks(text string) ([]acp.Content, error) {
	blocks := []acp.Content{acp.NewTextContent(text)}
	paths := append([]string(nil), m.attachments...)
	for _, match := range fileReference.FindAllStringSubmatch(text, -1) {
		for _, path := range match[1:] {
			if path != "" {
				paths = append(paths, path)
				break
			}
		}
	}
	seen := map[string]bool{}
	for _, path := range paths {
		if seen[path] {
			continue
		}
		seen[path] = true
		block, err := m.client.Attachment(path)
		if err != nil {
			return nil, err
		}
		blocks = append(blocks, block)
	}
	return blocks, nil
}
