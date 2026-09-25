package client

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	acp "github.com/BrokkAi/acp-go"
	"github.com/BrokkAi/acp-go/schema"
)

func (c *Client) validateContent(blocks []acp.Content) error {
	if len(blocks) == 0 {
		return fmt.Errorf("prompt is empty")
	}
	var caps schema.PromptCapabilities
	if c.Init.AgentCapabilities != nil && c.Init.AgentCapabilities.PromptCapabilities != nil {
		caps = *c.Init.AgentCapabilities.PromptCapabilities
	}
	on := func(v *bool) bool { return v != nil && *v }
	for _, block := range blocks {
		if _, err := json.Marshal(block); err != nil {
			return err
		}
		if block.Image != nil && !on(caps.Image) {
			return fmt.Errorf("agent does not accept images")
		}
		if block.Audio != nil && !on(caps.Audio) {
			return fmt.Errorf("agent does not accept audio")
		}
		if block.Resource != nil && !on(caps.EmbeddedContext) {
			return fmt.Errorf("agent does not accept embedded resources")
		}
	}
	return nil
}

// Attachment converts a user-selected local file to the richest advertised content.
func (c *Client) Attachment(path string) (acp.Content, error) {
	if !filepath.IsAbs(path) {
		path = filepath.Join(c.Cwd, path)
	}
	path, err := filepath.Abs(path)
	if err != nil {
		return acp.Content{}, err
	}
	info, err := os.Stat(path)
	if err != nil {
		return acp.Content{}, err
	}
	if !info.Mode().IsRegular() {
		return acp.Content{}, fmt.Errorf("attachment must be a regular file")
	}
	const limit = 4 << 20
	if info.Size() > limit {
		return acp.Content{}, fmt.Errorf("attachment exceeds 4 MiB")
	}
	f, err := os.Open(path)
	if err != nil {
		return acp.Content{}, err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return acp.Content{}, err
	}
	if len(b) > limit {
		return acp.Content{}, fmt.Errorf("attachment exceeds 4 MiB")
	}
	kind := mime.TypeByExtension(filepath.Ext(path))
	if kind == "" {
		kind = http.DetectContentType(b)
	}
	kind = strings.Split(kind, ";")[0]
	uri := (&url.URL{Scheme: "file", Path: filepath.ToSlash(path)}).String()
	var block acp.Content
	switch {
	case strings.HasPrefix(kind, "image/"):
		block = acp.NewImageContent(kind, base64.StdEncoding.EncodeToString(b))
	case strings.HasPrefix(kind, "audio/"):
		block = acp.NewAudioContent(kind, base64.StdEncoding.EncodeToString(b))
	case utf8.Valid(b) && !strings.ContainsRune(string(b), 0):
		if c.Init.AgentCapabilities != nil && c.Init.AgentCapabilities.PromptCapabilities != nil && c.Init.AgentCapabilities.PromptCapabilities.EmbeddedContext != nil && *c.Init.AgentCapabilities.PromptCapabilities.EmbeddedContext {
			block = acp.NewTextResourceContent(uri, string(b))
		} else {
			block = acp.NewTextContent("File: " + path + "\n" + string(b))
		}
	default:
		block = acp.NewBlobResourceContent(uri, base64.StdEncoding.EncodeToString(b))
	}
	if err := c.validateContent([]acp.Content{block}); err != nil {
		return acp.Content{}, err
	}
	return block, nil
}
