package client

import "testing"

func TestV1ConfigOptionsAcceptsBothIDKeys(t *testing.T) {
	options := v1ConfigOptions([]any{
		map[string]any{"configId": "mode", "name": "Mode", "type": "select", "currentValue": "a", "options": []any{map[string]any{"groupId": "g", "name": "Group", "options": []any{map[string]any{"value": "a", "name": "A"}}}}},
		// codegraff 0.0.302 uses the v1 key on v2 connections.
		map[string]any{"id": "thought_level", "name": "Thought", "type": "select", "currentValue": "low", "options": []any{map[string]any{"value": "low", "name": "Low"}}},
		map[string]any{"configId": "fast", "name": "Fast", "type": "boolean", "currentValue": true},
	})
	if len(options) != 3 || options[0].ID != "mode" || options[1].ID != "thought_level" || options[2].Boolean == nil || !options[2].Boolean.CurrentValue {
		t.Fatalf("options = %+v", options)
	}
	choices := selectChoices(options[0].Select.Options)
	if len(choices) != 1 || choices[0].Group != "Group" || choices[0].Value != "a" {
		t.Fatalf("grouped choices = %+v", choices)
	}
}
