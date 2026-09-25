package registry

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/BrokkAi/micro-acp/internal/config"
)

func TestRegistryRefreshAndOfflineCache(t *testing.T) {
	var version atomic.Int32
	version.Store(1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if version.Load() == 2 {
			http.Error(w, "unavailable", 503)
			return
		}
		if r.Header.Get("If-None-Match") == "v1" {
			w.WriteHeader(304)
			return
		}
		w.Header().Set("ETag", "v1")
		json.NewEncoder(w).Encode(Index{Version: "1.0.0", Agents: []Agent{{ID: "test", Name: "Test", Version: "1"}}})
	}))
	defer server.Close()
	c := Client{URL: server.URL, Cache: t.TempDir(), HTTP: server.Client()}
	first, err := c.Load(context.Background(), false)
	if err != nil {
		t.Fatal(err)
	}
	second, err := c.Load(context.Background(), false)
	if err != nil || second.Warning != "" || second.FetchedAt.Before(first.FetchedAt) {
		t.Fatalf("revalidation: %+v %v", second, err)
	}
	version.Store(2)
	fallback, err := c.Load(context.Background(), false)
	if err != nil || fallback.Warning == "" {
		t.Fatalf("fallback: %+v %v", fallback, err)
	}
	_, err = c.Load(context.Background(), true)
	if err != nil {
		t.Fatal(err)
	}
	c.URL += "/other"
	if _, err = c.Load(context.Background(), true); err == nil {
		t.Fatal("accepted another registry's cache")
	}
}

func TestInvalidRegistryDoesNotPoisonCache(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(`{"version":"1","agents":[]}`)) }))
	defer server.Close()
	c := Client{URL: server.URL, Cache: t.TempDir(), HTTP: server.Client()}
	if _, err := c.Load(context.Background(), false); err == nil {
		t.Fatal("accepted empty registry")
	}
	if _, err := os.Stat(filepath.Join(c.Cache, "registry.json")); !os.IsNotExist(err) {
		t.Fatal("invalid response was cached")
	}
}

func tarFile(t *testing.T, name string, kind byte) *os.File {
	t.Helper()
	f, err := os.CreateTemp(t.TempDir(), "archive")
	if err != nil {
		t.Fatal(err)
	}
	g := gzip.NewWriter(f)
	w := tar.NewWriter(g)
	body := []byte("agent")
	h := &tar.Header{Name: name, Mode: 0755, Typeflag: kind, Size: int64(len(body))}
	if kind == tar.TypeSymlink {
		h.Linkname = "/tmp/outside"
		h.Size = 0
	}
	if err := w.WriteHeader(h); err != nil {
		t.Fatal(err)
	}
	if kind != tar.TypeSymlink {
		w.Write(body)
	}
	w.Close()
	g.Close()
	f.Seek(0, 0)
	t.Cleanup(func() { f.Close() })
	return f
}
func TestExtractionRejectsTraversalAndSymlinks(t *testing.T) {
	for _, test := range []struct {
		name string
		kind byte
	}{{"../escape", tar.TypeReg}, {"/absolute", tar.TypeReg}, {"agent", tar.TypeSymlink}} {
		t.Run(test.name, func(t *testing.T) {
			f := tarFile(t, test.name, test.kind)
			info, _ := f.Stat()
			if err := extract(f, info.Size(), "agent.tar.gz", t.TempDir(), "agent"); err == nil {
				t.Fatal("unsafe archive accepted")
			}
		})
	}
}
func TestBinaryInstallChecksumAndReuse(t *testing.T) {
	f := tarFile(t, "nested/agent", tar.TypeReg)
	bytes, err := os.ReadFile(f.Name())
	if err != nil {
		t.Fatal(err)
	}
	hash := sha256.Sum256(bytes)
	var requests atomic.Int32
	s := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { requests.Add(1); w.Write(bytes) }))
	defer s.Close()
	c := Client{Cache: t.TempDir(), HTTP: s.Client()}
	binary := Binary{Archive: s.URL + "/agent.tar.gz", Command: "./nested/agent", SHA256: hex.EncodeToString(hash[:])}
	launch, err := c.install(context.Background(), Agent{Name: "test"}, binary)
	if err != nil {
		t.Fatal(err)
	}
	if body, err := os.ReadFile(launch.Command); err != nil || string(body) != "agent" {
		t.Fatalf("executable: %q %v", body, err)
	}
	if _, err = c.install(context.Background(), Agent{Name: "test"}, binary); err != nil {
		t.Fatal(err)
	}
	if requests.Load() != 1 {
		t.Fatal("valid install downloaded twice")
	}
	binary.SHA256 = "incorrect"
	if _, err := c.install(context.Background(), Agent{Name: "test"}, binary); err == nil {
		t.Fatal("checksum mismatch accepted")
	}
}
func TestCacheFilePrivate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cache.json")
	if err := config.AtomicWrite(path, []byte("{}")); err != nil {
		t.Fatal(err)
	}
	info, _ := os.Stat(path)
	if info.Mode().Perm() != 0600 {
		t.Fatalf("permissions %v", info.Mode())
	}
}
