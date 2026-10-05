//go:build linux || darwin || windows

package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/BrokkAi/micro-acp/internal/store"
	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/vt"
)

// lockedEmulator serializes writes with renders so assertions can read the
// visible screen and its scrollback while the PTY reader keeps writing.
type lockedEmulator struct {
	*vt.Emulator
	mu sync.RWMutex
}

func (e *lockedEmulator) Write(p []byte) (int, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.Emulator.Write(p)
}

func (e *lockedEmulator) Resize(w, h int) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.Emulator.Resize(w, h)
}

// screen is the visible viewport, where menus and the prompt live.
func (e *lockedEmulator) screen() string {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return ansi.Strip(e.Emulator.Render())
}

// history adds scrollback to the visible viewport. The TUI prints transcripts
// above the prompt, so printed output can scroll out of a short terminal.
func (e *lockedEmulator) history() string {
	e.mu.RLock()
	defer e.mu.RUnlock()
	var b strings.Builder
	if sb := e.Emulator.Scrollback(); sb != nil {
		for _, line := range sb.Lines() {
			b.WriteString(line.String())
			b.WriteByte('\n')
		}
	}
	b.WriteString(e.Emulator.Render())
	return ansi.Strip(b.String())
}

func TestTerminalHistoryRetainsScrolledOutput(t *testing.T) {
	emulator := &lockedEmulator{Emulator: vt.NewEmulator(20, 5)}
	for i := 0; i < 20; i++ {
		if _, err := emulator.Write([]byte(fmt.Sprintf("line %d\r\n", i))); err != nil {
			t.Fatal(err)
		}
	}
	if strings.Contains(emulator.screen(), "line 0") {
		t.Fatal("first line should have scrolled off the viewport")
	}
	if !strings.Contains(emulator.history(), "line 0") {
		t.Fatal("history dropped output that scrolled off the screen")
	}
}

