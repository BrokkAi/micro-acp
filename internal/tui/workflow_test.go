package tui

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	acp "github.com/BrokkAi/acp-go"
	"github.com/BrokkAi/micro-acp/internal/client"
	"github.com/BrokkAi/micro-acp/internal/config"
	"github.com/BrokkAi/micro-acp/internal/demo"
	"github.com/BrokkAi/micro-acp/internal/store"
)

func TestTUIAgentProcess(t *testing.T) {
	if path := os.Getenv("MICRO_ACP_TUI_TEST_AGENT"); path != "" {
		if err := demo.Run(context.Background(), path); err != nil {
			os.Exit(1)
		}
		os.Exit(0)
	}
}

func connectedComposer(t *testing.T) *model {
	t.Helper()
	m := composer(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	t.Cleanup(cancel)
	c, err := client.Open(ctx, "demo", m.options.Cwd, config.Command{
		Command: os.Args[0], Args: []string{"-test.run=^TestTUIAgentProcess$"},
		Env: map[string]string{"MICRO_ACP_TUI_TEST_AGENT": t.TempDir()},
	}, store.Store{Directory: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := c.Shutdown(); err != nil {
			t.Error(err)
		}
	})
	if err := c.New(); err != nil {
		t.Fatal(err)
	}
	m.client = c
	return m
}

func TestQueuedPromptEditRetainsContextWithoutDuplicatingFiles(t *testing.T) {
	m := connectedComposer(t)
	if err := os.WriteFile(filepath.Join(m.options.Cwd, "main.go"), []byte("package main"), 0600); err != nil {
		t.Fatal(err)
	}
	m.resources = []acp.Content{acp.NewResourceLinkContent("https://example.com/spec", "spec")}
	m.busy, m.prompting = true, true
	m.sendPrompt("Read @main.go")
	if len(m.queued) != 1 || len(m.queued[0].blocks) != 3 {
		t.Fatal("context was not queued")
	}
	m.command("/queue")
	m.Update(keyPress(tea.KeyEnter, 0))
	if m.input.Value() != "Read @main.go" || len(m.resources) != 1 {
		t.Fatal("edit damaged context")
	}
	m.sendPrompt(m.input.Value())
	if len(m.queued) != 1 || len(m.queued[0].blocks) != 3 {
		t.Fatal("editing duplicated the file attachment")
	}
	m.command("/queue")
	m.input.SetValue("unfinished draft")
	m.Update(keyPress(tea.KeyEnter, 0))
	if m.input.Value() != "unfinished draft" || len(m.queued) != 1 {
		t.Fatal("editing discarded the current draft")
	}
}

func TestQueuedPromptsCannotCrossSessionBoundaries(t *testing.T) {
	m := connectedComposer(t)
	m.busy, m.prompting = true, true
	m.sendPrompt("Follow up in this session")
	m.busy, m.prompting = false, false
	if err := m.client.New(); err != nil {
		t.Fatal(err)
	}
	if cmd := m.sendQueued(); cmd != nil || len(m.queued) != 1 || !m.queuePaused {
		t.Fatal("queued prompt was sent to a different session")
	}
}

func TestInvalidReferenceKeepsDraft(t *testing.T) {
	m := connectedComposer(t)
	m.input.SetValue("Read @missing.go")
	m.sendPrompt(m.input.Value())
	if m.input.Value() != "Read @missing.go" || m.lastError == "" || m.prompting {
		t.Fatal("invalid attachment discarded or sent the draft")
	}
}

func TestQueuePickerTracksAutomaticDequeue(t *testing.T) {
	for _, count := range []int{1, 2} {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			m := connectedComposer(t)
			m.busy, m.prompting = true, true
			m.sendPrompt("first queued prompt")
			if count == 2 {
				m.sendPrompt("second queued prompt")
			}
			m.command("/queue")
			m.picker.index = count - 1
			// Finishing the active turn starts the first queued prompt while
			// the user is still looking at the queue picker.
			m.Update(resultMsg{status: "Ready", prompt: true})
			defer func() {
				if p := recover(); p != nil {
					t.Errorf("selecting a queue entry after automatic dequeue panicked: %v", p)
				}
			}()
			m.Update(keyPress(tea.KeyEnter, 0))
			if count == 1 && m.input.Value() != "" {
				t.Fatal("already submitted prompt was restored for editing")
			}
			if count == 2 && m.input.Value() != "second queued prompt" {
				t.Fatalf("edited the wrong prompt: %q", m.input.Value())
			}
		})
	}
}
