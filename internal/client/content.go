package client

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/BrokkAi/acp-go/schema"
)

func ContentText(block schema.ContentBlock) string {
	switch {
	case block.Text != nil:
		return block.Text.Text
	case block.Image != nil:
		return "[image: " + block.Image.MimeType + "]"
	case block.Audio != nil:
		return "[audio: " + block.Audio.MimeType + "]"
	case block.ResourceLink != nil:
		return fmt.Sprintf("[%s](%s)", block.ResourceLink.Name, block.ResourceLink.URI)
	case block.Resource != nil:
		r := block.Resource.Resource
		if r.TextResourceContents != nil {
			return r.TextResourceContents.URI + "\n" + r.TextResourceContents.Text
		}
		if r.BlobResourceContents != nil {
			return "[resource: " + r.BlobResourceContents.URI + "]"
		}
	}
	return "[unsupported content]"
}

func ToolText(tool schema.ToolCall) string {
	var text strings.Builder
	fmt.Fprintf(&text, "%s", tool.Title)
	if tool.Status != nil {
		fmt.Fprintf(&text, " · %s", *tool.Status)
	}
	if tool.Name != nil {
		fmt.Fprintf(&text, "\nTool: %s", *tool.Name)
	}
	for _, location := range tool.Locations {
		fmt.Fprintf(&text, "\n%s", location.Path)
		if location.Line != nil {
			fmt.Fprintf(&text, ":%d", *location.Line)
		}
	}
	writeJSON := func(label string, raw json.RawMessage) {
		if len(raw) == 0 {
			return
		}
		var value any
		if json.Unmarshal(raw, &value) == nil {
			pretty, _ := json.MarshalIndent(value, "", "  ")
			raw = pretty
		}
		fmt.Fprintf(&text, "\n%s:\n%s", label, raw)
	}
	writeJSON("Input", tool.RawInput)
	for _, part := range tool.Content {
		switch {
		case part.Content != nil:
			fmt.Fprintf(&text, "\n%s", ContentText(part.Content.Content))
		case part.Diff != nil:
			d := part.Diff
			fmt.Fprintf(&text, "\n--- %s\n+++ %s", d.Path, d.Path)
			if d.OldText != nil {
				for _, line := range strings.Split(*d.OldText, "\n") {
					fmt.Fprintf(&text, "\n- %s", line)
				}
			}
			for _, line := range strings.Split(d.NewText, "\n") {
				fmt.Fprintf(&text, "\n+ %s", line)
			}
		case part.Terminal != nil:
			fmt.Fprintf(&text, "\nTerminal: %s", part.Terminal.TerminalID)
		}
	}
	writeJSON("Output", tool.RawOutput)
	return text.String()
}

func mergeTool(old schema.ToolCall, patch schema.ToolCallUpdate) schema.ToolCall {
	if patch.Title != nil {
		old.Title = *patch.Title
	}
	if patch.Status != nil {
		old.Status = patch.Status
	}
	if patch.Kind != nil {
		old.Kind = patch.Kind
	}
	if patch.Name != nil {
		old.Name = patch.Name
	}
	if patch.Content != nil {
		old.Content = patch.Content
	}
	if patch.Locations != nil {
		old.Locations = patch.Locations
	}
	if patch.RawInput != nil {
		old.RawInput = patch.RawInput
	}
	if patch.RawOutput != nil {
		old.RawOutput = patch.RawOutput
	}
	return old
}
