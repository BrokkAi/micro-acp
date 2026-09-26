package client

import "github.com/BrokkAi/acp-go/schema"

func unfinishedTool(tool *schema.ToolCall) bool {
	return tool != nil && (tool.Status == nil || (*tool.Status != schema.ToolCallStatusCompleted && *tool.Status != schema.ToolCallStatusFailed))
}

// Called with c.mu held. Keep the wire status intact: cancellation is local,
// and a final completed/failed update from the agent must still take precedence.
func (c *Client) cancelTurnTools() {
	for i := c.turnStart; i < len(c.current.Messages); i++ {
		m := &c.current.Messages[i]
		if m.Role == "tool" && unfinishedTool(m.Tool) && !m.Cancelled {
			m.Cancelled = true
			c.revision++
		}
	}
}
