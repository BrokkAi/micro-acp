package tui

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	acp "github.com/BrokkAi/acp-go"
	"github.com/BrokkAi/acp-go/schema"
	"github.com/BrokkAi/micro-acp/internal/client"
	"github.com/charmbracelet/x/ansi"
)

func queueComposer(t *testing.T, steering bool) *model {
	t.Helper()
	m := composer(t)
	m.client = &client.Client{Agent: "test", Init: acp.Initialization{Meta: schema.Meta{"steering": map[string]any{"supported": steering}}}}
	m.busy, m.prompting = true, true
	return m
}

func TestEnterSteersAndTabQueuesWithoutCancelling(t *testing.T) {
	m := queueComposer(t, true)
	m.input.SetValue("Follow up later")
	m.key(keyPress(tea.KeyTab, 0))
	if len(m.queued) != 1 || m.queued[0].steer || m.steering != nil || m.input.Value() != "" {
		t.Fatal("Tab did not queue the draft")
	}
	m.input.SetValue("Change direction now")
	if cmd := m.key(keyPress(tea.KeyEnter, 0)); cmd == nil || m.steering == nil || m.steering.text != "Change direction now" || len(m.queued) != 1 || !m.prompting {
		t.Fatal("Enter did not steer independently of queued work")
	}
	pending := *m.steering
	m.input.SetValue("More guidance")
	if cmd := m.key(keyPress(tea.KeyEnter, 0)); cmd != nil || len(m.queued) != 2 {
		t.Fatal("concurrent steers were not serialized")
	}
	if cmd := m.steeringResult(steeredMsg{client: m.client, prompt: pending, outcome: client.SteeringInjected}); cmd == nil || m.steering.text != "More guidance" || len(m.queued) != 1 {
		t.Fatal("next steering message did not retain submission order")
	}
	plain := queueComposer(t, false)
	plain.input.SetValue("Follow-up")
	if cmd := plain.key(keyPress(tea.KeyEnter, 0)); cmd != nil || len(plain.queued) != 1 || plain.steering != nil {
		t.Fatal("agent without steering did not use the local queue")
	}
}

func TestQueueEditPreservesPositionAndExistingDraft(t *testing.T) {
	m := queueComposer(t, false)
	for _, text := range []string{"first", "second", "third"} {
		m.sendPrompt(text)
	}
	m.input.SetValue("unfinished draft")
	m.resources = []acp.Content{acp.NewResourceLinkContent("draft context", "https://example.com/draft")}
	m.editQueued(1)
	if m.input.Value() != "second" || m.editing == nil {
		t.Fatal("queued prompt did not open for editing")
	}
	// The first task finishing while a prompt is edited must not drain past it.
	m.Update(resultMsg{prompt: true, reason: schema.StopReasonEndTurn})
	if m.prompting || len(m.queued) != 2 {
		t.Fatal("queue drained during editing")
	}
	m.input.SetValue("edited second")
	cmd := m.sendPrompt(m.input.Value())
	if cmd == nil || !m.prompting || len(m.queued) != 2 || m.queued[0].text != "edited second" || m.queued[1].text != "third" {
		t.Fatalf("edit reordered queue: %+v", m.queued)
	}
	if m.input.Value() != "unfinished draft" || len(m.resources) != 1 || m.editing != nil {
		t.Fatal("editing lost the previous draft or its attachment")
	}
	m.key(keyPress(tea.KeyUp, tea.ModAlt))
	if m.input.Value() != "third" {
		t.Fatal("Alt+Up did not edit the latest queued prompt")
	}
	m.input.SetValue("discard this edit")
	m.key(keyPress(tea.KeyEscape, 0))
	if m.input.Value() != "unfinished draft" || len(m.queued) != 2 || m.queued[1].text != "third" {
		t.Fatal("Esc did not restore the original queue entry and draft")
	}
}

