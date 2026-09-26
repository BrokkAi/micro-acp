package tui

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/BrokkAi/acp-go/agent"
	"github.com/BrokkAi/acp-go/schema"
	"github.com/BrokkAi/micro-acp/internal/client"
	"github.com/BrokkAi/micro-acp/internal/demo"
	"github.com/BrokkAi/micro-acp/internal/store"
	"github.com/charmbracelet/x/ansi"
)

type toolOutputAgent struct{ demo.Agent }

func (a *toolOutputAgent) Prompt(_ context.Context, _ agent.Client, _ schema.PromptRequest, updates agent.SessionUpdater) (schema.PromptResponse, error) {
	status, kind := schema.ToolCallStatusInProgress, schema.ToolKindExecute
	tool := schema.ToolCall{ToolCallID: "run", Title: "Run checks", Status: &status, Kind: &kind, RawInput: json.RawMessage(`{"command":"go test ./...","cwd":"/work"}`)}
	if err := updates.Update(schema.SessionUpdate{ToolCall: &tool}); err != nil {
		return schema.PromptResponse{}, err
	}
	status = schema.ToolCallStatusCompleted
	output, _ := json.Marshal(map[string]any{"stdout": strings.Repeat("ok example/package\n", 30), "exitCode": 0})
	err := updates.Update(schema.SessionUpdate{ToolCallUpdate: &schema.ToolCallUpdate{ToolCallID: "run", Status: &status, RawInput: json.RawMessage("null"), RawOutput: output}})
	return schema.PromptResponse{StopReason: schema.StopReasonEndTurn}, err
}

func TestToolDetailsThroughACPAndKeyboard(t *testing.T) {
	t.Setenv("MICRO_ACP_TUI_TOOLS", "yes")
	m := connectedComposer(t)
	if _, err := m.client.Prompt("tools"); err != nil {
		t.Fatal(err)
	}
	m.syncTranscript()
	if compact := strings.Join(m.printQueue, "\n"); !strings.Contains(compact, m.theme.amber.Bold(true).Render("Run checks")) || strings.Contains(compact, "exitCode") {
		t.Fatalf("compact transcript is unstyled or expanded:\n%s", compact)
	}
	m.input.SetValue("keep this draft")
	m.Update(keyPress('o', tea.ModCtrl))
	if m.page != "details" {
		t.Fatal("Ctrl+O did not open details")
	}
	details := ansi.Strip(m.viewport.GetContent())
	for _, want := range []string{"✓ Run checks", "command: go test ./...", "cwd: /work", "exitCode: 0", "ok example/package"} {
		if !strings.Contains(details, want) {
			t.Errorf("ACP details missing %q:\n%s", want, details)
		}
	}
	m.viewport.GotoTop()
	m.Update(keyPress(tea.KeyPgDown, 0))
	if m.viewport.YOffset() == 0 {
		t.Fatal("expanded tool output did not scroll")
	}
	m.Update(keyPress('o', tea.ModCtrl))
	if m.page != "chat" || m.input.Value() != "keep this draft" {
		t.Fatal("Ctrl+O did not return to the saved draft")
	}
	m.Update(keyPress('o', tea.ModCtrl))
	m.Update(keyPress(tea.KeyEscape, 0))
	if m.page != "chat" || m.input.Value() != "keep this draft" {
		t.Fatal("Esc did not return to the saved draft")
	}
	saved, _ := m.client.Snapshot()
	if input := string(saved.Messages[1].Tool.RawInput); input != `{"command":"go test ./...","cwd":"/work"}` {
		t.Fatalf("presentation changed the original input: %s", input)
	}
}

func toolMessage(t *testing.T, wire string) store.Message {
	t.Helper()
	var tool schema.ToolCall
	if err := json.Unmarshal([]byte(wire), &tool); err != nil {
		t.Fatal(err)
	}
	return store.Message{Role: "tool", Tool: &tool, Text: client.ToolText(tool)}
}

func TestToolDetailsRenderStructuredPayloads(t *testing.T) {
	m := newModel(context.Background(), Options{})
	message := toolMessage(t, `{"toolCallId":"run","title":"Run checks","name":"shell","kind":"execute","status":"completed","locations":[{"path":"src/main.go","line":42}],"rawInput":{"command":"go test ./...\ngo vet ./...","cwd":"/work","options":{"retry":false,"limit":9007199254740993}},"rawOutput":{"stdout":"ok example/core\nPASS","exitCode":0,"extra":[null,{},[],""]}}`)
	rendered := m.messageView(message, true, false)
	text := ansi.Strip(rendered)
	for _, want := range []string{"✓ Run checks", "shell · execute · completed", "Files", "src/main.go:42", "Input", "command:", "go test ./...\n", "go vet ./...", "cwd: /work", "retry: false", "limit: 9007199254740993", "Result", "exitCode: 0", "ok example/core", "PASS", "[1]: null", "(empty object)", "(empty list)", "(empty string)"} {
		if !strings.Contains(text, want) {
			t.Errorf("missing %q:\n%s", want, text)
		}
	}
	for _, unwanted := range []string{`"command"`, `\n`, `"stdout"`, "rawOutput", "rawInput"} {
		if strings.Contains(text, unwanted) {
			t.Errorf("debug serialization leaked: %q:\n%s", unwanted, text)
		}
	}
	if !strings.Contains(rendered, m.theme.mint.Render("PASS")) {
		t.Fatal("successful output has no semantic color")
	}
}

