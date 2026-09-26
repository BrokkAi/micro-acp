package registry

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/BrokkAi/micro-acp/internal/config"
)

type releaseSource struct {
	repository, prefix, executable, extension string
	universalMac                              bool
}

// WithBuiltins supplements the live registry without pinning agent versions.
// Published registry entries take precedence if these IDs are registered later.
func WithBuiltins(agents []Agent) []Agent {
	anvil := Agent{ID: "anvil", Name: "Anvil", Version: "latest", Description: "BrokkAi/anvil · portable ACP agent · npm"}
	anvil.Distribution.NPX = &Package{Package: "@brokkai/anvil@latest"}
	muse := Agent{ID: "muse-acp", Name: "Muse ACP", Version: "latest", Description: "BrokkAi/muse-acp · npm · requires Muse Code and a Muse login"}
	muse.Distribution.NPX = &Package{Package: "@brokkai/muse-acp@latest"}
	builtins := []Agent{
		anvil,
		muse,
		{ID: "draupnir", Name: "Draupnir", Version: "latest", Description: "foundev/draupnir · portable ACP agent · native release", release: &releaseSource{repository: "foundev/draupnir", prefix: "brokk-draupnir", executable: "draupnir", extension: ".zip", universalMac: true}},
	}
	result := append([]Agent(nil), agents...)
	seen := map[string]bool{}
	for _, a := range result {
		seen[a.ID] = true
	}
	for _, a := range builtins {
		if !seen[a.ID] {
			result = append(result, a)
		}
	}
	sort.SliceStable(result, func(i, j int) bool { return result[i].Name < result[j].Name })
	return result
}

type githubRelease struct {
	Tag    string `json:"tag_name"`
	Assets []struct {
		Name   string `json:"name"`
		URL    string `json:"browser_download_url"`
		Digest string `json:"digest"`
	} `json:"assets"`
}

func (s releaseSource) binary(release githubRelease, platform string) (Binary, error) {
	targets := map[string]string{
		"linux-x86_64": "x86_64-unknown-linux-gnu", "linux-aarch64": "aarch64-unknown-linux-gnu",
		"darwin-x86_64": "x86_64-apple-darwin", "darwin-aarch64": "aarch64-apple-darwin",
	}
	target, ok := targets[platform]
	if !ok {
		return Binary{}, fmt.Errorf("%s has no supported release for %s", s.repository, platform)
	}
	if s.universalMac && strings.HasPrefix(platform, "darwin-") {
		target = "universal-apple-darwin"
	}
	if release.Tag == "" || strings.ContainsAny(release.Tag, "/\\") || !filepath.IsLocal(release.Tag) {
		return Binary{}, errors.New("release has an invalid version tag")
	}
	directory := s.prefix + "-" + release.Tag + "-" + target
	name := directory + s.extension
	for _, asset := range release.Assets {
		if asset.Name != name {
			continue
		}
		digest, ok := strings.CutPrefix(asset.Digest, "sha256:")
		decoded, err := hex.DecodeString(digest)
		if !ok || err != nil || len(decoded) != 32 {
			return Binary{}, fmt.Errorf("release asset %s has no valid SHA-256 digest", name)
		}
		return Binary{Archive: asset.URL, SHA256: digest, Command: directory + "/" + s.executable}, nil
	}
	return Binary{}, fmt.Errorf("%s release %s has no asset for %s", s.repository, release.Tag, platform)
}

func (c Client) releaseBinary(ctx context.Context, source releaseSource, offline bool) (Binary, error) {
	cachePath := filepath.Join(c.Cache, "releases", strings.ReplaceAll(source.repository, "/", "-")+".json")
	var cached githubRelease
	data, cacheErr := os.ReadFile(cachePath)
	if cacheErr == nil {
		cacheErr = json.Unmarshal(data, &cached)
	}
	cachedBinary, validationErr := source.binary(cached, Platform())
	if cacheErr == nil {
		cacheErr = validationErr
	}
	if offline {
		if cacheErr != nil {
			return Binary{}, fmt.Errorf("%s has no cached release; connect once without --offline", source.repository)
		}
		return cachedBinary, nil
	}
	data, err := c.fetchRelease(ctx, source.repository)
	if err != nil {
		if cacheErr == nil {
			return cachedBinary, nil
		}
		return Binary{}, fmt.Errorf("resolve %s release: %w", source.repository, err)
	}
	var latest githubRelease
	if err := json.Unmarshal(data, &latest); err != nil {
		return Binary{}, fmt.Errorf("decode %s release: %w", source.repository, err)
	}
	binary, err := source.binary(latest, Platform())
	if err != nil {
		return Binary{}, err
	}
	if err := config.AtomicWrite(cachePath, data); err != nil {
		return Binary{}, err
	}
	return binary, nil
}

func (c Client) fetchRelease(ctx context.Context, repository string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 12*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://api.github.com/repos/"+repository+"/releases/latest", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "micro-acp")
	res, err := c.httpClient().Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GitHub HTTP %s", res.Status)
	}
	data, err := io.ReadAll(io.LimitReader(res.Body, (2<<20)+1))
	if err != nil {
		return nil, err
	}
	if len(data) > 2<<20 {
		return nil, errors.New("release metadata exceeds 2 MiB")
	}
	return data, nil
}
