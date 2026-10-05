package tui

import (
	"context"
	"image/color"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/BrokkAi/acp-go/schema"
	"github.com/BrokkAi/micro-acp/internal/store"
	"github.com/charmbracelet/colorprofile"
	"github.com/charmbracelet/x/ansi"
)

func TestMarkdownHasNoPaddingMarkersOrStrayBlankLines(t *testing.T) {
	m := newModel(context.Background(), Options{})
	text := "## Summary\n\nRun `go test` and see [the docs](https://example.com/docs).\n\n- one\n- two\n\n> quoted"
	rendered := m.markdown(text, 60)
	plainText := ansi.Strip(rendered)
	for i, row := range strings.Split(plainText, "\n") {
		if strings.TrimRight(row, " ") != row {
			t.Errorf("line %d keeps trailing padding: %q", i, row)
		}
	}
	if strings.HasPrefix(plainText, "\n") || strings.HasSuffix(plainText, "\n") || strings.Contains(plainText, "\n\n\n") {
		t.Fatalf("blank-line padding remains:\n%s", plainText)
	}
	for _, unwanted := range []string{"##", "`"} {
		if strings.Contains(plainText, unwanted) {
			t.Errorf("markdown marker %q leaked:\n%s", unwanted, plainText)
		}
	}
	if !strings.HasPrefix(plainText, "Summary") || !strings.Contains(plainText, "• one") || !strings.Contains(plainText, "│ quoted") {
		t.Fatalf("unexpected structure:\n%s", plainText)
	}
}

func TestAssistantTextHangsUnderItsBullet(t *testing.T) {
	m := newModel(context.Background(), Options{})
	m.width = 40
	text := "This reply is long enough to wrap onto several lines in a narrow window.\n\n- a list item that is also long enough to wrap onto another line"
	rows := strings.Split(strings.TrimRight(ansi.Strip(m.messageView(store.Message{Role: "assistant", Text: text}, false, false)), "\n"), "\n")
	if !strings.HasPrefix(rows[0], "● This") {
		t.Fatalf("bullet and text are misaligned: %q", rows[0])
	}
	sawItem := false
	for _, row := range rows[1:] {
		if row == "" {
			continue
		}
		if !strings.HasPrefix(row, "  ") || strings.HasPrefix(row, "   ") && !sawItem {
			t.Fatalf("continuation does not hang at the text column: %q\n%s", row, strings.Join(rows, "\n"))
		}
		if strings.HasPrefix(row, "  • ") {
			sawItem = true
		} else if sawItem && !strings.HasPrefix(row, "    ") {
			t.Fatalf("wrapped list item does not hang under its text: %q\n%s", row, strings.Join(rows, "\n"))
		}
		if ansi.StringWidth(row) >= m.width {
			t.Fatalf("row reaches the last column: %q", row)
		}
	}
	if !sawItem {
		t.Fatal("list item missing")
	}
	continued := ansi.Strip(m.messageView(store.Message{Role: "assistant", Text: "More text."}, false, true))
	if !strings.HasPrefix(continued, "  More") {
		t.Fatalf("continuation chunk drew a second bullet: %q", continued)
	}
}

func TestUserTurnTintFitsItsText(t *testing.T) {
	m := newModel(context.Background(), Options{})
	m.width = 80
	// A terminal that never reports its background gets no tint at all:
	// the guess could put the terminal's own text on a clashing band.
	if view := m.messageView(store.Message{Role: "user", Text: "short prompt"}, false, false); strings.Contains(view, "48;") {
		t.Fatalf("tinted without a known background: %q", view)
	}
	m.Update(tea.BackgroundColorMsg{Color: color.RGBA{R: 20, G: 24, B: 31, A: 255}})
	view := m.messageView(store.Message{Role: "user", Text: "short prompt"}, false, false)
	first := strings.Split(view, "\n")[0]
	if got := ansi.StringWidth(first); got != ansi.StringWidth("❯ short prompt")+1 {
		t.Fatalf("user tint is %d cells wide, want it sized to the text: %q", got, ansi.Strip(first))
	}
	if !strings.Contains(first, "48;2;") {
		t.Fatal("user turn has no background tint")
	}
	m.profile = colorprofile.ANSI
	m.applyTheme(true)
	if view := m.messageView(store.Message{Role: "user", Text: "short prompt"}, false, false); strings.Contains(view, "\x1b[4") || strings.Contains(view, "48;") {
		t.Fatalf("16-color terminals got a background block: %q", view)
	}
}

