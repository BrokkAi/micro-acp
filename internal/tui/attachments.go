package tui

import (
	"context"
	"fmt"
	"io/fs"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"

	tea "charm.land/bubbletea/v2"
	acp "github.com/BrokkAi/acp-go"
)

type filesMsg struct {
	paths []string
	err   error
}

func (m *model) indexFiles() tea.Cmd {
	m.filesLoading = true
	m.filesError = ""
	root, parent := m.options.Cwd, m.ctx
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(parent, 5*time.Second)
		defer cancel()
		// git's file list includes untracked files and honors nested ignore rules.
		b, err := exec.CommandContext(ctx, "git", "-C", root, "ls-files", "--cached", "--others", "--exclude-standard", "-z").Output()
		var paths []string
		if err == nil {
			seen := map[string]bool{}
			for _, p := range strings.Split(string(b), "\x00") {
				if p != "" && filepath.IsLocal(p) && !seen[p] {
					paths = append(paths, p)
					seen[p] = true
				}
				if len(paths) >= 50000 {
					break
				}
			}
		} else {
			err = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
				if parent.Err() != nil {
					return parent.Err()
				}
				if err != nil {
					return nil
				}
				if d.IsDir() {
					switch d.Name() {
					case ".git", "node_modules", ".cache", "vendor", ".venv":
						return filepath.SkipDir
					}
					return nil
				}
				if d.Type().IsRegular() {
					rel, _ := filepath.Rel(root, path)
					paths = append(paths, rel)
				}
				if len(paths) >= 50000 {
					return fs.SkipAll
				}
				return nil
			})
		}
		sort.Strings(paths)
		return filesMsg{paths, err}
	}
}
func quoteReference(path string) string {
	if strings.ContainsAny(path, " \t\n\"'\\") {
		return "@" + strconv.Quote(path)
	}
	return "@" + path
}
func references(text string) ([]string, error) {
	value := []rune(text)
	var paths []string
	for i := 0; i < len(value); i++ {
		if value[i] != '@' || (i > 0 && !unicode.IsSpace(value[i-1])) {
			continue
		}
		start := i + 1
		end := start
		if start < len(value) && (value[start] == '"' || value[start] == '\'') {
			quote := value[start]
			end++
			for end < len(value) && value[end] != quote {
				if value[end] == '\\' && quote == '"' && end+1 < len(value) {
					end++
				}
				end++
			}
			if end == len(value) {
				return nil, fmt.Errorf("close the quote around the @file reference")
			}
			path := string(value[start+1 : end])
			if quote == '"' {
				var err error
				path, err = strconv.Unquote(string(value[start : end+1]))
				if err != nil {
					return nil, err
				}
			}
			paths = append(paths, path)
			i = end
		} else {
			for end < len(value) && !unicode.IsSpace(value[end]) {
				end++
			}
			if end > start {
				paths = append(paths, string(value[start:end]))
			}
			i = end - 1
		}
	}
	return paths, nil
}
func (m *model) promptBlocks(text string) ([]acp.Content, error) {
	blocks := []acp.Content{acp.NewTextContent(text)}
	blocks = append(blocks, m.resources...)
	refs, err := references(text)
	if err != nil {
		return nil, err
	}
	paths := append(append([]string(nil), m.attachments...), refs...)
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
