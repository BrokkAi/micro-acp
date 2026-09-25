package registry

import (
	"archive/tar"
	"archive/zip"
	"compress/bzip2"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/BrokkAi/micro-acp/internal/config"
)

const maxArchive = 512 << 20
const maxExtracted = 1 << 30

func localPath(root, name string) (string, error) {
	name = filepath.FromSlash(name)
	if strings.Contains(name, "\\") || !filepath.IsLocal(name) {
		return "", fmt.Errorf("unsafe archive path %q", name)
	}
	return filepath.Join(root, name), nil
}

func (c Client) install(ctx context.Context, a Agent, b Binary) (config.Command, error) {
	// Hash the full distribution so a same-version republish cannot reuse a stale install.
	manifest, _ := json.Marshal(b)
	sum := sha256.Sum256(manifest)
	dir := filepath.Join(c.Cache, "agents", hex.EncodeToString(sum[:16]))
	command, err := localPath(dir, b.Command)
	if err != nil {
		return config.Command{}, err
	}
	launch := config.Command{Command: command, Args: b.Args, Env: b.Env}
	if info, err := os.Stat(command); err == nil && info.Mode().IsRegular() {
		return launch, nil
	}
	if err := os.MkdirAll(filepath.Dir(dir), 0700); err != nil {
		return launch, err
	}
	stage, err := os.MkdirTemp(filepath.Dir(dir), ".install-*")
	if err != nil {
		return launch, err
	}
	defer os.RemoveAll(stage)
	u, err := url.Parse(b.Archive)
	if err != nil || u.Scheme != "https" {
		return launch, errors.New("binary archive must use HTTPS")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, b.Archive, nil)
	if err != nil {
		return launch, err
	}
	res, err := c.httpClient().Do(req)
	if err != nil {
		return launch, err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return launch, fmt.Errorf("download %s: HTTP %s", a.Name, res.Status)
	}
	f, err := os.CreateTemp(c.Cache, ".archive-*")
	if err != nil {
		return launch, err
	}
	defer os.Remove(f.Name())
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(f, h), io.LimitReader(res.Body, maxArchive+1))
	if err != nil {
		return launch, err
	}
	if n > maxArchive {
		return launch, errors.New("agent archive exceeds 512 MiB")
	}
	if b.SHA256 != "" && !strings.EqualFold(hex.EncodeToString(h.Sum(nil)), b.SHA256) {
		return launch, errors.New("agent archive SHA-256 mismatch")
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return launch, err
	}
	if err := extract(f, n, strings.ToLower(u.Path), stage, b.Command); err != nil {
		return launch, err
	}
	stagedCommand, _ := localPath(stage, b.Command)
	info, err := os.Stat(stagedCommand)
	if err != nil {
		return launch, fmt.Errorf("archive missing executable %s: %w", b.Command, err)
	}
	if !info.Mode().IsRegular() {
		return launch, errors.New("agent command is not a regular file")
	}
	if err := os.Chmod(stagedCommand, 0755); err != nil {
		return launch, err
	}
	if err := os.Rename(stage, dir); err != nil {
		// A concurrent client may have completed the exact same installation.
		if _, check := os.Stat(command); check != nil {
			return launch, err
		}
	}
	return launch, nil
}

func extract(f *os.File, size int64, name, root, command string) error {
	remaining := int64(maxExtracted)
	write := func(name string, mode os.FileMode, r io.Reader) error {
		path, err := localPath(root, name)
		if err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			return err
		}
		out, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600|mode&0111)
		if err != nil {
			return err
		}
		n, err := io.Copy(out, io.LimitReader(r, remaining+1))
		remaining -= n
		if e := out.Close(); err == nil {
			err = e
		}
		if err != nil {
			return err
		}
		if remaining < 0 {
			return errors.New("extracted agent exceeds 1 GiB")
		}
		return nil
	}
	switch {
	case strings.HasSuffix(name, ".zip"):
		z, err := zip.NewReader(f, size)
		if err != nil {
			return err
		}
		for _, entry := range z.File {
			if _, err := localPath(root, entry.Name); err != nil {
				return err
			}
			if entry.FileInfo().IsDir() {
				continue
			}
			if !entry.Mode().IsRegular() {
				return errors.New("archive contains a symlink or special file")
			}
			r, err := entry.Open()
			if err != nil {
				return err
			}
			err = write(entry.Name, entry.Mode(), r)
			r.Close()
			if err != nil {
				return err
			}
		}
		return nil
	case strings.HasSuffix(name, ".tar.gz"), strings.HasSuffix(name, ".tgz"), strings.HasSuffix(name, ".tar.bz2"), strings.HasSuffix(name, ".tbz2"):
		var r io.Reader
		if strings.HasSuffix(name, "bz2") {
			r = bzip2.NewReader(f)
		} else {
			gz, err := gzip.NewReader(f)
			if err != nil {
				return err
			}
			defer gz.Close()
			r = gz
		}
		t := tar.NewReader(r)
		for {
			h, err := t.Next()
			if errors.Is(err, io.EOF) {
				return nil
			}
			if err != nil {
				return err
			}
			if _, err := localPath(root, h.Name); err != nil {
				return err
			}
			switch h.Typeflag {
			case tar.TypeDir:
				continue
			case tar.TypeReg, tar.TypeRegA:
				if err := write(h.Name, os.FileMode(h.Mode), t); err != nil {
					return err
				}
			default:
				return errors.New("archive contains a symlink or special file")
			}
		}
	default:
		for _, ext := range []string{".dmg", ".pkg", ".deb", ".rpm", ".msi", ".appimage"} {
			if strings.HasSuffix(name, ext) {
				return fmt.Errorf("unsupported installer %s", ext)
			}
		}
		return write(command, 0755, f)
	}
}
