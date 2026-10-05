package tui

import (
	"context"
	"image/color"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/BrokkAi/acp-go/schema"
	"github.com/BrokkAi/micro-acp/internal/store"
	"github.com/charmbracelet/x/ansi"
)

func TestTranscriptWaitsForTheThemeBeforeRendering(t *testing.T) {
	m := connectedComposer(t)
	m.Init()
	m.printing = true // Hold the queue so it can be inspected.
	m.syncTranscript()
	if len(m.printQueue) != 0 {
		t.Fatalf("transcript rendered before the background was known: %q", m.printQueue)
	}
	m.printWarning("registry unreachable")
	m.Update(tea.BackgroundColorMsg{Color: color.RGBA{R: 255, G: 255, B: 255, A: 255}})
	printed := strings.Join(m.printQueue, "\n")
	if !strings.Contains(ansi.Strip(printed), "registry unreachable") || !strings.Contains(ansi.Strip(printed), "New session") {
		t.Fatalf("held output missing: %q", printed)
	}
	if strings.Contains(printed, "38;2;146;158;175") || !strings.Contains(printed, "38;2;100;116;139") {
		t.Fatalf("held output used the dark palette: %q", printed)
	}
}

func TestStopMarkerFollowsTheTurnWhileASteerIsPending(t *testing.T) {
	m := connectedComposer(t)
	m.syncTranscript()
	m.printQueue, m.printing = nil, true
	m.busy, m.prompting = true, true
	if _, err := m.client.Prompt("hello"); err != nil {
		t.Fatal(err)
	}
	m.steering = &queuedPrompt{id: 99}
	m.Update(resultMsg{status: "Ready", prompt: true, reason: schema.StopReasonMaxTokens})
	printed := ansi.Strip(strings.Join(m.printQueue, "\n"))
	reply, marker := strings.Index(printed, "local demo agent"), strings.Index(printed, "└ Stopped · token limit reached")
	if reply < 0 || marker < reply || !m.busy {
		t.Fatalf("stop marker precedes the reply or steering no longer holds the client:\n%s", printed)
	}
	m.printQueue = nil
	m.steering, m.busy = nil, false
	m.Update(resultMsg{status: "Ready", prompt: true, reason: schema.StopReason("x\x1b]52;c;ZXZpbA==\a\x1b[2J" + strings.Repeat("y", 200))})
	for _, entry := range m.printQueue {
		if strings.Contains(entry, "\x1b]52") || strings.Contains(entry, "\x1b[2J") || ansi.StringWidth(entry) > m.lineWidth() {
			t.Fatalf("agent stop reason reached scrollback raw: %q", entry)
		}
	}
}

func TestSubagentReprintShowsAChangedTask(t *testing.T) {
	m := newModel(context.Background(), Options{})
	child := store.Subagent{ID: "c", Title: "Survey", State: store.SubagentRunning}
	m.subagents["c"] = child
	message := store.Message{Role: "subagent", ID: "c"}
	before := m.subagentSignature(message)
	child.Description = "Look around"
	m.subagents["c"] = child
	after := m.subagentSignature(message)
	if subagentTask(before) == subagentTask(after) || subagentTask(after) != "Look around" {
		t.Fatalf("task change not detected: %q -> %q", before, after)
	}
	m.reprinting = false
	if view := ansi.Strip(m.messageView(message, false, false)); !strings.Contains(view, "Look around") {
		t.Fatalf("changed task not shown: %q", view)
	}
	m.reprinting = true
	if view := ansi.Strip(m.messageView(message, false, false)); strings.Contains(view, "Look around") {
		t.Fatalf("unchanged task repeated: %q", view)
	}
}

func TestMarkdownCannotEmitTerminalControls(t *testing.T) {
	m := newModel(context.Background(), Options{})
	for _, text := range []string{"Done. &#27;[2J&#27;[3J&#27;[H", "Hidden &#27;[8m secret", "Title &#27;]0;pwned&#7; &#x1B;]52;c;ZXZpbA==&#7;", "8-bit &#155;2J"} {
		got := m.markdown(text, 60)
		for _, bad := range []string{"\x1b[2J", "\x1b[3J", "\x1b[H", "\x1b[8m", "\x1b]0;", "\x1b]52", "\u009b"} {
			if strings.Contains(got, bad) {
				t.Fatalf("%q produced control %q: %q", text, bad, got)
			}
		}
	}
	link := m.markdown("See [docs](https://example.com/docs).", 60)
	if !strings.Contains(link, "\x1b]8;") || !strings.Contains(ansi.Strip(link), "docs") {
		t.Fatalf("hyperlinks or text were lost: %q", link)
	}
}

func TestListHangingLeavesOtherTextAlone(t *testing.T) {
	m := newModel(context.Background(), Options{})
	cases := map[string][]string{
		"```text\n1. go build ./...\ngo test ./...\ngo vet ./...\n```": {"1. go build ./...", "go test ./...", "go vet ./..."},
		"✓ Build passed\n✓ Tests passed\nAll good, ready to merge.":    {"✓ Build passed", "✓ Tests passed", "All good, ready to merge."},
		"- **Status:** done\n  Next: deploy":                           {"• Status: done", "  Next: deploy"},
	}
	for text, want := range cases {
		rows := strings.Split(ansi.Strip(m.markdown(text, 80)), "\n")
		for _, w := range want {
			found := false
			for _, row := range rows {
				if strings.TrimLeft(row, " ") == strings.TrimLeft(w, " ") && (!strings.HasPrefix(w, "  ") || strings.HasPrefix(row, w)) {
					found = true
				}
			}
			if !found {
				t.Errorf("%q: missing row %q in:\n%s", text, w, strings.Join(rows, "\n"))
			}
		}
	}
	path := "- run with /usr/local/share/some/really/long/directory/name/file.txt and check the result"
	got := ansi.Strip(m.markdown(path, 40))
	if strings.Contains(got, "ct ory") || strings.Contains(got, "dire ct") {
		t.Fatalf("hanging inserted spaces into a long word:\n%s", got)
	}
	for _, row := range strings.Split(got, "\n") {
		if ansi.StringWidth(row) > 40 {
			t.Fatalf("hung row exceeds the width: %q", row)
		}
	}
	if strings.Contains(m.markdown("text with  inside", 40), "") {
		t.Fatal("list marker sentinel leaked from input")
	}
}

func TestNarrowLayoutsStayWithinBounds(t *testing.T) {
	m := newModel(context.Background(), Options{})
	m.Update(tea.WindowSizeMsg{Width: 24, Height: 10})
	m.busy, m.status, m.activity, m.startedAt = true, "Working", "go test ./...", time.Now().Add(-65*time.Minute)
	if status := ansi.Strip(m.statusLine(m.lineWidth())); !strings.Contains(status, "go test") {
		t.Fatalf("narrow status line lost its label: %q", status)
	}
	m.lastError = strings.Repeat("an error happened and it is long ", 5)
	m.attachments = []string{"a", "b"}
	m.busy = false
	for _, row := range strings.Split(m.View().Content, "\n") {
		if ansi.StringWidth(row) > 24 {
			t.Fatalf("row exceeds 24 cells: %q", ansi.Strip(row))
		}
	}
	m.lastError, m.attachments = "", nil
	m.openPicker("agents", m.agents)
	if rows := strings.Split(ansi.Strip(m.View().Content), "\n"); strings.TrimSpace(rows[len(rows)-1]) == "" {
		t.Fatal("modal view ends with an empty footer row")
	}
}
