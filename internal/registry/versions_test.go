package registry

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
)

type metadataTransport func(*http.Request) (*http.Response, error)

func (f metadataTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func metadataResponse(body string) *http.Response {
	return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}
}

func TestNPMVersionsPinLaunchRefreshAndOffline(t *testing.T) {
	var version, requests atomic.Int32
	version.Store(3)
	c := Client{Cache: t.TempDir(), HTTP: &http.Client{Transport: metadataTransport(func(r *http.Request) (*http.Response, error) {
		requests.Add(1)
		if r.URL.Host != "registry.npmjs.org" || r.URL.Path != "/@brokkai/anvil/latest" {
			t.Errorf("unexpected version lookup: %s", r.URL)
		}
		return metadataResponse(fmt.Sprintf(`{"name":"@brokkai/anvil","version":"1.2.%d"}`, version.Load())), nil
	})}}
	dir := t.TempDir()
	fakeCommand(t, dir, "npx")
	t.Setenv("PATH", dir)
	a := builtin(t, "anvil")
	a.Distribution.NPX.Args = []string{"--acp"}
	a.Distribution.NPX.Env = map[string]string{"EXAMPLE": "value"}
	resolve := func(offline bool) Agent {
		t.Helper()
		return c.ResolveVersions(context.Background(), []Agent{a}, offline)[0]
	}
	if got := resolve(true); got.VersionError == "" || requests.Load() != 0 {
		t.Fatal("offline lookup without a cache must fail without making a request")
	}
	pinned := resolve(false)
	if pinned.Version != "1.2.3" || pinned.VersionError != "" || pinned.NeedsVersionResolution() {
		t.Fatalf("unresolved version: %+v", pinned)
	}
	if a.Distribution.NPX.Package != "@brokkai/anvil@latest" {
		t.Fatal("resolution modified the source distribution, preventing future refreshes")
	}
	version.Store(4) // A release published after the user saw the picker.
	command, err := c.Resolve(context.Background(), pinned, false)
	if err != nil || strings.Join(command.Args, " ") != "--yes @brokkai/anvil@1.2.3 --acp" || command.Env["EXAMPLE"] != "value" || requests.Load() != 1 {
		t.Fatalf("launch did not preserve the displayed version and arguments: %+v %v", command, err)
	}
	if got := resolve(true); got.Version != "1.2.3" || got.VersionError != "" || requests.Load() != 1 {
		t.Fatalf("offline version: %+v", got)
	}
	if got := resolve(false); got.Version != "1.2.4" || got.Distribution.NPX.Package != "@brokkai/anvil@1.2.4" {
		t.Fatalf("refresh did not adopt the new release: %+v", got)
	}
}

func TestNPMMetadataFailuresPreserveValidCache(t *testing.T) {
	for _, body := range []string{"network error", `{`, `{"name":"other","version":"1.2.3"}`, `{"name":"@brokkai/anvil","version":"latest"}`, `{"name":"@brokkai/anvil","version":"^1.2.3"}`} {
		t.Run(body, func(t *testing.T) {
			var fail atomic.Bool
			c := Client{Cache: t.TempDir(), HTTP: &http.Client{Transport: metadataTransport(func(r *http.Request) (*http.Response, error) {
				if fail.Load() {
					if body == "network error" {
						return nil, errors.New("network unavailable")
					}
					return metadataResponse(body), nil
				}
				return metadataResponse(`{"name":"@brokkai/anvil","version":"2.3.4-beta.1"}`), nil
			})}}
			a := builtin(t, "anvil")
			resolve := func(offline bool) Agent {
				return c.ResolveVersions(context.Background(), []Agent{a}, offline)[0]
			}
			fail.Store(true)
			if got := resolve(false); got.VersionError == "" {
				t.Fatalf("accepted invalid metadata without a cache: %+v", got)
			}
			fail.Store(false)
			if got := resolve(false); got.VersionError != "" {
				t.Fatal(got.VersionError)
			}
			fail.Store(true)
			for _, offline := range []bool{false, true} {
				if got := resolve(offline); got.VersionError != "" || got.Version != "2.3.4-beta.1" {
					t.Fatalf("valid cached version lost (offline=%v): %+v", offline, got)
				}
			}
		})
	}
}

func TestVersionResolutionKeepsOtherAgentsWhenLookupFails(t *testing.T) {
	var requests atomic.Int32
	c := Client{Cache: t.TempDir(), HTTP: &http.Client{Transport: metadataTransport(func(r *http.Request) (*http.Response, error) {
		requests.Add(1)
		if r.URL.Path == "/@brokkai/muse-acp/latest" {
			return nil, errors.New("unavailable")
		}
		return metadataResponse(`{"name":"@brokkai/anvil","version":"3.2.1"}`), nil
	})}}
	registered := Agent{ID: "fixed", Version: "1.0.0"}
	registered.Distribution.NPX = &Package{Package: "fixed@1.0.0"}
	got := c.ResolveVersions(context.Background(), []Agent{builtin(t, "anvil"), builtin(t, "muse-acp"), registered}, false)
	if got[0].Version != "3.2.1" || got[1].VersionError == "" || got[2].Version != "1.0.0" || got[2].VersionError != "" || requests.Load() != 2 {
		t.Fatalf("failed lookup affected other agents: %+v", got)
	}
}

func TestLatestNPMDoesNotLaunchAnOlderFallback(t *testing.T) {
	c := Client{Cache: t.TempDir(), HTTP: &http.Client{Transport: metadataTransport(func(r *http.Request) (*http.Response, error) {
		return metadataResponse(`{"name":"@brokkai/anvil","version":"3.2.1"}`), nil
	})}}
	a := builtin(t, "anvil")
	a.Version = "3.2.0"
	a.Distribution.Binary = map[string]Binary{Platform(): {Archive: "https://example.test/3.2.0.zip", Command: "anvil"}}
	pinned := c.ResolveVersions(context.Background(), []Agent{a}, false)[0]
	t.Setenv("PATH", t.TempDir()) // No npm runtime; a fallback would run the wrong version.
	if _, err := c.Resolve(context.Background(), pinned, false); err == nil || !strings.Contains(err.Error(), "cannot run") {
		t.Fatalf("attempted to launch the old binary after showing %s: %v", pinned.Version, err)
	}
}
