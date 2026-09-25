package client_test

import (
	"os"
	"path/filepath"
	"testing"

	acp "github.com/BrokkAi/acp-go"
	"github.com/BrokkAi/micro-acp/internal/store"
)

func TestRichAttachmentsAndUnsupportedMedia(t *testing.T) {
	cwd := t.TempDir()
	c := openTest(t, "rich", cwd, t.TempDir(), store.Store{Directory: t.TempDir()})
	require(t, c.New())
	require(t, os.WriteFile(filepath.Join(cwd, "code.go"), []byte("package main"), 0600))
	block, err := c.Attachment("code.go")
	require(t, err)
	if block.Resource == nil || block.Resource.Resource.TextResourceContents.Text != "package main" {
		t.Fatalf("missing embedded text: %+v", block)
	}
	require(t, os.WriteFile(filepath.Join(cwd, "image.png"), []byte("PNG"), 0600))
	image, err := c.Attachment("image.png")
	require(t, err)
	if image.Image == nil || image.Image.Data != "UE5H" {
		t.Fatalf("image was not base64 encoded: %+v", image)
	}
	native := openTest(t, "native", cwd, t.TempDir(), store.Store{Directory: t.TempDir()})
	require(t, native.New())
	if _, err := native.Attachment("image.png"); err == nil {
		t.Fatal("unadvertised image accepted")
	}
	text, err := native.Attachment("code.go")
	require(t, err)
	if text.Text == nil {
		t.Fatal("text should have a baseline fallback")
	}
	if _, err := c.Attachment(cwd); err == nil {
		t.Fatal("directory accepted as attachment")
	}
}

func TestRichContentRoundTripAndPersistence(t *testing.T) {
	st := store.Store{Directory: t.TempDir()}
	c := openTest(t, "rich", t.TempDir(), t.TempDir(), st)
	require(t, c.New())
	blocks := []acp.Content{acp.NewTextContent("media"), acp.NewImageContent("image/png", "UE5H"), acp.NewAudioContent("audio/wav", "V0FW"), acp.NewTextResourceContent("file:///code.go", "package main"), acp.NewBlobResourceContent("file:///data.bin", "AA=="), acp.NewResourceLinkContent("Guide", "https://example.com/guide")}
	_, err := c.PromptContent(blocks)
	require(t, err)
	s, _ := c.Snapshot()
	stored, err := st.Load(s.ID)
	require(t, err)
	if len(stored.Messages) != 2 || len(stored.Messages[0].Content) != 6 || len(stored.Messages[1].Content) != 5 {
		t.Fatalf("rich content lost on disk: %+v", stored.Messages)
	}
}
