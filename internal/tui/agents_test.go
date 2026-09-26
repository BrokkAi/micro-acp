package tui

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/BrokkAi/micro-acp/internal/config"
	"github.com/BrokkAi/micro-acp/internal/registry"
	"github.com/charmbracelet/x/ansi"
)

type agentMetadataTransport func(*http.Request) (*http.Response, error)

func (f agentMetadataTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func agentCatalogModel(t *testing.T, selected string) *model {
	t.Helper()
	m := newModel(context.Background(), Options{Agent: selected, Paths: config.Paths{Cache: t.TempDir()}, Config: config.Config{
		RegistryURL: "https://example.test/registry.json",
		Agents: map[string]config.Command{
			"muse-acp": {Command: "/custom/muse"},
			"draupnir": {Command: "/custom/draupnir"},
		},
	}})
	m.registry.HTTP = &http.Client{Transport: agentMetadataTransport(func(r *http.Request) (*http.Response, error) {
		var body string
		switch r.URL.Host {
		case "example.test":
			body = `{"version":"1","agents":[{"id":"registered","name":"Registered","version":"7.8.9"}]}`
		case "registry.npmjs.org":
			if r.URL.Path != "/@brokkai/anvil/latest" {
				t.Errorf("looked up a custom override: %s", r.URL)
			}
			body = `{"name":"@brokkai/anvil","version":"1.2.3"}`
		default:
			t.Errorf("unexpected metadata request: %s", r.URL)
		}
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
	})}
	return m
}

func TestAgentPickerResolvesLatestBeforeSelection(t *testing.T) {
	m := agentCatalogModel(t, "")
	m.Init()
	m.picker.input.SetValue("anvil")
	m.picker.filter()
	m.pickerKey(keyPress(tea.KeyEnter, 0))
	if m.picker == nil || m.busy || m.picker.matches[0].version != "resolving…" {
		t.Fatal("selected an agent before its version was known")
	}
	m.Update(m.fetchCatalog()())
	if m.picker.input.Value() != "anvil" || len(m.picker.matches) != 1 {
		t.Fatal("version resolution lost the picker search")
	}
	for _, width := range []int{80, 35} {
		m.Update(tea.WindowSizeMsg{Width: width, Height: 24})
		if view := ansi.Strip(m.View().Content); !strings.Contains(view, "Anvil (1.2.3)") || strings.Contains(view, "latest") || strings.Contains(view, "resolving…") {
			t.Fatalf("picker did not show the exact version at width %d:\n%s", width, view)
		}
	}
	a := m.picker.matches[0].value.(registry.Agent)
	if a.Distribution.NPX.Package != "@brokkai/anvil@1.2.3" {
		t.Fatalf("picker value was not pinned: %+v", a)
	}
	if cmd := m.pickerKey(keyPress(tea.KeyEnter, 0)); cmd == nil || m.picker != nil || !m.busy || !strings.Contains(m.status, "1.2.3") {
		t.Fatal("resolved agent could not be selected")
	}
}

func TestBuiltinStartupWaitsForCatalog(t *testing.T) {
	m := agentCatalogModel(t, "anvil")
	m.Init()
	if m.started || m.busy {
		t.Fatal("built-in launched before its version was resolved")
	}
	m.Update(m.fetchCatalog()())
	if !m.started || !m.busy || !strings.Contains(m.status, "1.2.3") {
		t.Fatal("built-in did not start after version resolution")
	}
}

func TestUnresolvedAgentRemainsVisibleWithoutLaunching(t *testing.T) {
	m := agentCatalogModel(t, "")
	m.options.Offline = true
	m.Init()
	m.Update(m.fetchCatalog()())
	m.picker.input.SetValue("anvil")
	m.picker.filter()
	if len(m.picker.matches) != 1 || m.picker.matches[0].version != "unavailable" {
		t.Fatal("missing metadata was presented as a usable version")
	}
	if cmd := m.pickerKey(keyPress(tea.KeyEnter, 0)); cmd != nil || m.picker == nil || m.busy || !strings.Contains(m.lastError, "/refresh") {
		t.Fatal("unresolved agent launched or failed without a retry instruction")
	}
}
