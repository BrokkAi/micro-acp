package client_test

import (
	"os"
	"path/filepath"
	"testing"

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
