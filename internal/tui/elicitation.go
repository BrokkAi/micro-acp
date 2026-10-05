package tui

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os/exec"
	"runtime"
	"strings"

	"charm.land/bubbles/v2/textinput"
	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	acp "github.com/BrokkAi/acp-go"
	"github.com/BrokkAi/acp-go/schema"
	"github.com/BrokkAi/micro-acp/internal/client"
	"github.com/BrokkAi/micro-acp/internal/forms"
	"github.com/charmbracelet/x/ansi"
)

type elicitationMsg struct{ event client.Elicitation }
type browserMsg struct{ err error }
type elicitationUI struct {
	event         client.Elicitation
	fields        []forms.Field
	values        map[string]string
	index, choice int
	input         textinput.Model
	err           string
	action        int
}

func (m *model) waitElicitation() tea.Cmd {
	requests, ctx := m.interactions.Elicitations, m.ctx
	return func() tea.Msg {
		select {
		case e := <-requests:
			return elicitationMsg{e}
		case <-ctx.Done():
			return nil
		}
	}
}
func (m *model) nextElicitation() tea.Cmd {
	for m.elicitation == nil && len(m.elicitationQueue) > 0 {
		e := m.elicitationQueue[0]
		m.elicitationQueue = m.elicitationQueue[1:]
		select {
		case <-e.Done:
			continue
		default:
		}
		ui := &elicitationUI{event: e, values: map[string]string{}, input: textinput.New(), action: 2}
		ui.input.SetWidth(max(10, m.width-8))
		ui.input.CharLimit = 8192
		if e.Schema != nil {
			var err error
			ui.fields, err = forms.Fields(*e.Schema)
			if err != nil {
				e.Reply <- acp.DeclineElicitation()
				m.lastError = err.Error()
				continue
			}
			for _, f := range ui.fields {
				ui.values[f.Name] = f.Default
			}
		}
		m.elicitation = ui
		m.interactionOffset = 0
		ui.loadField()
		return ui.input.Focus()
	}
	return nil
}
func (e *elicitationUI) loadField() {
	e.choice = 0
	e.err = ""
	if e.index < len(e.fields) {
		f := e.fields[e.index]
		e.input.SetValue(e.values[f.Name])
		for i, o := range f.Options {
			if o.Value == e.values[f.Name] {
				e.choice = i
			}
		}
	}
}
func (e *elicitationUI) saveField() bool {
	if e.index >= len(e.fields) {
		return true
	}
	f := e.fields[e.index]
	value := e.input.Value()
	if len(f.Options) > 0 {
		if f.Kind == "array" {
			value = e.values[f.Name]
			if value == "" {
				value = "[]"
			}
		} else {
			value = f.Options[e.choice].Value
		}
	}
	if _, err := f.Parse(value); err != nil {
		e.err = err.Error()
		return false
	}
	e.values[f.Name] = value
	return true
}
func (m *model) finishElicitation(response schema.CreateElicitationResponse) tea.Cmd {
	m.elicitation.event.Reply <- response
	m.elicitation = nil
	return m.nextElicitation()
}
func (m *model) elicitationKey(msg tea.KeyPressMsg) tea.Cmd {
	e := m.elicitation
	k := msg.String()
	if k == "esc" {
		return m.finishElicitation(acp.CancelElicitation())
	}
	if k == "ctrl+d" {
		return m.finishElicitation(acp.DeclineElicitation())
	}
	if k == "shift+tab" && e.index > 0 {
		m.interactionOffset = 0
		e.index--
		e.loadField()
		return nil
	}
	if e.index < len(e.fields) {
		f := e.fields[e.index]
		if k == "ctrl+x" && !f.Required {
			e.values[f.Name] = ""
			e.index++
			m.interactionOffset = 0
			e.loadField()
			return nil
		}
		if k == "enter" || k == "tab" {
			if e.saveField() {
				m.interactionOffset = 0
				e.index++
				e.loadField()
			}
			return nil
		}
		if len(f.Options) > 0 {
			switch k {
			case "up", "left":
				e.choice = max(0, e.choice-1)
			case "down", "right":
				e.choice = min(len(f.Options)-1, e.choice+1)
			case "space":
				if f.Kind == "array" {
					var values []string
					_ = json.Unmarshal([]byte(e.values[f.Name]), &values)
					selected := f.Options[e.choice].Value
					found := false
					var next []string
					for _, v := range values {
						if v == selected {
							found = true
						} else {
							next = append(next, v)
						}
					}
					if !found {
						next = append(next, selected)
					}
					if next == nil {
						next = []string{}
					}
					raw, _ := json.Marshal(next)
					e.values[f.Name] = string(raw)
				}
			}
			return nil
		}
		var cmd tea.Cmd
		e.input, cmd = e.input.Update(msg)
		return cmd
	}
	switch k {
	case "up", "left":
		e.action = max(0, e.action-1)
	case "down", "right":
		e.action = min(2, e.action+1)
	case "enter":
		if e.action == 1 {
			return m.finishElicitation(acp.DeclineElicitation())
		}
		if e.action == 2 {
			return m.finishElicitation(acp.CancelElicitation())
		}
		if e.event.URL != "" {
			address := e.event.URL
			next := m.finishElicitation(acp.AcceptElicitation(nil))
			return tea.Batch(next, func() tea.Msg { return browserMsg{openBrowser(address)} })
		}
		values, err := forms.Values(e.fields, e.values)
		if err != nil {
			e.err = err.Error()
			return nil
		}
		return m.finishElicitation(acp.AcceptElicitation(values))
	}
	return nil
}
func openBrowser(address string) error {
	u, err := url.Parse(address)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		return fmt.Errorf("invalid browser URL")
	}
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", address)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", address)
	default:
		cmd = exec.Command("xdg-open", address)
	}
	return cmd.Run()
}
func (m *model) elicitationView() string { return m.elicitationPanel(max(20, m.height-4)) }

