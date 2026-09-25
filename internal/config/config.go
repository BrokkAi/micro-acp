package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/BrokkAi/acp-go/schema"
)

const RegistryURL = "https://cdn.agentclientprotocol.com/registry/v1/latest/registry.json"

type Command struct {
	Command string            `json:"command"`
	Args    []string          `json:"args,omitempty"`
	Env     map[string]string `json:"env,omitempty"`
}

type Config struct {
	DefaultAgent string             `json:"default_agent,omitempty"`
	RegistryURL  string             `json:"registry_url,omitempty"`
	Agents       map[string]Command `json:"agents,omitempty"`
	Session      SessionOptions     `json:"session,omitempty"`
}

type SessionOptions struct {
	MCPServers            []schema.McpServer `json:"mcp_servers,omitempty"`
	AdditionalDirectories []string           `json:"additional_directories,omitempty"`
}

type Paths struct{ Config, Data, Cache string }

func DefaultPaths() (Paths, error) {
	if base := os.Getenv("MICRO_ACP_HOME"); base != "" {
		base, err := filepath.Abs(base)
		return Paths{filepath.Join(base, "config.json"), filepath.Join(base, "data"), filepath.Join(base, "cache")}, err
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return Paths{}, err
	}
	root := func(key, fallback string) string {
		if v := os.Getenv(key); v != "" {
			return v
		}
		return filepath.Join(home, fallback)
	}
	return Paths{
		filepath.Join(root("XDG_CONFIG_HOME", ".config"), "micro-acp", "config.json"),
		filepath.Join(root("XDG_STATE_HOME", ".local/state"), "micro-acp"),
		filepath.Join(root("XDG_CACHE_HOME", ".cache"), "micro-acp"),
	}, nil
}

func Load(path string, explicit bool) (Config, error) {
	c := Config{RegistryURL: RegistryURL, Agents: map[string]Command{}}
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) && !explicit {
		return c, nil
	}
	if err != nil {
		return c, err
	}
	if err := json.Unmarshal(b, &c); err != nil {
		return c, fmt.Errorf("config %s: %w", path, err)
	}
	if c.RegistryURL == "" {
		c.RegistryURL = RegistryURL
	}
	for name, cmd := range c.Agents {
		if name == "" || cmd.Command == "" {
			return c, fmt.Errorf("custom agents require a name and command")
		}
	}
	return c, nil
}

// AtomicWrite keeps the old file intact on failure and never exposes a partial JSON document.
func AtomicWrite(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".micro-acp-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(data); err == nil {
		err = f.Sync()
	}
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return os.Rename(f.Name(), path)
}
