package registry

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
)

type releaseSource struct {
	repository, prefix, executable, extension string
	universalMac                              bool
}

// WithBuiltins supplements the registry with sources for resolving current versions.
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

func (c Client) loadRelease(ctx context.Context, source releaseSource, offline bool) (githubRelease, error) {
	cache := filepath.Join(c.Cache, "releases", strings.ReplaceAll(source.repository, "/", "-")+".json")
	validate := func(data []byte) error {
		var release githubRelease
		if err := json.Unmarshal(data, &release); err != nil {
			return err
		}
		_, err := source.binary(release, Platform())
		return err
	}
	data, err := c.loadMetadata(ctx, "https://api.github.com/repos/"+source.repository+"/releases/latest", cache, offline, validate)
	if err != nil {
		return githubRelease{}, fmt.Errorf("resolve %s release: %w", source.repository, err)
	}
	var release githubRelease
	_ = json.Unmarshal(data, &release) // Validated before caching or returning.
	return release, nil
}