// Keep the active field and action choices visible; only explanatory context scrolls.
func (m *model) elicitationPanel(height int) string {
	e := m.elicitation
	w := max(10, m.width-4)
	context := clean(e.event.Request.Message)
	if e.event.URL != "" {
		u, _ := url.Parse(e.event.URL)
		context += "\nOpen in your browser: " + clean(u.Host) + "\n" + clean(e.event.URL)
	}
	var controls []string
	if e.index < len(e.fields) {
		f := e.fields[e.index]
		required := "optional"
		if f.Required {
			required = "required"
		}
		controls = append(controls, line(fmt.Sprintf("%d/%d · %s (%s)", e.index+1, len(e.fields), f.Title, required), w))
		if f.Description != "" {
			context += "\n" + clean(f.Description)
		}
		if len(f.Options) > 0 {
			var choices []item
			var selected []string
			if f.Kind == "array" {
				_ = json.Unmarshal([]byte(e.values[f.Name]), &selected)
			}
			for _, o := range f.Options {
				label := o.Label
				if f.Kind == "array" {
					mark := "[ ] "
					for _, v := range selected {
						if v == o.Value {
							mark = "[x] "
						}
					}
					label = mark + label
				}
				choices = append(choices, item{title: label})
			}
			controls = append(controls, m.suggestionRows(choices, e.choice, w, min(5, max(1, height-7)), false))
		} else {
			controls = append(controls, e.input.View())
		}
		hint := "Enter next · Esc cancel · PgUp/PgDn details · Shift+Tab back · Ctrl+X skip · Ctrl+D decline"
		if f.Kind == "array" {
			hint = "Space toggle · " + hint
		}
		controls = append(controls, m.theme.muted.Render(line(hint, w)))
	} else {
		if len(e.fields) > 0 {
			context += "\nReview your responses (Shift+Tab to edit):"
			for _, f := range e.fields {
				context += "\n" + clean(f.Title) + ": " + clean(e.values[f.Name])
			}
		}
		label := "Submit"
		if e.event.URL != "" {
			label = "Submit / open URL"
		}
		for i, text := range []string{label, "Decline", "Cancel"} {
			prefix := "  "
			if e.action == i {
				prefix = "› "
			}
			controls = append(controls, line(prefix+text, w))
		}
		controls = append(controls, m.theme.muted.Render(line("Enter confirm · Esc cancel · PgUp/PgDn details", w)))
	}
	if e.err != "" {
		controls = append(controls, m.theme.danger.Render(line(e.err, w)))
	}
	body := strings.Join(controls, "\n")
	context = ansi.Wrap(context, w, "")
	v := viewport.New(viewport.WithWidth(w), viewport.WithHeight(min(max(1, lipgloss.Height(context)), max(1, height-lipgloss.Height(body)-1))))
	v.SetContent(context)
	v.SetYOffset(m.interactionOffset)
	header := "INPUT REQUEST · " + e.event.Agent
	if e.event.Subagent != "" {
		header += " · " + subagentMark + " " + e.event.Subagent
	}
	return m.theme.accent.Bold(true).Render(line(header, w)) + "\n" + v.View() + "\n" + body
}
