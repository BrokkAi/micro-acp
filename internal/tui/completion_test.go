package tui

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/BrokkAi/micro-acp/internal/store"
)

func composer(t *testing.T) *model {
	t.Helper()
	m := newModel(context.Background(), Options{Cwd: t.TempDir()})
	m.input.Focus()
	m.filesLoaded = true
	return m
}
func paste(m *model, s string) { m.Update(tea.PasteMsg{Content: s}) }
func TestSlashCompletionWhileTypingAndEscape(t *testing.T) {
	m := composer(t)
	paste(m, "/mo")
	if m.completion == nil || m.completion.kind != "commands" || len(m.completion.entries) < 2 {
		t.Fatal("slash command suggestions missing")
	}
	m.Update(keyPress(tea.KeyTab, 0))
	if !strings.HasPrefix(m.input.Value(), "/mo") || !strings.HasSuffix(m.input.Value(), " ") {
		t.Fatalf("Tab did not complete: %q", m.input.Value())
	}
	m.input.SetValue("/mo")
	m.input.MoveToEnd()
	m.refreshCompletion()
	m.Update(keyPress(tea.KeyEscape, 0))
	m.Update(pulseMsg{})
	if m.completion != nil || m.input.Value() != "/mo" {
		t.Fatal("Escape changed draft or suggestions reopened")
	}
	paste(m, "d")
	if m.completion == nil {
		t.Fatal("editing did not reopen completion")
	}
}
func TestExactCommandOutranksLongerPrefix(t *testing.T) {
	m := composer(t)
	paste(m, "/mode")
	if m.completion == nil || m.completion.entries[0].id != "/mode" {
		t.Fatal("/mode selected /model")
	}
}
func TestAtCompletionPreservesUnicodeAndSuffix(t *testing.T) {
	m := composer(t)
	m.files = []string{"docs/a file.md", "main.go"}
	m.input.SetValue("Explain 🦊 @docs/a then compare @main.go")
	m.input.MoveToBegin()
	m.input.SetCursorColumn(len([]rune("Explain 🦊 @docs/a")))
	m.refreshCompletion()
	if m.completion == nil || len(m.completion.entries) != 1 {
		t.Fatalf("wrong file suggestions: %+v", m.completion)
	}
	m.Update(keyPress(tea.KeyEnter, 0))
	want := "Explain 🦊 @\"docs/a file.md\" then compare @main.go"
	if m.input.Value() != want {
		t.Fatalf("draft damaged: %q", m.input.Value())
	}
	if m.prompting {
		t.Fatal("file selection sent the prompt")
	}
	refs, err := references(m.input.Value())
	if err != nil || len(refs) != 2 || refs[0] != "docs/a file.md" {
		t.Fatalf("bad references: %v %v", refs, err)
	}
}
func TestAtDoesNotCompleteEmailOrOldReference(t *testing.T) {
	for _, text := range []string{"mail me@example.com", "read @main.go and then", "read @\"a file.md\" next"} {
		m := composer(t)
		paste(m, text)
		if m.completion != nil {
			t.Fatalf("unexpected completion for %q", text)
		}
	}
}
func TestMultilinePasteAndHistoryDraft(t *testing.T) {
	m := composer(t)
	text := strings.Repeat("line\n", 15) + "last"
	paste(m, text)
	if m.input.Value() != text || m.input.Height() != 8 {
		t.Fatalf("multiline input truncated: %d lines, height %d", m.input.LineCount(), m.input.Height())
	}
	m.input.SetValue("unsent draft")
	m.history = []string{"old prompt"}
	m.historyIndex = 1
	m.Update(keyPress(tea.KeyUp, 0))
	if m.input.Value() != "old prompt" {
		t.Fatal("history unavailable")
	}
	m.Update(keyPress(tea.KeyDown, 0))
	if m.input.Value() != "unsent draft" {
		t.Fatal("history lost draft")
	}
}
func TestFileIndexHonorsGitIgnore(t *testing.T) {
	m := composer(t)
	root := m.options.Cwd
	if out, err := exec.Command("git", "init", "-q", root).CombinedOutput(); err != nil {
		t.Fatalf("git init: %v %s", err, out)
	}
	for path, content := range map[string]string{".gitignore": "secret.txt\n", "secret.txt": "hidden", "visible.go": "package main"} {
		if err := os.WriteFile(filepath.Join(root, path), []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	result := m.indexFiles()().(filesMsg)
	if result.err != nil {
		t.Fatal(result.err)
	}
	joined := strings.Join(result.paths, " ")
	if strings.Contains(joined, "secret.txt") || !strings.Contains(joined, "visible.go") {
		t.Fatalf("index ignores .gitignore: %v", result.paths)
	}
}
func TestNormalTerminalAndCompactSuggestions(t *testing.T) {
	m := composer(t)
	m.width, m.height = 80, 24
	paste(m, "/")
	view := m.View()
	if view.AltScreen || view.MouseMode != tea.MouseModeNone || view.Cursor == nil {
		t.Fatal("normal terminal or composer cursor disabled")
	}
	if strings.Count(view.Content, "\n") > 14 {
		t.Fatal("suggestions turned into a full-screen menu")
	}
	if !strings.Contains(view.Content, "Tab complete") {
		t.Fatal("completion controls missing")
	}
}
func TestToolSummaryAndExpandedDetails(t *testing.T) {
	m := composer(t)
	msg := store.Message{Role: "tool", Text: "Run tests\nInput:\nsecret-details"}
	if strings.Contains(m.messageView(msg, false, false), "secret-details") {
		t.Fatal("tool detail flooded transcript")
	}
	if !strings.Contains(m.messageView(msg, true, false), "secret-details") {
		t.Fatal("expanded tool detail missing")
	}
}
func TestStreamingParagraphBoundaries(t *testing.T) {
	text := "Intro.\n\n```go\nfunc main() {\n\n"
	if n := stablePrefix(text); text[:n] != "Intro.\n\n" {
		t.Fatalf("committed unfinished code: %q", text[:n])
	}
}
