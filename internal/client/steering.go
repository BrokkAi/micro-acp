package client

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	acp "github.com/BrokkAi/acp-go"
	"github.com/BrokkAi/acp-go/schema"
	"github.com/BrokkAi/micro-acp/internal/store"
)

type SteeringOutcome string

const (
	SteeringInjected       SteeringOutcome = "injected"
	SteeringStartedNewTurn SteeringOutcome = "startedNewTurn"
	SteeringPromptRequired SteeringOutcome = "promptRequired"
)

type steeringState struct {
	pending                        int
	sequence                       uint64
	closing, disabled              bool
	ready, promptReturned          bool
	wake                           chan struct{}
	status                         string
	generation, detachedGeneration uint64
}

func (c *Client) CanSteer() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	capability, _ := c.Init.Meta["steering"].(map[string]any)
	return capability["supported"] == true && !c.steering.disabled
}

// SteerContent joins an active prompt without cancelling it or taking the
// session-operation lock. Only explicit non-delivery permits automatic fallback.
func (c *Client) SteerContent(sessionID string, blocks []acp.Content) (outcome SteeringOutcome, err error) {
	c.steerOp.Lock()
	defer c.steerOp.Unlock()
	if err := c.validateContent(blocks); err != nil {
		return "", err
	}
	if !c.CanSteer() {
		return SteeringPromptRequired, nil
	}
	c.mu.Lock()
	if c.current.ID != sessionID {
		c.mu.Unlock()
		return "", errors.New("steering belongs to another session")
	}
	if c.turnDone == nil || c.steering.closing {
		c.mu.Unlock()
		return SteeringPromptRequired, nil
	}
	if c.cancelRequested {
		c.mu.Unlock()
		return "", errors.New("turn is stopping; follow-up retained in queue")
	}
	c.steering.pending++
	c.steering.sequence++
	id := fmt.Sprintf("steer-%d", c.steering.sequence)
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		defer c.mu.Unlock()
		for i := range c.current.Messages {
			message := &c.current.Messages[i]
			if message.Pending && message.ID == id {
				if err == nil && (outcome == SteeringInjected || outcome == SteeringStartedNewTurn) {
					message.Pending = false
				} else {
					c.current.Messages = append(c.current.Messages[:i], c.current.Messages[i+1:]...)
				}
				break
			}
		}
		c.steering.pending--
		c.current.UpdatedAt = time.Now().UTC()
		c.revision++
		c.wakeSteering()
	}()
	// The prompt may still be saving its initial history before writing the RPC.
	// Wait for turn activity or its response so steering cannot overtake it.
	for {
		c.mu.Lock()
		s := c.steering
		stopping := c.cancelRequested
		if s.ready || s.promptReturned || stopping {
			c.mu.Unlock()
			if stopping {
				return "", errors.New("turn is stopping; follow-up retained in queue")
			}
			if !s.ready {
				return SteeringPromptRequired, nil
			}
			break
		}
		c.mu.Unlock()
		select {
		case <-s.wake:
		case <-c.conn.Done():
			return "", errors.New("agent disconnected before steering could be sent")
		case <-c.ctx.Done():
			return "", c.ctx.Err()
		}
	}
	c.mu.Lock()
	nextGeneration := max(uint64(1), c.steering.generation) + 1
	var text []string
	for _, block := range blocks {
		text = append(text, ContentText(block))
	}
	c.current.Messages = append(c.current.Messages, store.Message{Role: "user", ID: id, Text: strings.Join(text, "\n"), Content: blocks, Pending: true})
	remoteID := c.wire.SessionID
	c.revision++
	c.mu.Unlock()
	request := schema.PromptRequest{SessionID: remoteID, Prompt: blocks, Meta: schema.Meta{"steering": map[string]any{"idleBehavior": "promptRequired"}}}
	var response struct {
		Outcome SteeringOutcome `json:"outcome"`
	}
	if err = c.conn.Call(c.ctx, "_session/steering", request, &response); err != nil {
		var rpcErr *acp.RPCError
		if errors.As(err, &rpcErr) && rpcErr.Code == -32601 {
			c.mu.Lock()
			c.steering.disabled = true
			c.mu.Unlock()
			return SteeringPromptRequired, nil
		}
		return "", fmt.Errorf("steering delivery was not confirmed; check the transcript before retrying: %w", err)
	}
	switch response.Outcome {
	case SteeringInjected, SteeringPromptRequired:
		return response.Outcome, nil
	case SteeringStartedNewTurn:
		// Codex starts a detached continuation if the original turn settled.
		// Its thread-status updates own completion; never send the input again.
		c.mu.Lock()
		c.steering.detachedGeneration = nextGeneration
		stopping := c.cancelRequested
		c.mu.Unlock()
		if stopping {
			_ = c.Cancel()
		}
		return response.Outcome, nil
	case "failed":
		return "", errors.New("agent rejected steering; follow-up retained in queue")
	default:
		return "", fmt.Errorf("unknown steering outcome %q; check the transcript before retrying", response.Outcome)
	}
}

// Called with mu held. These metadata updates describe Codex's detached turn
// lifecycle; Claude honors the promptRequired opt-in instead.
func (c *Client) steeringStatus(meta schema.Meta) {
	codex, _ := meta["codex"].(map[string]any)
	status, _ := codex["threadStatus"].(map[string]any)
	value, _ := status["type"].(string)
	if value != "active" && value != "idle" && value != "systemError" {
		return
	}
	if value == "active" && c.steering.status != "active" {
		c.steering.generation++
		c.steering.ready = true
	}
	c.steering.status = value
	c.wakeSteering()
}

func (c *Client) wakeSteering() {
	if c.steering.wake != nil {
		close(c.steering.wake)
		c.steering.wake = make(chan struct{})
	}
}

func (c *Client) finishSteering(ctx context.Context) error {
	c.mu.Lock()
	c.steering.promptReturned = true
	c.wakeSteering()
	c.mu.Unlock()
	for {
		c.mu.Lock()
		s := c.steering
		detachedDone := s.detachedGeneration == 0 || (s.generation >= s.detachedGeneration && (s.status == "idle" || s.status == "systemError"))
		if s.pending == 0 && detachedDone {
			c.steering.closing = true
			c.mu.Unlock()
			if s.detachedGeneration != 0 && s.status == "systemError" {
				return errors.New("steered continuation failed")
			}
			return nil
		}
		c.mu.Unlock()
		select {
		case <-s.wake:
		case <-c.conn.Done():
			return fmt.Errorf("agent disconnected during steering: %w", c.conn.Err())
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}
