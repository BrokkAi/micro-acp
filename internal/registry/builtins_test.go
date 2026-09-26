package registry

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func builtin(t *testing.T, id string) Agent {
	t.Helper()
	for _, a := range WithBuiltins(nil) {
		if a.ID == id {
			return a
		}
	}
	t.Fatalf("missing built-in %s", id)
	return Agent{}
}

func TestBuiltinCatalogAndRegistryPrecedence(t *testing.T) {
	if got := WithBuiltins(nil); len(got) != 3 {
		t.Fatalf("catalog: %+v", got)
	}
	registered := Agent{ID: "anvil", Name: "Published Anvil", Version: "42"}
	got := WithBuiltins([]Agent{registered})
	if len(got) != 3 {
		t.Fatalf("duplicate agent: %+v", got)
	}
	for _, a := range got {
		if a.ID == "anvil" && (a.Version != "42" || a.Name != registered.Name) {
			t.Fatal("built-in replaced the published registry entry")
		}
	}
}

func TestBuiltinNpmPackages(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "npx"), []byte("#!/bin/sh\nexit 0\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	for _, id := range []string{"anvil", "muse-acp"} {
		command, err := (Client{}).Resolve(context.Background(), builtin(t, id), false)
		if err != nil {
			t.Fatal(err)
		}
		if command.Command != "npx" || strings.Join(command.Args, " ") != "--yes @brokkai/"+id+"@latest" {
			t.Fatalf("wrong npm launch: %+v", command)
		}
	}
}

func releaseMetadata(tag, name, url, digest string) []byte {
	data, _ := json.Marshal(map[string]any{"tag_name": tag, "assets": []any{map[string]string{"name": name, "browser_download_url": url, "digest": "sha256:" + digest}}})
	return data
}

func TestBuiltinReleasePlatformLayouts(t *testing.T) {
	for _, test := range []struct{ id, platform, archive, command string }{
		{"draupnir", "linux-x86_64", "brokk-draupnir-v1.2.3-x86_64-unknown-linux-gnu.zip", "draupnir"},
		{"draupnir", "linux-aarch64", "brokk-draupnir-v1.2.3-aarch64-unknown-linux-gnu.zip", "draupnir"},
		{"draupnir", "darwin-x86_64", "brokk-draupnir-v1.2.3-universal-apple-darwin.zip", "draupnir"},
		{"draupnir", "darwin-aarch64", "brokk-draupnir-v1.2.3-universal-apple-darwin.zip", "draupnir"},
	} {
		t.Run(test.id+"/"+test.platform, func(t *testing.T) {
			var release githubRelease
			url := "https://github.com/example/releases/download/v1.2.3/" + test.archive
			if err := json.Unmarshal(releaseMetadata("v1.2.3", test.archive, url, strings.Repeat("ab", 32)), &release); err != nil {
				t.Fatal(err)
			}
			b, err := builtin(t, test.id).release.binary(release, test.platform)
			if err != nil {
				t.Fatal(err)
			}
			directory := strings.TrimSuffix(test.archive, ".zip")
			if b.Archive != url || b.Command != directory+"/"+test.command || len(b.SHA256) != 64 {
				t.Fatalf("wrong release layout: %+v", b)
			}
			release.Assets[0].Digest = ""
			if _, err := builtin(t, test.id).release.binary(release, test.platform); err == nil {
				t.Fatal("accepted an unverified release")
			}
		})
	}
}

type releaseTransport struct {
	base http.RoundTripper
	api  string
}

func (r releaseTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.URL.Host == "api.github.com" {
		clone := req.Clone(req.Context())
		u := *req.URL
		u.Scheme, u.Host = "https", r.api
		clone.URL = &u
		return r.base.RoundTrip(clone)
	}
	return r.base.RoundTrip(req)
}

func TestBuiltinDownloadCacheAndOffline(t *testing.T) {
	const id = "draupnir"
	a := builtin(t, id)
	target := map[string]string{"linux-x86_64": "x86_64-unknown-linux-gnu", "linux-aarch64": "aarch64-unknown-linux-gnu", "darwin-x86_64": "x86_64-apple-darwin", "darwin-aarch64": "aarch64-apple-darwin"}[Platform()]
	if target == "" {
		t.Skip("native release platform")
	}
	if id == "draupnir" && strings.HasPrefix(Platform(), "darwin-") {
		target = "universal-apple-darwin"
	}
	directory := a.release.prefix + "-v1.2.3-" + target
	name := directory + a.release.extension
	var buf bytes.Buffer
	z := zip.NewWriter(&buf)
	w, err := z.Create(directory + "/draupnir")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.WriteString(w, "agent")
	if err := z.Close(); err != nil {
		t.Fatal(err)
	}
	archive := buf.Bytes()
	hash := sha256.Sum256(archive)
	var server *httptest.Server
	server = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/" + a.release.repository + "/releases/latest":
			_, _ = w.Write(releaseMetadata("v1.2.3", name, server.URL+"/"+name, hex.EncodeToString(hash[:])))
		case "/" + name:
			_, _ = w.Write(archive)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	httpClient := server.Client()
	httpClient.Transport = releaseTransport{httpClient.Transport, strings.TrimPrefix(server.URL, "https://")}
	c := Client{Cache: t.TempDir(), HTTP: httpClient}
	if _, err := c.Resolve(context.Background(), a, true); err == nil {
		t.Fatal("offline launch without cached metadata succeeded")
	}
	command, err := c.Resolve(context.Background(), a, false)
	if err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(command.Command); err != nil || string(data) != "agent" {
		t.Fatalf("wrong executable: %q %v", data, err)
	}
	server.Close()
	for _, offline := range []bool{true, false} {
		cached, err := c.Resolve(context.Background(), a, offline)
		if err != nil || cached.Command != command.Command {
			t.Fatalf("cache reuse (offline=%v): %+v %v", offline, cached, err)
		}
	}
	if err := os.Remove(command.Command); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Resolve(context.Background(), a, true); err == nil || !strings.Contains(err.Error(), "not installed") {
		t.Fatalf("offline launch attempted an uncached download: %v", err)
	}
}

func TestBuiltinRejectsUnsupportedPlatformAndUnsafeTag(t *testing.T) {
	source := builtin(t, "draupnir").release
	for _, tag := range []string{"", "../escape", "v1/child", `v1\child`} {
		if _, err := source.binary(githubRelease{Tag: tag}, "linux-x86_64"); err == nil {
			t.Fatalf("accepted tag %q", tag)
		}
	}
	if _, err := source.binary(githubRelease{Tag: "v1"}, "unknown-cpu"); err == nil {
		t.Fatal("accepted unsupported platform")
	}
	if _, err := source.binary(githubRelease{Tag: "v1"}, "linux-x86_64"); err == nil || !strings.Contains(err.Error(), "no asset") {
		t.Fatalf("missing asset: %v", err)
	}
}
