// Package repo locates Go modules and materializes the bundled Lean runtime.
package repo

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	leanassets "github.com/fooexec/go2lean/lean"
)

// Root walks up from the working directory to the directory holding go.mod.
func Root() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", errors.New("go.mod not found above the working directory")
		}
		dir = parent
	}
}

// LeanDir is the source checkout location (not needed by installed binaries).
func LeanDir(root string) string { return filepath.Join(root, "lean") }

// keepCores is how many core snapshots CoreDir keeps.
const keepCores = 4

// CoreDir materializes the bundled Lean sources under $GO2LEAN_CACHE (or
// os.UserCacheDir()/go2lean), keyed by their contents. root is retained for
// caller compatibility; the current directory never selects trusted sources.
func CoreDir(root string) (string, error) {
	files, err := CoreFiles(root)
	if err != nil {
		return "", err
	}
	h := sha256.New()
	for _, f := range files {
		b, err := leanassets.Core.ReadFile(f)
		if err != nil {
			return "", err
		}
		fmt.Fprintf(h, "%s\x00%d\x00", f, len(b))
		h.Write(b)
	}
	return coreSnapshot(files, h.Sum(nil))
}

// CoreFiles lists bundled runtime files in stable order, relative to CoreDir.
func CoreFiles(root string) ([]string, error) {
	var files []string
	err := fs.WalkDir(leanassets.Core, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			files = append(files, path)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Strings(files)
	return files, nil
}

func coreSnapshot(files []string, sum []byte) (string, error) {
	base := os.Getenv("GO2LEAN_CACHE")
	if base == "" {
		c, err := os.UserCacheDir()
		if err != nil {
			return "", err
		}
		base = filepath.Join(c, "go2lean")
	}
	dir := filepath.Join(base, "core-"+hex.EncodeToString(sum)[:16])
	if _, err := os.Stat(filepath.Join(dir, ".complete")); err != nil {
		if err := os.MkdirAll(base, 0o755); err != nil {
			return "", err
		}
		tmp, err := os.MkdirTemp(base, filepath.Base(dir)+".tmp-")
		if err != nil {
			return "", err
		}
		defer os.RemoveAll(tmp)
		for _, f := range files {
			b, err := leanassets.Core.ReadFile(f)
			if err != nil {
				return "", err
			}
			if err := os.MkdirAll(filepath.Dir(filepath.Join(tmp, f)), 0o755); err != nil {
				return "", err
			}
			if err := os.WriteFile(filepath.Join(tmp, f), b, 0o644); err != nil {
				return "", err
			}
		}
		if err := os.WriteFile(filepath.Join(tmp, ".complete"), nil, 0o644); err != nil {
			return "", err
		}
		if err := os.Rename(tmp, dir); err != nil {
			os.RemoveAll(tmp) // another run made it first
			if _, err := os.Stat(filepath.Join(dir, ".complete")); err != nil {
				return "", fmt.Errorf("core snapshot %s: %w", dir, err)
			}
		}
	}
	now := time.Now()
	os.Chtimes(dir, now, now)
	pruneCores(base, dir)
	return dir, nil
}

// pruneCores removes all but the keepCores most recently used snapshots.
func pruneCores(base, keep string) {
	entries, err := os.ReadDir(base)
	if err != nil {
		return
	}
	type snap struct {
		path string
		used time.Time
	}
	var snaps []snap
	for _, e := range entries {
		if !e.IsDir() || !strings.HasPrefix(e.Name(), "core-") || strings.Contains(e.Name(), ".tmp-") {
			continue
		}
		if info, err := e.Info(); err == nil {
			snaps = append(snaps, snap{filepath.Join(base, e.Name()), info.ModTime()})
		}
	}
	sort.Slice(snaps, func(i, j int) bool { return snaps[i].used.After(snaps[j].used) })
	for i, sn := range snaps {
		if i >= keepCores && sn.path != keep {
			os.RemoveAll(sn.path)
		}
	}
}

// WorkspaceLeanDir is a workspace's own lake project: its generated models
// and its search driver (see ssaout.EnsureProject).
func WorkspaceLeanDir(ws string) string { return filepath.Join(ws, "lean") }
