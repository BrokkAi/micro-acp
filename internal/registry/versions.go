package registry

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/BrokkAi/micro-acp/internal/buildinfo"
	"github.com/BrokkAi/micro-acp/internal/config"
)

var npmName = regexp.MustCompile(`^(?:@[a-z0-9._-]+/)?[a-z0-9._-]+$`)
var npmVersion = regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+(?:-[0-9A-Za-z.-]+)?(?:\+[0-9A-Za-z.-]+)?$`)

func latestNPM(a Agent) string {
	if p := a.Distribution.NPX; p != nil {
		name := strings.TrimSuffix(p.Package, "@latest")
		if npmName.MatchString(name) {
			return name
		}
	}
	return ""
}

func (a Agent) NeedsVersionResolution() bool {
	return a.release != nil || latestNPM(a) != "" || a.Version == "latest"
}

// ResolveVersions fetches metadata without installing or running any agents.
// Each returned distribution is pinned to the version shown in the catalog.
func (c Client) ResolveVersions(ctx context.Context, agents []Agent, offline bool) []Agent {
	resolved := append([]Agent(nil), agents...)
	var wg sync.WaitGroup
	limit := make(chan struct{}, 4)
	for i, a := range agents {
		if !a.NeedsVersionResolution() {
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			select {
			case limit <- struct{}{}:
				defer func() { <-limit }()
			case <-ctx.Done():
				resolved[i].VersionError = ctx.Err().Error()
				return
			}
			pinned, err := c.resolveVersion(ctx, a, offline)
			if err != nil {
				resolved[i].VersionError = err.Error()
				return
			}
			resolved[i] = pinned
		}()
	}
	wg.Wait()
	return resolved
}

func (c Client) resolveVersion(ctx context.Context, a Agent, offline bool) (Agent, error) {
	if a.release != nil {
		release, err := c.loadRelease(ctx, *a.release, offline)
		if err != nil {
			return a, err
		}
		binary, err := a.release.binary(release, Platform())
		if err != nil {
			return a, err
		}
		a.Version = release.Tag
		a.Distribution.Binary = map[string]Binary{Platform(): binary}
		a.release = nil
	} else if name := latestNPM(a); name != "" {
		version, err := c.latestNPMVersion(ctx, name, offline)
		if err != nil {
			return a, err
		}
		p := *a.Distribution.NPX
		p.Package = name + "@" + version
		a.Distribution.NPX = &p
		if a.Version != version && a.Version != "v"+version {
			// Fallback distributions still refer to the registry's original version.
			a.Distribution.UVX = nil
			a.Distribution.Binary = nil
		}
		a.Version = version
	} else if a.Version == "latest" {
		return a, errors.New("no version lookup is available for this distribution")
	}
	a.VersionError = ""
	return a, nil
}

func (c Client) latestNPMVersion(ctx context.Context, name string, offline bool) (string, error) {
	type manifest struct {
		Name    string `json:"name"`
		Version string `json:"version"`
	}
	validate := func(data []byte) error {
		var m manifest
		if err := json.Unmarshal(data, &m); err != nil {
			return err
		}
		if m.Name != name || !npmVersion.MatchString(m.Version) {
			return errors.New("npm returned an invalid package name or version")
		}
		return nil
	}
	cache := filepath.Join(c.Cache, "packages", fmt.Sprintf("%x.json", sha256.Sum256([]byte(name))))
	data, err := c.loadMetadata(ctx, "https://registry.npmjs.org/"+url.PathEscape(name)+"/latest", cache, offline, validate)
	if err != nil {
		return "", fmt.Errorf("resolve %s: %w", name, err)
	}
	var m manifest
	_ = json.Unmarshal(data, &m) // Validated before caching or returning.
	return m.Version, nil
}

func (c Client) loadMetadata(ctx context.Context, source, cache string, offline bool, validate func([]byte) error) ([]byte, error) {
	cached, cacheErr := os.ReadFile(cache)
	if cacheErr == nil {
		cacheErr = validate(cached)
	}
	if offline {
		if cacheErr != nil {
			return nil, errors.New("no cached version; refresh once without --offline")
		}
		return cached, nil
	}
	data, err := c.fetchMetadata(ctx, source)
	if err == nil {
		err = validate(data)
	}
	if err != nil {
		if cacheErr == nil {
			return cached, nil
		}
		return nil, err
	}
	if err := config.AtomicWrite(cache, data); err != nil {
		return nil, err
	}
	return data, nil
}

func (c Client) fetchMetadata(ctx context.Context, source string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 12*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, source, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "micro-acp/"+buildinfo.Version)
	res, err := c.httpClient().Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %s", res.Status)
	}
	data, err := io.ReadAll(io.LimitReader(res.Body, (2<<20)+1))
	if err != nil {
		return nil, err
	}
	if len(data) > 2<<20 {
		return nil, errors.New("version metadata exceeds 2 MiB")
	}
	return data, nil
}