func TestDiffViewNumbersLinesAndKeepsHeaderLikeContent(t *testing.T) {
	m := newModel(context.Background(), Options{})
	patch := "--- a/q.sql\n+++ b/q.sql\n@@ -9,3 +9,3 @@\n select 1;\n--- old comment\n+-- new comment\n end;\n@@ -40 +40 @@\n-x\n+y\n"
	text := ansi.Strip(m.diffView(patch, 60))
	for _, want := range []string{" 9   select 1;", "10 - -- old comment", "10 + -- new comment", "11   end;", "⋮", "40 - x", "40 + y"} {
		if !strings.Contains(text, want) {
			t.Errorf("missing %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "a/q.sql") || strings.Contains(text, "@@") {
		t.Fatalf("patch headers leaked:\n%s", text)
	}
	if added, removed := patchStat(patch); added != 2 || removed != 2 {
		t.Fatalf("counted +%d -%d, want +2 -2", added, removed)
	}
}

func TestHintsDropWholePairsAndFitWidth(t *testing.T) {
	m := newModel(context.Background(), Options{})
	pairs := []string{"↑↓", "navigate", "enter", "select", "esc", "close"}
	for _, width := range []int{8, 20, 28, 80} {
		got := ansi.Strip(m.hints(width, pairs...))
		if ansi.StringWidth(got) > width {
			t.Fatalf("width %d: %q overflows", width, got)
		}
		if strings.HasSuffix(got, "·") || strings.HasSuffix(got, " ") || strings.HasSuffix(got, "…") && width >= 11 {
			t.Fatalf("width %d: %q ends badly", width, got)
		}
	}
	if got := ansi.Strip(m.hints(28, pairs...)); got != "↑↓ navigate · enter select" {
		t.Fatalf("pairs were not dropped whole: %q", got)
	}
	for _, width := range []int{10, 30, 95} {
		if got := m.titleRule("Choose an agent", "9/44", width, m.theme.accent); ansi.StringWidth(got) != width {
			t.Fatalf("rule is %d wide, want %d: %q", ansi.StringWidth(got), width, ansi.Strip(got))
		}
	}
}

func TestSmallFormatters(t *testing.T) {
	for d, want := range map[time.Duration]string{3 * time.Second: "3s", 65 * time.Second: "1m 05s", 3725 * time.Second: "1h 02m"} {
		if got := elapsed(d); got != want {
			t.Errorf("elapsed(%v) = %q, want %q", d, got, want)
		}
	}
	now := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
	for t0, want := range map[time.Time]string{now.Add(-10 * time.Second): "just now", now.Add(-5 * time.Minute): "5m ago", now.Add(-3 * time.Hour): "3h ago", now.Add(-50 * time.Hour): "2d ago"} {
		if got := ago(t0, now); got != want {
			t.Errorf("ago = %q, want %q", got, want)
		}
	}
	root := filepath.Join(t.TempDir(), "repo")
	if got := displayPath(filepath.Join(root, "internal", "x.go"), root); got != "internal/x.go" {
		t.Errorf("workspace path not relative: %q", got)
	}
	if got := displayPath(filepath.Join(filepath.Dir(root), "elsewhere", "x.go"), root); strings.HasPrefix(got, "..") || strings.HasPrefix(got, "elsewhere") {
		t.Errorf("outside path shown relative to the workspace: %q", got)
	}
	if got := truncateLeft("internal/tui/very/long/path.go", 12); got != "…ong/path.go" || ansi.StringWidth(got) != 12 {
		t.Errorf("truncateLeft = %q", got)
	}
}

func TestScrollbackMatchesTerminalColorsAndWaitsForTheme(t *testing.T) {
	m := newModel(context.Background(), Options{Cwd: "/tmp/workspace"})
	m.Init()
	m.queueOutput(m.theme.accent.Render("early"))
	if cmd := m.flushOutput(); cmd != nil || m.printing {
		t.Fatal("scrollback printed before the theme was known")
	}
	m.Update(tea.ColorProfileMsg{Profile: colorprofile.ANSI256})
	if !m.headerPending || len(m.printQueue) != 1 {
		t.Fatal("scrollback printed before the background was known")
	}
	m.printHeader()
	if len(m.printQueue) != 2 || !strings.Contains(ansi.Strip(m.printQueue[0]), "micro-acp") || ansi.Strip(m.printQueue[1]) != "early" {
		t.Fatalf("header is not printed first: %q", m.printQueue)
	}
	m.Update(tea.BackgroundColorMsg{Color: color.RGBA{R: 250, G: 250, B: 250, A: 255}})
	if m.theme.dark || !m.printing || len(m.printQueue) != 0 {
		t.Fatal("light background not applied or scrollback still held")
	}
	if got := m.downsample(m.theme.accent.Render("x")); strings.Contains(got, "38;2;") || !strings.Contains(got, "38;5;") {
		t.Fatalf("256-color scrollback kept truecolor sequences: %q", got)
	}
	m.profile = colorprofile.TrueColor
	if got := m.downsample(m.theme.accent.Render("x")); !strings.Contains(got, "38;2;") {
		t.Fatalf("truecolor scrollback was downsampled: %q", got)
	}
}

func TestStatusLineNamesTheRunningTool(t *testing.T) {
	status := schema.ToolCallStatusInProgress
	messages := []store.Message{{Role: "user", Text: "go"}, {Role: "tool", Tool: &schema.ToolCall{Title: "go test ./...", Status: &status}}}
	if got := activity(messages, nil); got != "go test ./..." {
		t.Fatalf("activity = %q", got)
	}
	done := schema.ToolCallStatusCompleted
	messages[1].Tool.Status = &done
	plan := &schema.Plan{Entries: []schema.PlanEntry{{Content: "Write docs", Status: schema.PlanEntryStatusInProgress}}}
	if got := activity(messages, plan); got != "Write docs" {
		t.Fatalf("activity = %q", got)
	}
	m := newModel(context.Background(), Options{})
	m.busy, m.status, m.activity, m.startedAt = true, "Working", "go test ./...", time.Now()
	if view := ansi.Strip(m.View().Content); !strings.Contains(view, "go test ./... (0s · esc to interrupt)") {
		t.Fatalf("status line does not name the running tool:\n%s", view)
	}
}

func TestFooterSharesOneRowWhenItFits(t *testing.T) {
	m := connectedComposer(t)
	m.width = 140
	footer := ansi.Strip(m.footer(m.lineWidth(), false))
	if strings.Contains(footer, "\n") || !strings.Contains(footer, "/config") || !strings.Contains(footer, "ctrl+o details") {
		t.Fatalf("wide footer is not one row: %q", footer)
	}
	m.width = 60
	if footer := ansi.Strip(m.footer(m.lineWidth(), false)); !strings.Contains(footer, "\n") || lipgloss.Width(footer) > m.lineWidth() {
		t.Fatalf("narrow footer did not split into rows: %q", footer)
	}
	if footer := ansi.Strip(m.footer(m.lineWidth(), true)); strings.Contains(footer, "commands") {
		t.Fatalf("modal footer repeats general hints: %q", footer)
	}
}