// Exercise the real binary and renderer, not only Model.View: terminal cursor
// movement bugs can leave stale menus even when the next View is correct.
// Unix runs it in a pty; Windows runs it in a ConPTY pseudo console.
func TestTerminalWorkflow(t *testing.T) {
	if testing.Short() {
		t.Skip("subprocess terminal integration")
	}
	root := t.TempDir()
	binary := filepath.Join(root, "micro-acp")
	if runtime.GOOS == "windows" {
		binary += ".exe"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	build := exec.CommandContext(ctx, "go", "build", "-race", "-o", binary, ".")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	workspace := filepath.Join(root, "workspace")
	if err := os.Mkdir(workspace, 0700); err != nil {
		t.Fatal(err)
	}
	for path, content := range map[string]string{"main.go": "package main\n", ".gitignore": "secret.txt\n", "secret.txt": "hidden"} {
		if err := os.WriteFile(filepath.Join(workspace, path), []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if out, err := exec.Command("git", "init", "-q", workspace).CombinedOutput(); err != nil {
		t.Fatalf("git: %v %s", err, out)
	}
	t.Setenv("TERM", "xterm-256color")
	t.Setenv("MICRO_ACP_HOME", root)
	terminal, err := startPTY(ctx, []string{binary, "--demo", "--cwd", workspace}, 100, 30)
	if err != nil {
		t.Fatal(err)
	}
	emulator := &lockedEmulator{Emulator: vt.NewEmulator(100, 30)}
	readDone, replyDone := make(chan struct{}), make(chan struct{})
	stopReplies := make(chan struct{})
	go func() { defer close(readDone); _, _ = io.Copy(emulator, terminal) }()
	go func() {
		defer close(replyDone)
		buffer := make([]byte, 4096)
		for {
			n, err := emulator.Read(buffer)
			if err != nil {
				return
			}
			select {
			case <-stopReplies:
				return
			default:
			}
			_, _ = terminal.Write(buffer[:n])
		}
	}()
	processDone := make(chan error, 1)
	go func() { processDone <- terminal.Wait() }()
	t.Cleanup(func() {
		terminal.Kill()
		_ = terminal.Close()
		<-readDone
		// x/vt's Close must not race its blocking Read. A status query wakes
		// the reply reader so it exits before we close the emulator.
		close(stopReplies)
		_, _ = emulator.Write([]byte("\x1b[5n"))
		<-replyDone
		_ = emulator.Close()
	})
	screen := emulator.screen
	history := emulator.history
	wait := func(label string, condition func(string) bool) {
		t.Helper()
		deadline := time.Now().Add(12 * time.Second)
		for time.Now().Before(deadline) {
			if condition(screen()) {
				return
			}
			time.Sleep(20 * time.Millisecond)
		}
		t.Fatalf("%s\n%s", label, screen())
	}
	contains := func(text string) {
		t.Helper()
		wait("waiting for "+text, func(s string) bool { return strings.Contains(s, text) })
	}
	send := func(keys string) {
		t.Helper()
		if _, err := io.WriteString(terminal, keys); err != nil {
			t.Fatal(err)
		}
	}
	st := store.Store{Directory: filepath.Join(root, "data")}
	sessions := func() []store.Session {
		list, err := st.List()
		if err != nil {
			t.Fatal(err)
		}
		return list
	}
	waitSessions := func(n int) { t.Helper(); wait("session count", func(string) bool { return len(sessions()) == n }) }

	contains("Walkthrough")
	contains("● Stream words")
	contains("/config")
	// Session settings arrive before the UI handles the operation's result.
	// Commands remain disabled until the busy indicator disappears.
	wait("session ready for commands", func(s string) bool { return !strings.Contains(s, "esc to interrupt") })
	send("/config\r")
	contains("Session configuration")
	contains("On · stream")
	send("\x1b")
	wait("close session configuration", func(s string) bool { return !strings.Contains(s, "Type to search") })
	send("/config str")
	contains("Session configuration")
	send("\t")
	contains("❯ /config stream")
	send("off\r")
	contains("○ Stream words")
	send("/settings stream true\r")
	contains("● Stream words")
	send("/mo")
	contains("/model")
	contains("/mode")
	send("\x1b")
	wait("dismiss preserves draft", func(s string) bool { return strings.Contains(s, "❯ /mo") && !strings.Contains(s, "Commands") })
	send("\x15/mode\r")
	contains("Type to search")
	send("brief\r")
	wait("one Enter selects; old menu erased", func(s string) bool {
		return strings.Contains(s, "Brief") && !strings.Contains(s, "Type to search")
	})
	if strings.Count(screen(), "demo · New session") != 1 {
		t.Fatal("redraw duplicated committed output")
	}
	send("Review @ma")
	contains("main.go")
	if strings.Contains(screen(), "secret.txt") {
		t.Fatal("ignored file suggested")
	}
	send("\r")
	contains("Review @main.go")
	if len(sessions()[0].Messages) != 0 {
		t.Fatal("reference selection submitted the prompt")
	}
	send("please\r")
	contains("This is the local demo agent")
	wait("saved response", func(string) bool { list := sessions(); return len(list) == 1 && len(list[0].Messages) >= 2 })
	original := sessions()[0]
	if len(original.Messages[0].Content) != 2 {
		t.Fatal("file attachment missing")
	}

	send("permission\r")
	contains("Allow once")
	contains("› Cancel")
	send("\r")
	contains("cancelled")
	send("form\r")
	contains("Input request")
	send("\r")
	contains("2/2")
	send("\r")
	contains("› Cancel")
	send("\x1b[A\x1b[A\r")
	contains("hello, Ada!")
	send("slow\r")
	contains("Esc to stop")
	send("queued followup\t")
	contains("1 queued")
	contains("Brief")
	contains("● Stream words")
	send("unsent draft\x1b[1;3A")
	contains("Editing queued prompt")
	contains("❯ queued followup")
	send("\x1b")
	contains("❯ unsent draft")
	send("\x15")
	send("\x1b")
	contains("Stopped")
	contains("Brief")
	contains("● Stream words")
	contains("/queue send to continue")
	send("/queue\r")
	contains("Queued prompts")
	send("\r")
	contains("❯ queued followup")
	send("\x1b")
	wait("queue edit cancelled", func(s string) bool { return !strings.Contains(s, "Editing queued prompt") })
	send("\x15/new\r")
	waitSessions(2)
	send("/sessions\r")
	contains("Resume a session")
	send("Review\r")
	// The resumed transcript is taller than the viewport, so its earlier
	// messages can scroll into history before the next poll sees them.
	wait("session loaded", func(string) bool {
		return strings.Contains(history(), "This is the local demo agent") &&
			!strings.Contains(screen(), "Resume a session")
	})
	send("/fork --context\r")
	waitSessions(3)
	found := false
	for _, s := range sessions() {
		if s.ParentID == original.ID {
			found = true
		}
	}
	if !found {
		t.Fatal("fork parent missing")
	}
	send("/delete\r")
	contains("y delete")
	send("y")
	waitSessions(2)
	send("/new\r")
	waitSessions(3)

	if err := terminal.Resize(35, 14); err != nil {
		t.Fatal(err)
	}
	emulator.Resize(35, 14)
	send("/mode\r")
	contains("Demo response")
	contains("enter choose")
	send("\x1b")
	wait("close mode selector", func(s string) bool { return !strings.Contains(s, "Type to search") })
	send("/quit\r")
	select {
	case err := <-processDone:
		if err != nil {
			t.Fatalf("exit: %v\n%s", err, screen())
		}
	case <-time.After(10 * time.Second):
		t.Fatalf("quit hung\n%s", screen())
	}
	if emulator.IsAltScreen() {
		t.Fatal("application used the alternate screen")
	}
}