func TestToolDetailsRenderContentAndUnifiedDiff(t *testing.T) {
	m := newModel(context.Background(), Options{})
	message := toolMessage(t, `{"toolCallId":"edit","title":"Update greeting","kind":"edit","status":"completed","content":[{"type":"diff","path":"main.go","oldText":"package main\n\nvar greeting = \"old\"\n","newText":"package main\n\nvar greeting = \"new\"\n"},{"type":"content","content":{"type":"text","text":"{\"summary\":\"Updated greeting\",\"count\":1}"}},{"type":"content","content":{"type":"image","mimeType":"image/png","data":"secret-base64"}},{"type":"terminal","terminalId":"term-1"}]}`)
	rendered := m.messageView(message, true, false)
	text := ansi.Strip(rendered)
	for _, want := range []string{"Changes · main.go", "--- main.go", "+++ main.go", "@@", " package main", `-var greeting = "old"`, `+var greeting = "new"`, "summary: Updated greeting", "count: 1", "[image: image/png]", "Terminal", "term-1"} {
		if !strings.Contains(text, want) {
			t.Errorf("missing %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "secret-base64") || strings.Contains(text, "-package main") || strings.Contains(text, "+package main") {
		t.Fatalf("binary data exposed or unchanged lines marked as edits:\n%s", text)
	}
	if !strings.Contains(rendered, m.theme.mint.Render(`+var greeting = "new"`)) || !strings.Contains(rendered, m.theme.danger.Render(`-var greeting = "old"`)) {
		t.Fatal("diff additions and removals lack distinct colors")
	}
	compact := m.messageView(message, false, false)
	if strings.Contains(compact, "greeting =") || !strings.Contains(compact, m.theme.cyan.Render("main.go")) {
		t.Fatalf("compact view lost colored file summary: %s", compact)
	}
}

func TestToolPresentationColorsKindsAndStates(t *testing.T) {
	for _, dark := range []bool{true, false} {
		m := newModel(context.Background(), Options{})
		m.applyTheme(dark)
		for _, tt := range []struct {
			kind, status string
			cancelled    bool
			style        lipgloss.Style
		}{
			{"read", "completed", false, m.theme.cyan},
			{"execute", "in_progress", false, m.theme.amber},
			{"edit", "completed", false, m.theme.accent},
			{"delete", "pending", false, m.theme.danger},
			{"read", "failed", false, m.theme.danger},
			{"execute", "in_progress", true, m.theme.amber},
		} {
			message := toolMessage(t, fmt.Sprintf(`{"toolCallId":"test","title":"Work","kind":%q,"status":%q}`, tt.kind, tt.status))
			message.Cancelled = tt.cancelled
			title := "Work"
			if tt.cancelled {
				title += " · cancelled"
			}
			if got := m.messageView(message, false, false); !strings.Contains(got, tt.style.Bold(true).Render(title)) {
				t.Errorf("dark=%v kind=%s status=%s: missing title color: %q", dark, tt.kind, tt.status, got)
			}
		}
	}
}

func TestToolDetailsWrapAndSanitizeWithoutLosingValues(t *testing.T) {
	m := newModel(context.Background(), Options{})
	message := toolMessage(t, `{"toolCallId":"test","title":"A long tool title with 日本語 and emoji 🛠","name":"long-tool-name","rawInput":{"very_long_field_name":{"nested":{"items":["a very long value that must not disappear",false,0]}}},"rawOutput":"error: unsafe\u001b]52;c;secret\u0007\u001b[31m output\n\tindented line"}`)
	for _, width := range []int{20, 35, 80} {
		m.width = width
		got := m.messageView(message, true, false)
		if lipgloss.Width(got) > width-4 {
			t.Errorf("view width %d exceeds %d:\n%s", lipgloss.Width(got), width-4, got)
		}
		flat := strings.Join(strings.Fields(ansi.Strip(got)), "")
		if !strings.Contains(flat, "averylongvaluethatmustnotdisappear") || strings.Contains(got, "secret") || strings.Contains(got, "\x1b]52") {
			t.Fatalf("lost content or leaked terminal controls at width %d: %q", width, got)
		}
	}
}

func TestToolRawHandlesMissingAndNonObjectValues(t *testing.T) {
	m := newModel(context.Background(), Options{})
	for _, tt := range []struct{ raw, want string }{
		{"", ""}, {"null", ""}, {"  null\n", ""}, {"false", "false"}, {"0", "0"},
		{`"first\nsecond"`, "first\nsecond"}, {"unfinished {", "unfinished {"},
	} {
		if got := ansi.Strip(m.toolRaw(json.RawMessage(tt.raw), 70)); got != tt.want {
			t.Errorf("raw %q: got %q, want %q", tt.raw, got, tt.want)
		}
	}
}
