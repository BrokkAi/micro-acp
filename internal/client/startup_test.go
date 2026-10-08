package client

import (
	"testing"
	"time"
)

// Starting an agent can mean a first-time package install (npx --yes, uvx,
// or a binary download) on a cold cache, which routinely takes minutes. The
// startup deadline must survive that, so lock a floor well above a warm
// spawn.
func TestAgentStartupTimeoutFitsInstalls(t *testing.T) {
	if agentStartupTimeout < 5*time.Minute {
		t.Fatalf("agent startup timeout %v is too short for first-time installs", agentStartupTimeout)
	}
}
