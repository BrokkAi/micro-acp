package tui

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/BrokkAi/acp-go/schema"
	"github.com/BrokkAi/micro-acp/internal/client"
)

func formModel(t *testing.T) (*model, client.Elicitation) {
	t.Helper()
	var s schema.ElicitationSchema
	if err := json.Unmarshal([]byte(`{"type":"object","properties":{"name":{"type":"string","minLength":2},"optional":{"type":"string","enum":["a","b"]}},"required":["name"]}`), &s); err != nil {
		t.Fatal(err)
	}
	e := client.Elicitation{Agent: "fixture", Schema: &s, Reply: make(chan schema.CreateElicitationResponse, 1), Done: make(chan struct{})}
	m := newModel(context.Background(), Options{})
	m.page = "chat"
	m.input.SetValue("unsent draft")
	m.Update(elicitationMsg{e})
	return m, e
}

func keyPress(code rune, mod tea.KeyMod) tea.KeyPressMsg {
	return tea.KeyPressMsg{Code: code, Mod: mod}
}

func TestFormPasteReviewEditAndSubmit(t *testing.T) {
	m, e := formModel(t)
	// Validation keeps the field active. Paste goes to the form, not the composer.
	m.Update(keyPress(tea.KeyEnter, 0))
	if m.elicitation.index != 0 || m.elicitation.err == "" {
		t.Fatal("invalid field was accepted")
	}
	m.Update(tea.PasteMsg{Content: "Ada"})
	m.Update(keyPress(tea.KeyEnter, 0))
	m.Update(keyPress('x', tea.ModCtrl)) // Omit an optional enumeration.
	if m.elicitation.index != 2 || m.elicitation.action != 2 {
		t.Fatal("form skipped review or defaults to submit")
	}
	m.Update(keyPress(tea.KeyTab, tea.ModShift))
	m.Update(keyPress(tea.KeyTab, tea.ModShift))
	m.elicitation.input.SetValue("Grace")
	m.Update(keyPress(tea.KeyEnter, 0))
	m.Update(keyPress('x', tea.ModCtrl))
	m.Update(keyPress(tea.KeyUp, 0))
	m.Update(keyPress(tea.KeyUp, 0))
	m.Update(keyPress(tea.KeyEnter, 0))
	r := <-e.Reply
	if r.Accept == nil || r.Accept.Content["name"] != "Grace" || len(r.Accept.Content) != 1 {
		t.Fatalf("bad form response: %+v", r)
	}
	if m.input.Value() != "unsent draft" {
		t.Fatal("form input changed the conversation draft")
	}
}

func TestFormDeclineCancelAndCanceledQueue(t *testing.T) {
	for _, key := range []tea.KeyPressMsg{keyPress(tea.KeyEscape, 0), keyPress('d', tea.ModCtrl)} {
		m, e := formModel(t)
		m.Update(key)
		result := <-e.Reply
		if result.Accept != nil || (result.Cancel == nil && result.Decline == nil) {
			t.Fatal("dismissal accepted request")
		}
	}
	m, e := formModel(t)
	done := make(chan struct{})
	close(done)
	canceled := e
	canceled.Done = done
	m.elicitationQueue = []client.Elicitation{canceled, e}
	m.Update(keyPress(tea.KeyEscape, 0))
	<-e.Reply
	if m.elicitation == nil || len(m.elicitationQueue) != 0 {
		t.Fatal("canceled input request blocked the queue")
	}
}

func TestURLRequiresExplicitConsentAndDisplaysAddress(t *testing.T) {
	m := newModel(context.Background(), Options{})
	e := client.Elicitation{Agent: "fixture", URL: "https://example.com/login?flow=42", Reply: make(chan schema.CreateElicitationResponse, 1), Done: make(chan struct{})}
	m.Update(elicitationMsg{e})
	view := m.elicitationView()
	if !strings.Contains(view, e.URL) || !strings.Contains(view, "fixture") {
		t.Fatal("URL or requesting agent hidden")
	}
	_, cmd := m.Update(keyPress(tea.KeyEnter, 0))
	if r := <-e.Reply; r.Cancel == nil || cmd != nil {
		t.Fatal("URL opened without explicit selection")
	}
}
