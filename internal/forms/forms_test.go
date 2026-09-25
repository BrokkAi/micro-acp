package forms

import (
	"encoding/json"
	"testing"

	"github.com/BrokkAi/acp-go/schema"
)

func TestFormDefaultsTypesConstraintsAndReview(t *testing.T) {
	var s schema.ElicitationSchema
	if err := json.Unmarshal([]byte(`{"type":"object","required":["name","count","enabled","choices"],"properties":{"name":{"type":"string","minLength":2,"pattern":"^[a-z]+$","default":"ada"},"count":{"type":"integer","minimum":1,"maximum":3,"default":2},"enabled":{"type":"boolean","default":true},"choices":{"type":"array","items":{"type":"string","enum":["a","b"]},"minItems":1,"maxItems":2},"model":{"type":"string","oneOf":[{"const":"fast","title":"Fast"}]},"ratio":{"type":"number","minimum":0,"maximum":1},"email":{"type":"string","format":"email"}}}`), &s); err != nil {
		t.Fatal(err)
	}
	fields, err := Fields(s)
	if err != nil {
		t.Fatal(err)
	}
	values := map[string]string{}
	for _, f := range fields {
		values[f.Name] = f.Default
	}
	values["choices"] = `["a","b"]`
	values["ratio"] = "0.5"
	values["model"] = "fast"
	values["email"] = "ada@example.com"
	result, err := Values(fields, values)
	if err != nil {
		t.Fatal(err)
	}
	if result["count"] != int64(2) || result["enabled"] != true || result["ratio"] != 0.5 || result["name"] != "ada" {
		t.Fatalf("incorrect types/defaults: %#v", result)
	}
	for key, bad := range map[string]string{"name": "A", "count": "4", "enabled": "maybe", "choices": "[\"a\",\"a\"]", "ratio": "NaN", "email": "invalid", "model": "unknown"} {
		old := values[key]
		values[key] = bad
		if _, err := Values(fields, values); err == nil {
			t.Errorf("accepted %s=%s", key, bad)
		}
		values[key] = old
	}
}
func TestSensitiveAndUnknownFieldsDeclined(t *testing.T) {
	for _, raw := range []string{`{"properties":{"api_key":{"type":"string"}}}`, `{"properties":{"nested":{"type":"object"}}}`} {
		var s schema.ElicitationSchema
		if err := json.Unmarshal([]byte(raw), &s); err != nil {
			t.Fatal(err)
		}
		if _, err := Fields(s); err == nil {
			t.Fatalf("accepted %s", raw)
		}
	}
}
