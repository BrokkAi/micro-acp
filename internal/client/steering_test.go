package client

import (
	"context"
	"encoding/json"
	"net"
	"reflect"
	"strings"
	"testing"
	"time"

	acp "github.com/BrokkAi/acp-go"
	"github.com/BrokkAi/acp-go/schema"
	"github.com/BrokkAi/micro-acp/internal/store"
)

func steeringClient(t *testing.T, handler acp.Handler) (*Client, *acp.Connection) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	left, right := net.Pipe()
	c := &Client{ctx: ctx, cancel: cancel, store: store.Store{Directory: t.TempDir()}, current: store.NewSession("test", "remote", t.TempDir()), wire: acp.Session{SessionID: "remote"}}
	c.Init.Meta = schema.Meta{"steering": map[string]any{"supported": true}}
	c.conn = acp.Connect(left, left, nil, c.notification)
	agent := acp.Connect(right, right, handler, nil)
	t.Cleanup(func() { cancel(); _ = c.conn.Close(); _ = agent.Close() })
	return c, agent
}

func steeringStatusUpdate(t *testing.T, c *acp.Connection, status string) {
	t.Helper()
	err := c.Notify(context.Background(), "session/update", map[string]any{"sessionId": "remote", "update": map[string]any{"sessionUpdate": "session_info_update", "_meta": map[string]any{"codex": map[string]any{"threadStatus": map[string]any{"type": status}}}}})
	if err != nil {
		t.Error(err)
	}
}

