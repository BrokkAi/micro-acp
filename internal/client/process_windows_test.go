//go:build windows

package client

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const (
	helperRoleEnv      = "MICRO_ACP_TEST_PROCESS_ROLE"
	helperHeartbeatEnv = "MICRO_ACP_TEST_PROCESS_HEARTBEAT"
)

// TestProcessTreeHelper is re-executed by TestKillProcessTerminatesProcessTree.
// The parent role spawns a child; the child appends to a heartbeat file until
// the job object terminates it.
func TestProcessTreeHelper(t *testing.T) {
	switch os.Getenv(helperRoleEnv) {
	case "":
		t.Skip("helper process")
	case "parent":
		// Give the job assignment in the outer test time to happen before the
		// child is created, so the child inherits the job.
		time.Sleep(500 * time.Millisecond)
		child := exec.Command(os.Args[0], "-test.run=^TestProcessTreeHelper$")
		child.Env = envWith(os.Environ(), helperRoleEnv, "child")
		if err := child.Start(); err != nil {
			t.Fatal(err)
		}
		for {
			time.Sleep(time.Hour)
		}
	case "child":
		path := os.Getenv(helperHeartbeatEnv)
		for {
			f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := f.WriteString("x"); err != nil {
				t.Fatal(err)
			}
			_ = f.Close()
			time.Sleep(50 * time.Millisecond)
		}
	}
}

func TestKillProcessTerminatesProcessTree(t *testing.T) {
	heartbeat := filepath.Join(t.TempDir(), "heartbeat")
	cmd := exec.CommandContext(context.Background(), os.Args[0], "-test.run=^TestProcessTreeHelper$")
	cmd.Env = envWith(os.Environ(), helperRoleEnv, "parent")
	cmd.Env = envWith(cmd.Env, helperHeartbeatEnv, heartbeat)
	configureProcess(cmd)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	postStart(cmd)
	defer killProcess(cmd)
	jobsMu.Lock()
	_, assigned := jobs[cmd]
	jobsMu.Unlock()
	if !assigned {
		t.Skip("job objects are unavailable in this environment")
	}
	done := make(chan struct{})
	go func() {
		_ = cmd.Wait()
		close(done)
	}()

	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		if info, err := os.Stat(heartbeat); err == nil && info.Size() > 0 {
			break
		}
		time.Sleep(25 * time.Millisecond)
	}
	if info, err := os.Stat(heartbeat); err != nil || info.Size() == 0 {
		t.Fatal("child process never wrote a heartbeat")
	}

	killProcess(cmd)
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("parent process did not exit after killProcess")
	}
	time.Sleep(1500 * time.Millisecond)
	first, err := os.Stat(heartbeat)
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(time.Second)
	second, err := os.Stat(heartbeat)
	if err != nil {
		t.Fatal(err)
	}
	if second.Size() != first.Size() {
		t.Fatalf("child survived tree termination: heartbeat grew from %d to %d bytes", first.Size(), second.Size())
	}
}

func envWith(env []string, key, value string) []string {
	prefix := strings.ToUpper(key) + "="
	out := make([]string, 0, len(env)+1)
	for _, entry := range env {
		if strings.HasPrefix(strings.ToUpper(entry), prefix) {
			continue
		}
		out = append(out, entry)
	}
	return append(out, key+"="+value)
}
