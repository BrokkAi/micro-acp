package client

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	acp "github.com/BrokkAi/acp-go"
	"github.com/BrokkAi/acp-go/schema"
	draft "github.com/BrokkAi/acp-go/schema/v2/unstable"
	"github.com/BrokkAi/micro-acp/internal/store"
)

// maxTerminalOutput bounds the output kept for one agent-owned terminal.
const maxTerminalOutput = 256 << 10

// agentTerminal is the stored state of a v2 agent-owned, display-only terminal.
type agentTerminal struct {
	command, cwd string
	output       []byte
	exit         *draft.TerminalExitStatus
}

func (c *Client) v2Notification(method string, raw json.RawMessage) error {
	switch method {
	case draft.ElicitationCompleteMethodName:
		return c.elicitationComplete(raw)
	case draft.SessionUpdateMethodName:
	default:
		return nil
	}
	var envelope struct {
		SessionID string          `json:"sessionId"`
		Update    json.RawMessage `json:"update"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return err
	}
	// Unknown or malformed updates must not end a healthy connection.
	var u draft.SessionUpdate
	if json.Unmarshal(envelope.Update, &u) != nil {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.current.ID == "" {
		return nil
	}
	id := envelope.SessionID
	root := c.current.RemoteID == "" || c.current.RemoteID == id
	if !root {
		if c.subagent(id) == nil {
			return nil
		}
		c.v2ChildUpdate(id, u, envelope.Update)
		c.revision++
		return nil
	}
	if c.current.RemoteID == "" && id != "" {
		c.current.RemoteID = id
		c.wire.SessionID = schema.SessionId(id)
	}
	switch {
	case u.StateUpdate != nil:
		state := u.StateUpdate
		switch {
		case state.Running != nil:
			c.v2.state = TurnRunning
		case state.RequiresAction != nil:
			c.v2.state = TurnRequiresAction
		case state.Idle != nil:
			c.v2.state = TurnIdle
			c.v2.idleMeta, _ = json.Marshal(state.Idle.Meta)
		default:
			c.v2.state = TurnUnknown
		}
	case u.UserMessage != nil || u.UserMessageChunk != nil:
		messageID := ""
		if u.UserMessage != nil {
			messageID = string(u.UserMessage.MessageID)
		} else {
			messageID = string(u.UserMessageChunk.MessageID)
		}
		// The agent echoes the prompt it took in. The prompt is already in the
		// transcript, so only record its ID.
		if !c.replaying && c.v2.echo && c.v2.echoID == "" && c.turnStart < len(c.current.Messages) {
			c.v2.echoID = messageID
			c.current.Messages[c.turnStart].ID = messageID
		}
		if !c.replaying && messageID == c.v2.echoID {
			break
		}
		applyV2Message(&c.current.Messages, u)
	case u.PlanUpdate != nil:
		c.current.Plan = applyPlan(&c.current.Messages, u.PlanUpdate)
	case u.PlanRemoved != nil:
		c.current.Plan = nil
	case u.SessionInfoUpdate != nil:
		info := u.SessionInfoUpdate
		if info.Title.Set {
			c.current.Title = info.Title.Value
		}
		if info.UpdatedAt.Set && !info.UpdatedAt.Null {
			if stamp, err := time.Parse(time.RFC3339, info.UpdatedAt.Value); err == nil {
				c.current.UpdatedAt = stamp
			}
		}
	case u.AvailableCommandsUpdate != nil:
		if commands, err := convert[[]schema.AvailableCommand](u.AvailableCommandsUpdate.AvailableCommands); err == nil {
			c.current.Commands = commands
		}
	case u.UsageUpdate != nil:
		if usage, err := convert[schema.UsageUpdate](u.UsageUpdate); err == nil {
			c.current.Usage = &usage
		}
	case u.ConfigOptionUpdate != nil:
		c.wire.ConfigOptions = v1ConfigOptions(u.ConfigOptionUpdate.ConfigOptions)
	case u.SubagentUpdate != nil:
		c.subagentUpdate(c.current.RemoteID, string(u.Kind), envelope.Update)
	case u.SessionMessage != nil || u.SessionMessageChunk != nil:
		c.sessionMessage(&c.current.Messages, string(u.Kind), envelope.Update)
	default:
		c.applyV2(id, &c.current.Messages, u, c.turnDone != nil && c.cancelRequested)
	}
	c.revision++
	return nil
}

// v2ChildUpdate applies an update for an announced subagent. A child reports
// its own turn state on its session. Called with mu held.
func (c *Client) v2ChildUpdate(id string, u draft.SessionUpdate, raw json.RawMessage) {
	switch {
	case u.SubagentUpdate != nil:
		c.subagentUpdate(id, string(u.Kind), raw)
	case u.SessionMessage != nil || u.SessionMessageChunk != nil:
		c.sessionMessage(&c.subagent(id).Messages, string(u.Kind), raw)
	case u.StateUpdate != nil:
		child := c.subagent(id)
		child.StopReason = ""
		switch state := u.StateUpdate; {
		case state.Running != nil:
			child.State = store.SubagentRunning
		case state.RequiresAction != nil:
			child.State = store.SubagentRequiresAction
		case state.Idle != nil:
			child.State = store.SubagentIdle
			if state.Idle.StopReason != nil {
				child.StopReason = string(*state.Idle.StopReason)
			}
		default:
			child.State = store.SubagentUnknown
		}
	case u.PlanUpdate != nil:
		applyPlan(&c.subagent(id).Messages, u.PlanUpdate)
	default:
		c.applyV2(id, &c.subagent(id).Messages, u, false)
	}
}

// applyV2 adds transcript content from one update: messages, tool calls and
// agent-owned terminals. Called with mu held.
func (c *Client) applyV2(session string, messages *[]store.Message, u draft.SessionUpdate, cancelled bool) {
	switch {
	case u.UserMessage != nil || u.UserMessageChunk != nil || u.AgentMessage != nil || u.AgentMessageChunk != nil || u.AgentThought != nil || u.AgentThoughtChunk != nil:
		applyV2Message(messages, u)
	case u.ToolCallUpdate != nil:
		message := toolMessage(messages, string(u.ToolCallUpdate.ToolCallID), cancelled)
		mergeV2Tool(message, *u.ToolCallUpdate)
	case u.ToolCallContentChunk != nil:
		message := toolMessage(messages, string(u.ToolCallContentChunk.ToolCallID), cancelled)
		content, changes, patch := v1ToolContent([]draft.ToolCallContent{u.ToolCallContentChunk.Content})
		message.Tool.Content = append(message.Tool.Content, content...)
		message.Changes = append(message.Changes, changes...)
		message.Patch = joinPatch(message.Patch, patch)
		message.Text = toolMessageText(*message)
	case u.TerminalUpdate != nil:
		t := c.agentTerminal(session, string(u.TerminalUpdate.TerminalID))
		update := u.TerminalUpdate
		if update.Command.Set {
			t.command = update.Command.Value
		}
		if update.Cwd != nil {
			t.cwd = string(*update.Cwd)
		}
		if update.Output != nil {
			t.output = decodeOutput(update.Output.Data)
		}
		if update.ExitStatus != nil {
			t.exit = update.ExitStatus
		}
		showTerminal(messages, string(update.TerminalID), t)
	case u.TerminalOutputChunk != nil:
		t := c.agentTerminal(session, string(u.TerminalOutputChunk.TerminalID))
		t.output = append(t.output, decodeOutput(u.TerminalOutputChunk.Data)...)
		if len(t.output) > maxTerminalOutput {
			t.output = t.output[len(t.output)-maxTerminalOutput:]
		}
		showTerminal(messages, string(u.TerminalOutputChunk.TerminalID), t)
	case u.Notice != nil:
		notice := u.Notice
		text := notice.Title
		switch notice.Severity {
		case draft.NoticeSeverityWarning:
			text = "Warning: " + text
		case draft.NoticeSeverityError:
			text = "Error: " + text
		}
		if description := nullableText(notice.Description); description != nil && *description != "" {
			text += "\n" + *description
		}
		*messages = append(*messages, store.Message{Role: "notice", Text: text})
	case u.CompactionUpdate != nil:
		update := u.CompactionUpdate
		message := findMessage(messages, "notice", "compaction:"+string(update.CompactionID))
		summary := ""
		if message != nil {
			summary = strings.TrimPrefix(message.Text, compactionText(message.Text))
		}
		if update.Summary.Set {
			summary = ""
			for _, block := range update.Summary.Value {
				summary += v1Text(block)
			}
			if summary != "" {
				summary = "\n" + summary
			}
		}
		head := "Compacting the conversation…"
		switch update.Status {
		case draft.CompactionStatusCompleted:
			head = "Conversation compacted"
		case draft.CompactionStatusCancelled:
			head = "Compaction cancelled"
		case draft.CompactionStatusFailed:
			head = "Compaction failed"
			if update.Error.Set && !update.Error.Null {
				head += ": " + update.Error.Value
			}
		}
		if message == nil {
			*messages = append(*messages, store.Message{Role: "notice", ID: "compaction:" + string(update.CompactionID)})
			message = &(*messages)[len(*messages)-1]
		}
		message.Text = head + summary
	case u.CompactionSummaryChunk != nil:
		chunk := u.CompactionSummaryChunk
		message := findMessage(messages, "notice", "compaction:"+string(chunk.CompactionID))
		if message == nil {
			*messages = append(*messages, store.Message{Role: "notice", ID: "compaction:" + string(chunk.CompactionID), Text: "Compacting the conversation…\n"})
			message = &(*messages)[len(*messages)-1]
		}
		if !strings.Contains(message.Text, "\n") {
			message.Text += "\n"
		}
		message.Text += v1Text(chunk.Content)
	}
}

// compactionText is the status line of a compaction notice.
func compactionText(text string) string {
	head, _, _ := strings.Cut(text, "\n")
	return head
}

func (c *Client) agentTerminal(session, id string) *agentTerminal {
	key := session + "\x00" + id
	t := c.v2.terminals[key]
	if t == nil {
		t = &agentTerminal{}
		c.v2.terminals[key] = t
	}
	return t
}

func decodeOutput(data string) []byte {
	decoded, err := base64.StdEncoding.DecodeString(data)
	if err != nil {
		return []byte(data)
	}
	return decoded
}

// showTerminal writes a terminal's state in the transcript form the v1
// client-hosted terminals use: command line first, then output and exit.
func showTerminal(messages *[]store.Message, id string, t *agentTerminal) {
	title := t.command
	if title == "" {
		title = "Terminal " + id
	}
	text := title + "\n" + strings.TrimRight(string(t.output), "\n")
	if t.exit != nil {
		switch {
		case t.exit.ExitCode.Set && !t.exit.ExitCode.Null:
			text += fmt.Sprintf("\nExit: %d", t.exit.ExitCode.Value)
		case t.exit.Signal.Set && !t.exit.Signal.Null:
			text += "\nSignal: " + t.exit.Signal.Value
		default:
			text += "\nExited"
		}
	}
	if message := findMessage(messages, "terminal", id); message != nil {
		message.Text = text
		return
	}
	*messages = append(*messages, store.Message{Role: "terminal", ID: id, Text: text})
}

func findMessage(messages *[]store.Message, role, id string) *store.Message {
	for i := len(*messages) - 1; i >= 0; i-- {
		if m := &(*messages)[i]; m.Role == role && m.ID == id {
			return m
		}
	}
	return nil
}

func v1Block(block draft.ContentBlock) schema.ContentBlock {
	converted, err := convert[schema.ContentBlock](block)
	if err != nil {
		return acp.NewTextContent("[unsupported content]")
	}
	return converted
}

func v1Text(block draft.ContentBlock) string { return ContentText(v1Block(block)) }

// applyV2Message applies a message upsert or chunk, keyed by role and message
// ID. Like v1 chunks, text goes to Text and other blocks to Content. A replayed
// message is cleared with empty content first, then its chunks follow.
func applyV2Message(messages *[]store.Message, u draft.SessionUpdate) {
	var role string
	var id draft.MessageId
	var upsert *draft.Nullable[[]draft.ContentBlock]
	var chunk *draft.ContentChunk
	switch {
	case u.UserMessage != nil:
		role, id, upsert = "user", u.UserMessage.MessageID, &u.UserMessage.Content
	case u.UserMessageChunk != nil:
		role, id, chunk = "user", u.UserMessageChunk.MessageID, u.UserMessageChunk
	case u.AgentMessage != nil:
		role, id, upsert = "assistant", u.AgentMessage.MessageID, &u.AgentMessage.Content
	case u.AgentMessageChunk != nil:
		role, id, chunk = "assistant", u.AgentMessageChunk.MessageID, u.AgentMessageChunk
	case u.AgentThought != nil:
		role, id, upsert = "thought", u.AgentThought.MessageID, &u.AgentThought.Content
	case u.AgentThoughtChunk != nil:
		role, id, chunk = "thought", u.AgentThoughtChunk.MessageID, u.AgentThoughtChunk
	default:
		return
	}
	message := findMessage(messages, role, string(id))
	if message == nil {
		*messages = append(*messages, store.Message{Role: role, ID: string(id)})
		message = &(*messages)[len(*messages)-1]
	}
	add := func(block draft.ContentBlock) {
		converted := v1Block(block)
		message.Text += ContentText(converted)
		if converted.Text == nil {
			message.Content = append(message.Content, converted)
		}
	}
	if chunk != nil {
		add(chunk.Content)
		return
	}
	if !upsert.Set {
		return
	}
	message.Text, message.Content = "", nil
	for _, block := range upsert.Value {
		add(block)
	}
}

func toolMessage(messages *[]store.Message, id string, cancelled bool) *store.Message {
	if message := findMessage(messages, "tool", id); message != nil {
		if message.Tool == nil {
			message.Tool = &schema.ToolCall{ToolCallID: schema.ToolCallId(id), Title: message.Text}
		}
		return message
	}
	*messages = append(*messages, store.Message{Role: "tool", ID: id, Tool: &schema.ToolCall{ToolCallID: schema.ToolCallId(id)}, Cancelled: cancelled})
	return &(*messages)[len(*messages)-1]
}

// mergeV2Tool applies a tool_call_update upsert with v2 patch semantics:
// omitted fields are kept, null clears, values replace.
func mergeV2Tool(message *store.Message, update draft.ToolCallUpdate) {
	tool := message.Tool
	if update.Title.Set {
		tool.Title = update.Title.Value
	}
	if update.Status != nil {
		status := schema.ToolCallStatus(*update.Status)
		tool.Status = &status
	}
	if update.Kind != nil {
		kind := schema.ToolKind(*update.Kind)
		tool.Kind = &kind
	}
	if update.Name.Set {
		tool.Name = nullableText(update.Name)
	}
	if update.Locations.Set {
		tool.Locations = nil
		for _, location := range update.Locations.Value {
			converted := schema.ToolCallLocation{Path: string(location.Path)}
			if location.Line.Set && !location.Line.Null {
				line := location.Line.Value
				converted.Line = &line
			}
			tool.Locations = append(tool.Locations, converted)
		}
	}
	if hasJSONValue(update.RawInput) {
		tool.RawInput = update.RawInput
	}
	if hasJSONValue(update.RawOutput) {
		tool.RawOutput = update.RawOutput
	}
	if update.Content.Set {
		tool.Content, message.Changes, message.Patch = v1ToolContent(update.Content.Value)
	}
	message.Cancelled = message.Cancelled && unfinishedTool(tool)
	message.Text = toolMessageText(*message)
}

func toolMessageText(message store.Message) string {
	text := ToolText(*message.Tool)
	for _, change := range message.Changes {
		text += "\n" + change.String()
	}
	if message.Patch != "" {
		text += "\n" + message.Patch
	}
	return text
}

// v1ToolContent splits v2 tool content into v1 content and the structured
// file changes v1 has no type for.
func v1ToolContent(parts []draft.ToolCallContent) (content []schema.ToolCallContent, changes []store.FileChange, patch string) {
	for _, part := range parts {
		switch {
		case part.Content != nil:
			content = append(content, schema.ToolCallContent{Content: &schema.Content{Content: v1Block(part.Content.Content)}})
		case part.Terminal != nil:
			content = append(content, schema.ToolCallContent{Terminal: &schema.Terminal{TerminalID: schema.TerminalId(part.Terminal.TerminalID)}})
		case part.Diff != nil:
			for _, change := range part.Diff.Changes {
				changes = append(changes, fileChange(change))
			}
			if part.Diff.Patch != nil {
				patch = joinPatch(patch, part.Diff.Patch.Text)
			}
		}
	}
	return content, changes, patch
}

func joinPatch(patch, more string) string {
	if patch == "" || more == "" {
		return patch + more
	}
	return strings.TrimRight(patch, "\n") + "\n" + more
}

func fileChange(change draft.DiffChange) store.FileChange {
	switch {
	case change.Add != nil:
		return store.FileChange{Operation: "add", Path: string(change.Add.Path)}
	case change.Delete != nil:
		return store.FileChange{Operation: "delete", Path: string(change.Delete.Path)}
	case change.Modify != nil:
		return store.FileChange{Operation: "modify", Path: string(change.Modify.Path)}
	case change.Move != nil:
		return store.FileChange{Operation: "move", Path: string(change.Move.Path), OldPath: string(change.Move.OldPath)}
	case change.Copy != nil:
		return store.FileChange{Operation: "copy", Path: string(change.Copy.Path), OldPath: string(change.Copy.OldPath)}
	}
	return store.FileChange{Operation: string(change.Kind)}
}

// applyPlan records a plan update. A checklist becomes the live plan and a
// snapshot in the transcript; a Markdown plan is shown as a message and a
// plan file as a notice. It returns the checklist, or nil for other kinds.
func applyPlan(messages *[]store.Message, update *draft.PlanUpdate) *schema.Plan {
	content := update.Plan
	upsert := func(role, id, text string) {
		if message := findMessage(messages, role, id); message != nil {
			message.Text = text
			return
		}
		*messages = append(*messages, store.Message{Role: role, ID: id, Text: text})
	}
	switch {
	case content.Items != nil:
		plan := &schema.Plan{Entries: []schema.PlanEntry{}}
		var lines []string
		for _, entry := range content.Items.Entries {
			plan.Entries = append(plan.Entries, schema.PlanEntry{Content: entry.Content, Priority: schema.PlanEntryPriority(entry.Priority), Status: schema.PlanEntryStatus(entry.Status)})
			lines = append(lines, string(entry.Status)+"  ["+string(entry.Priority)+"] "+entry.Content)
		}
		*messages = append(*messages, store.Message{Role: "plan", Text: strings.Join(lines, "\n")})
		return plan
	case content.Markdown != nil:
		upsert("assistant", "plan:"+string(content.Markdown.PlanID), "**Plan**\n\n"+content.Markdown.Content)
	case content.File != nil:
		upsert("notice", "plan:"+string(content.File.PlanID), "Plan: "+content.File.URI)
	}
	return nil
}

// v1ConfigOptions maps v2 session options, which name their ID configId and
// their groups groupId, to the v1 shape the selectors read. Unknown option
// kinds are skipped.
func v1ConfigOptions(value any) []schema.SessionConfigOption {
	raw, err := convert[[]map[string]json.RawMessage](value)
	if err != nil {
		return nil
	}
	// codegraff 0.0.302 sends the v1 key "id" for options on v2 connections.
	for _, option := range raw {
		if _, ok := option["configId"]; !ok {
			option["configId"] = option["id"]
		}
	}
	options, err := convert[[]draft.SessionConfigOption](raw)
	if err != nil {
		return nil
	}
	result := []schema.SessionConfigOption{}
	for _, option := range options {
		converted := schema.SessionConfigOption{ID: schema.SessionConfigId(option.ConfigID), Name: option.Name, Description: nullableText(option.Description)}
		if option.Category != nil {
			category := schema.SessionConfigOptionCategory(*option.Category)
			converted.Category = &category
		}
		switch {
		case option.Select != nil:
			var entries []map[string]any
			b, _ := json.Marshal(option.Select.Options)
			_ = json.Unmarshal(b, &entries)
			for _, entry := range entries {
				if group, ok := entry["groupId"]; ok {
					entry["group"] = group
					delete(entry, "groupId")
				}
			}
			converted.Select = &schema.SessionConfigSelect{CurrentValue: schema.SessionConfigValueId(option.Select.CurrentValue), Options: entries}
		case option.Boolean != nil:
			converted.Boolean = &schema.SessionConfigBoolean{CurrentValue: option.Boolean.CurrentValue}
		default:
			continue
		}
		result = append(result, converted)
	}
	return result
}
