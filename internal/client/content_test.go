package client

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/BrokkAi/acp-go/schema"
)

func TestToolRawUpdateNullSemantics(t *testing.T) {
	for _, value := range []string{"", "null", " \n null \t ", "false", "0", `""`, "[]", "{}", `{"new":true}`} {
		t.Run(value, func(t *testing.T) {
			original := json.RawMessage(`{"original":true}`)
			old := schema.ToolCall{ToolCallID: "tool", Title: "Run", RawInput: original, RawOutput: original}
			wire := `{"toolCallId":"tool"}`
			if value != "" {
				wire = `{"toolCallId":"tool","rawInput":` + value + `,"rawOutput":` + value + `}`
			}
			var patch schema.ToolCallUpdate
			if err := json.Unmarshal([]byte(wire), &patch); err != nil {
				t.Fatal(err)
			}
			updated := mergeTool(old, patch)
			want := strings.TrimSpace(value)
			if want == "" || want == "null" {
				want = string(original)
			}
			if string(updated.RawInput) != want || string(updated.RawOutput) != want {
				t.Fatalf("input=%s output=%s, want %s", updated.RawInput, updated.RawOutput, want)
			}
			if string(old.RawInput) != string(original) || string(old.RawOutput) != string(original) {
				t.Fatal("update mutated the previous snapshot")
			}
		})
	}
}

func TestNullToolValuesHaveNoPresentation(t *testing.T) {
	tool := schema.ToolCall{Title: "Run", RawInput: json.RawMessage("null"), RawOutput: json.RawMessage(" \n null ")}
	if text := ToolText(tool); text != "Run" {
		t.Fatalf("null values presented as tool input/output: %s", text)
	}
	tool.RawInput, tool.RawOutput = json.RawMessage("false"), json.RawMessage("0")
	if text := ToolText(tool); !strings.Contains(text, "Input:\nfalse") || !strings.Contains(text, "Output:\n0") {
		t.Fatalf("valid zero values hidden: %s", text)
	}
}