func TestSteeringAcceptanceKeepsOrderingAndDoesNotCancel(t *testing.T) {
	started, received, acknowledge, finish := make(chan struct{}), make(chan struct{}), make(chan struct{}), make(chan struct{})
	var agent *acp.Connection
	blocks := []acp.Content{acp.NewTextContent("Change direction"), acp.NewResourceLinkContent("Spec", "https://example.com/spec")}
	c, remote := steeringClient(t, func(ctx context.Context, method string, raw json.RawMessage) (any, error) {
		switch method {
		case "session/prompt":
			steeringStatusUpdate(t, agent, "active")
			close(started)
			select {
			case <-finish:
			case <-ctx.Done():
				return nil, ctx.Err()
			}
			return schema.PromptResponse{StopReason: schema.StopReasonEndTurn}, nil
		case "_session/steering":
			var request schema.PromptRequest
			if err := json.Unmarshal(raw, &request); err != nil {
				return nil, err
			}
			got, _ := json.Marshal(request.Prompt)
			want, _ := json.Marshal(blocks)
			if string(got) != string(want) || request.SessionID != "remote" || request.Meta["steering"].(map[string]any)["idleBehavior"] != "promptRequired" {
				t.Errorf("incorrect steering request: %s", raw)
			}
			_ = agent.Notify(ctx, "session/update", acp.NewAgentMessageChunkUpdate("remote", schema.ContentChunk{Content: acp.NewTextContent("Following the new direction")}))
			_ = agent.Notify(ctx, "session/update", acp.NewUserMessageChunkUpdate("remote", schema.ContentChunk{Content: blocks[0]}))
			close(received)
			select {
			case <-acknowledge:
			case <-ctx.Done():
				return nil, ctx.Err()
			}
			return map[string]string{"outcome": "injected"}, nil
		default:
			t.Errorf("unexpected request (steering must not cancel): %s", method)
			return nil, &acp.RPCError{Code: -32601, Message: "unsupported"}
		}
	})
	agent = remote
	promptDone := make(chan error, 1)
	go func() { _, err := c.Prompt("Original task"); promptDone <- err }()
	<-started
	steerDone := make(chan error, 1)
	go func() {
		outcome, err := c.SteerContent(c.current.ID, blocks)
		if outcome != SteeringInjected && err == nil {
			t.Errorf("outcome = %s", outcome)
		}
		steerDone <- err
	}()
	<-received
	if err := c.Save(); err != nil {
		t.Fatal(err)
	}
	saved, err := c.store.Load(c.current.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, message := range saved.Messages {
		if message.Role == "user" && message.Text != "Original task" {
			t.Fatal("unacknowledged steering persisted as accepted")
		}
	}
	close(acknowledge)
	if err := <-steerDone; err != nil {
		t.Fatal(err)
	}
	close(finish)
	if err := <-promptDone; err != nil {
		t.Fatal(err)
	}
	snapshot, _ := c.Snapshot()
	if len(snapshot.Messages) != 3 || snapshot.Messages[1].Pending || snapshot.Messages[1].Role != "user" || !reflect.DeepEqual(snapshot.Messages[1].Content, blocks) || snapshot.Messages[2].Text != "Following the new direction" {
		t.Fatalf("steering was lost, duplicated, or reordered: %+v", snapshot.Messages)
	}
}

func TestSteeringNonDeliveryAndFailures(t *testing.T) {
	for _, outcome := range []string{"promptRequired", "failed", "futureOutcome", "unsupported"} {
		t.Run(outcome, func(t *testing.T) {
			started, finish := make(chan struct{}), make(chan struct{})
			var agent *acp.Connection
			c, remote := steeringClient(t, func(ctx context.Context, method string, raw json.RawMessage) (any, error) {
				if method == "session/prompt" {
					steeringStatusUpdate(t, agent, "active")
					close(started)
					select {
					case <-finish:
					case <-ctx.Done():
						return nil, ctx.Err()
					}
					return schema.PromptResponse{StopReason: schema.StopReasonEndTurn}, nil
				}
				if outcome == "unsupported" {
					return nil, &acp.RPCError{Code: -32601, Message: "unsupported"}
				}
				return map[string]string{"outcome": outcome}, nil
			})
			agent = remote
			done := make(chan error, 1)
			go func() { _, err := c.Prompt("Original"); done <- err }()
			<-started
			result, err := c.SteerContent(c.current.ID, []acp.Content{acp.NewTextContent("Follow-up")})
			fallback := outcome == "promptRequired" || outcome == "unsupported"
			if fallback && (err != nil || result != SteeringPromptRequired) || !fallback && err == nil {
				t.Fatalf("outcome=%s err=%v", result, err)
			}
			if c.CanSteer() == (outcome == "unsupported") {
				t.Fatal("incorrect capability after fallback")
			}
			close(finish)
			if err := <-done; err != nil {
				t.Fatal(err)
			}
			snapshot, _ := c.Snapshot()
			if len(snapshot.Messages) != 1 {
				t.Fatalf("undelivered steer left in transcript: %+v", snapshot.Messages)
			}
		})
	}
}

func TestSteeringDetachedContinuationOwnsCompletion(t *testing.T) {
	for _, earlyFinish := range []bool{false, true} {
		t.Run(map[bool]string{false: "streaming", true: "finished-before-ack"}[earlyFinish], func(t *testing.T) {
			started, originalDone, originalIdle := make(chan struct{}), make(chan struct{}), make(chan struct{})
			var agent *acp.Connection
			c, remote := steeringClient(t, func(ctx context.Context, method string, raw json.RawMessage) (any, error) {
				if method == "session/prompt" {
					steeringStatusUpdate(t, agent, "active")
					close(started)
					select {
					case <-originalDone:
					case <-ctx.Done():
						return nil, ctx.Err()
					}
					steeringStatusUpdate(t, agent, "idle")
					close(originalIdle)
					return schema.PromptResponse{StopReason: schema.StopReasonEndTurn}, nil
				}
				close(originalDone)
				<-originalIdle
				steeringStatusUpdate(t, agent, "active")
				if earlyFinish {
					steeringStatusUpdate(t, agent, "idle")
				}
				return map[string]string{"outcome": "startedNewTurn"}, nil
			})
			agent = remote
			done := make(chan error, 1)
			go func() { _, err := c.Prompt("Original"); done <- err }()
			<-started
			outcome, err := c.SteerContent(c.current.ID, []acp.Content{acp.NewTextContent("Late guidance")})
			if err != nil || outcome != SteeringStartedNewTurn {
				t.Fatalf("%s: %v", outcome, err)
			}
			if !earlyFinish {
				select {
				case err := <-done:
					t.Fatalf("returned while detached turn still running: %v", err)
				case <-time.After(20 * time.Millisecond):
				}
				steeringStatusUpdate(t, agent, "idle")
			}
			if err := <-done; err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestSteeringWithoutActiveTurnLeavesInputWithCaller(t *testing.T) {
	c, _ := steeringClient(t, func(context.Context, string, json.RawMessage) (any, error) {
		t.Error("idle steering sent a request")
		return nil, nil
	})
	result, err := c.SteerContent(c.current.ID, []acp.Content{acp.NewTextContent("next")})
	if err != nil || result != SteeringPromptRequired {
		t.Fatalf("%s: %v", result, err)
	}
	_, err = c.SteerContent("wrong-session", []acp.Content{acp.NewTextContent("next")})
	if err == nil || !strings.Contains(err.Error(), "another session") {
		t.Fatalf("session guard: %v", err)
	}
}

func TestSteeringAfterPromptResponseDoesNotStartDetachedWork(t *testing.T) {
	c, _ := steeringClient(t, func(context.Context, string, json.RawMessage) (any, error) {
		t.Error("completed prompt was steered again")
		return nil, nil
	})
	// Another delivery can keep the local turn open briefly after its RPC ends.
	c.turnDone = make(chan struct{})
	c.steering = steeringState{ready: true, promptReturned: true, wake: make(chan struct{})}
	outcome, err := c.SteerContent(c.current.ID, []acp.Content{acp.NewTextContent("follow-up")})
	if err != nil || outcome != SteeringPromptRequired || c.steering.pending != 0 || len(c.current.Messages) != 0 {
		t.Fatalf("completed turn retained or consumed input: %s %v", outcome, err)
	}
}

func TestSteeringWaitsForPromptActivity(t *testing.T) {
	for _, streams := range []bool{false, true} {
		t.Run(map[bool]string{false: "no-output", true: "streaming"}[streams], func(t *testing.T) {
			started, finish := make(chan struct{}), make(chan struct{})
			called := make(chan struct{}, 1)
			c, remote := steeringClient(t, func(ctx context.Context, method string, raw json.RawMessage) (any, error) {
				if method == "session/prompt" {
					close(started)
					select {
					case <-finish:
					case <-ctx.Done():
						return nil, ctx.Err()
					}
					return schema.PromptResponse{StopReason: schema.StopReasonEndTurn}, nil
				}
				called <- struct{}{}
				return map[string]string{"outcome": "injected"}, nil
			})
			promptDone := make(chan error, 1)
			go func() { _, err := c.Prompt("Original"); promptDone <- err }()
			<-started
			steerDone := make(chan SteeringOutcome, 1)
			go func() {
				outcome, err := c.SteerContent(c.current.ID, []acp.Content{acp.NewTextContent("Fast follow-up")})
				if err != nil {
					t.Error(err)
				}
				steerDone <- outcome
			}()
			select {
			case <-called:
				t.Fatal("steering overtook prompt readiness")
			case <-time.After(20 * time.Millisecond):
			}
			want := SteeringPromptRequired
			if streams {
				steeringStatusUpdate(t, remote, "active")
				want = SteeringInjected
			} else {
				close(finish)
			}
			if outcome := <-steerDone; outcome != want {
				t.Fatalf("got %s, want %s", outcome, want)
			}
			if streams {
				close(finish)
			}
			if err := <-promptDone; err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestDetachedSteeringDisconnectUnblocksTurn(t *testing.T) {
	started, finish, idle := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var agent *acp.Connection
	c, remote := steeringClient(t, func(ctx context.Context, method string, raw json.RawMessage) (any, error) {
		if method == "session/prompt" {
			steeringStatusUpdate(t, agent, "active")
			close(started)
			select {
			case <-finish:
			case <-ctx.Done():
				return nil, ctx.Err()
			}
			steeringStatusUpdate(t, agent, "idle")
			close(idle)
			return schema.PromptResponse{StopReason: schema.StopReasonEndTurn}, nil
		}
		close(finish)
		<-idle
		steeringStatusUpdate(t, agent, "active")
		return map[string]string{"outcome": "startedNewTurn"}, nil
	})
	agent = remote
	done := make(chan error, 1)
	go func() { _, err := c.Prompt("Original"); done <- err }()
	<-started
	if _, err := c.SteerContent(c.current.ID, []acp.Content{acp.NewTextContent("late")}); err != nil {
		t.Fatal(err)
	}
	_ = remote.Close()
	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "disconnected") {
			t.Fatalf("missing disconnect: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("detached continuation stayed busy after disconnect")
	}
}