func TestQueuedPromptsCannotBeOvertakenByNewIdleSubmission(t *testing.T) {
	m := queueComposer(t, false)
	m.sendPrompt("first")
	m.busy, m.prompting = false, false
	m.sendPrompt("second")
	if !m.prompting || len(m.queued) != 1 || m.queued[0].text != "second" {
		t.Fatalf("new input bypassed FIFO: %+v", m.queued)
	}
}

func TestFreshPromptAfterStopDoesNotNeedQueueResume(t *testing.T) {
	m := queueComposer(t, false)
	m.Update(resultMsg{prompt: true, reason: schema.StopReasonCancelled})
	if cmd := m.sendPrompt("new direction"); cmd == nil || !m.prompting || m.queuePaused || len(m.queued) != 0 {
		t.Fatal("a fresh prompt was stranded in an empty paused queue")
	}
}

func TestSteeringFallbackAndAmbiguousFailureRetainPrompt(t *testing.T) {
	for _, failed := range []bool{false, true} {
		for _, turnFinished := range []bool{false, true} {
			m := queueComposer(t, true)
			m.sendPrompt("guidance")
			pending := *m.steering
			if turnFinished {
				m.Update(resultMsg{prompt: true, reason: schema.StopReasonEndTurn})
				if !m.busy || m.prompting {
					t.Fatal("unresolved steering must prevent queue dispatch")
				}
			}
			msg := steeredMsg{client: m.client, prompt: pending, outcome: client.SteeringPromptRequired}
			if failed {
				msg.err = errors.New("delivery unconfirmed")
			}
			cmd := m.steeringResult(msg)
			if failed {
				if !m.queuePaused || len(m.queued) != 1 || cmd != nil || m.lastError != "delivery unconfirmed" {
					t.Fatal("ambiguous steering failure was lost or resent automatically")
				}
			} else if turnFinished {
				if cmd == nil || !m.prompting || len(m.queued) != 0 {
					t.Fatal("explicit promptRequired did not deliver one normal follow-up")
				}
			} else if len(m.queued) != 1 || m.queued[0].steer || cmd != nil {
				t.Fatal("fallback overlapped the current turn")
			}
		}
	}
}

func TestQueueRemoveAndClearKeepDraftAndPendingSteering(t *testing.T) {
	m := queueComposer(t, true)
	m.submitPrompt("first", true)
	m.submitPrompt("second", true)
	m.sendPrompt("guidance")
	m.input.SetValue("draft")
	m.openPicker("queue", m.queueItems())
	m.pickerKey(keyPress('d', tea.ModCtrl))
	if len(m.queued) != 1 || m.queued[0].text != "second" || len(m.picker.matches) != 1 {
		t.Fatal("remove did not update queue picker")
	}
	m.command("/queue clear")
	if len(m.queued) != 0 || m.input.Value() != "draft" || m.steering == nil {
		t.Fatal("clearing queued work damaged the draft or in-flight steering")
	}
}

func TestQueuePreviewAndPendingTranscriptFit(t *testing.T) {
	m := queueComposer(t, false)
	for _, text := range []string{"first visible prompt", "second visible prompt", "third", "fourth"} {
		m.sendPrompt(text)
	}
	for _, size := range [][2]int{{35, 14}, {80, 24}} {
		m.Update(tea.WindowSizeMsg{Width: size[0], Height: size[1]})
		view := m.View().Content
		if lipgloss.Width(view) > size[0] || lipgloss.Height(view) > size[1] || !strings.Contains(ansi.Strip(view), "first visible prompt") {
			t.Fatalf("bad queue preview at %v:\n%s", size, view)
		}
	}
	before := append([]queuedPrompt(nil), m.queued...)
	m.queuePaused = true
	m.sendQueued()
	// Explicit send is allowed, but calling with an active turn cannot overlap it.
	if !reflect.DeepEqual(before, m.queued) {
		t.Fatal("queue dispatched while another turn was active")
	}
}
