// Package registry consumes the published ACP registry; it never bakes agent versions into the client.
package registry

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"time"

	"github.com/BrokkAi/micro-acp/internal/buildinfo"
	"github.com/BrokkAi/micro-acp/internal/config"
)

type Package struct {
	Package string            `json:"package"`
	Args    []string          `json:"args,omitempty"`
	Env     map[string]string `json:"env,omitempty"`
}
type Binary struct {
	Archive string            `json:"archive"`
	SHA256  string            `json:"sha256,omitempty"`
	Command string            `json:"cmd"`
	Args    []string          `json:"args,omitempty"`
	Env     map[string]string `json:"env,omitempty"`
}
type Agent struct {
	release      *releaseSource
	ID           string `json:"id"`
	Name         string `json:"name"`
	Version      string `json:"version"`
	Description  string `json:"description"`
	Distribution struct {
		NPX    *Package          `json:"npx,omitempty"`
		UVX    *Package          `json:"uvx,omitempty"`
		Binary map[string]Binary `json:"binary,omitempty"`
	} `json:"distribution"`
}
type Index struct {
	Version string  `json:"version"`
	Agents  []Agent `json:"agents"`
}
type Snapshot struct {
	URL       string    `json:"url"`
	FetchedAt time.Time `json:"fetched_at"`
	ETag      string    `json:"etag,omitempty"`
	Index     Index     `json:"index"`
	Warning   string    `json:"-"`
}
type Client struct {
	URL   string
	Cache string
	HTTP  *http.Client
}

func (c Client) httpClient() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return &http.Client{Timeout: 5 * time.Minute}
}
func validate(index Index) error {
	if index.Version == "" || len(index.Agents) == 0 {
		return errors.New("registry has no version or agents")
	}
	seen := map[string]bool{}
	for _, a := range index.Agents {
		if a.ID == "" || a.Name == "" || a.Version == "" || seen[a.ID] {
			return errors.New("registry contains an invalid or duplicate agent")
		}
		seen[a.ID] = true
	}
	return nil
}
func (c Client) Load(ctx context.Context, offline bool) (Snapshot, error) {
	var cached Snapshot
	cachePath := filepath.Join(c.Cache, "registry.json")
	b, cacheErr := os.ReadFile(cachePath)
	if cacheErr == nil {
		cacheErr = json.Unmarshal(b, &cached)
	}
	if cacheErr == nil {
		cacheErr = validate(cached.Index)
	}
	if cached.URL != c.URL {
		cacheErr = errors.New("cache belongs to a different registry")
	}
	fallback := func(err error) (Snapshot, error) {
		if cacheErr != nil {
			return Snapshot{}, fmt.Errorf("registry unavailable and no usable cache: %w", err)
		}
		cached.Warning = fmt.Sprintf("Using registry cached %s: %v", cached.FetchedAt.Format(time.RFC3339), err)
		return cached, nil
	}
	if offline {
		return fallback(errors.New("offline mode"))
	}
	ctx, cancel := context.WithTimeout(ctx, 12*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.URL, nil)
	if err != nil {
		return Snapshot{}, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "micro-acp/"+buildinfo.Version)
	if cacheErr == nil && cached.ETag != "" {
		req.Header.Set("If-None-Match", cached.ETag)
	}
	res, err := c.httpClient().Do(req)
	if err != nil {
		return fallback(err)
	}
	defer res.Body.Close()
	if res.StatusCode == http.StatusNotModified && cacheErr == nil {
		cached.FetchedAt = time.Now().UTC()
		b, _ := json.Marshal(cached)
		if err := config.AtomicWrite(cachePath, b); err != nil {
			return Snapshot{}, err
		}
		return cached, nil
	}
	if res.StatusCode != http.StatusOK {
		return fallback(fmt.Errorf("HTTP %s", res.Status))
	}
	b, err = io.ReadAll(io.LimitReader(res.Body, 8<<20+1))
	if err != nil {
		return fallback(err)
	}
	if len(b) > 8<<20 {
		return fallback(errors.New("registry exceeds 8 MiB"))
	}
	var index Index
	if err := json.Unmarshal(b, &index); err != nil {
		return fallback(err)
	}
	if err := validate(index); err != nil {
		return fallback(err)
	}
	sort.Slice(index.Agents, func(i, j int) bool { return index.Agents[i].Name < index.Agents[j].Name })
	snapshot := Snapshot{URL: c.URL, FetchedAt: time.Now().UTC(), ETag: res.Header.Get("ETag"), Index: index}
	b, _ = json.Marshal(snapshot)
	if err := config.AtomicWrite(cachePath, b); err != nil {
		return Snapshot{}, err
	}
	return snapshot, nil
}
func Platform() string {
	arch := runtime.GOARCH
	switch arch {
	case "amd64":
		arch = "x86_64"
	case "arm64":
		arch = "aarch64"
	}
	return runtime.GOOS + "-" + arch
}
func (a Agent) Kind() string {
	if a.Distribution.NPX != nil {
		return "npx"
	}
	if a.Distribution.UVX != nil {
		return "uvx"
	}
	return "binary"
}
func (c Client) Resolve(ctx context.Context, a Agent, offline bool) (config.Command, error) {
	if p := a.Distribution.NPX; p != nil {
		if _, err := exec.LookPath("npx"); err == nil {
			if p.Package == "" {
				return config.Command{}, errors.New("registry npx package is empty")
			}
			return config.Command{Command: "npx", Args: append([]string{"--yes", p.Package}, p.Args...), Env: p.Env}, nil
		}
	}
	if p := a.Distribution.UVX; p != nil {
		if _, err := exec.LookPath("uvx"); err == nil {
			if p.Package == "" {
				return config.Command{}, errors.New("registry uvx package is empty")
			}
			return config.Command{Command: "uvx", Args: append([]string{p.Package}, p.Args...), Env: p.Env}, nil
		}
	}
	if b, ok := a.Distribution.Binary[Platform()]; ok {
		return c.install(ctx, a, b, offline)
	}
	if a.release != nil {
		b, err := c.releaseBinary(ctx, *a.release, offline)
		if err != nil {
			return config.Command{}, err
		}
		return c.install(ctx, a, b, offline)
	}
	return config.Command{}, fmt.Errorf("%s cannot run on %s: install %s or configure a custom agent", a.Name, Platform(), a.Kind())
}
