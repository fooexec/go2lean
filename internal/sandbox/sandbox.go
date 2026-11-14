// Package sandbox runs the code go2lean generates (test binaries that replay
// counterexamples on the user's code) away from the user's files and network,
// under resource limits.
//
// Generated tests are added to a package through `go test -overlay`, so
// nothing is written into the package directory, and built with `-c`. The
// binary then runs in a fresh copy of the package directory (its files and
// testdata). Isolation is bubblewrap (`bwrap`, the unprivileged sandbox
// Flatpak uses): the file system read-only except that copy, a private
// /tmp, no network, its own PID namespace, killed with go2lean. Resource
// limits are `prlimit`'s. Execution fails closed without isolation unless
// the user explicitly selects GO2LEAN_SANDBOX=rlimits or off.
package sandbox

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

// Limits bound one run.
type Limits struct {
	MemBytes   int64 // address space; 0 for none (ASan reserves terabytes)
	CPUSeconds int64
	FileBytes  int64 // largest file the run may write
	Wall       time.Duration
}

// Default is generous for a unit test and stops runaway ones (a random
// input that allocates gigabytes, a loop that never ends).
var Default = Limits{MemBytes: 8 << 30, CPUSeconds: 300, FileBytes: 256 << 20, Wall: 10 * time.Minute}

const (
	// ModeBwrap: bubblewrap isolation and resource limits.
	ModeBwrap = "bwrap"
	// ModeLimits: resource limits only (no usable bwrap here).
	ModeLimits = "rlimits"
	// ModeNone: GO2LEAN_SANDBOX=off, or prlimit is not installed.
	ModeNone = "none"
)

var (
	modeOnce sync.Once
	mode     string
)

// Mode reports the isolation runs get on this machine (detected once).
func Mode() string {
	modeOnce.Do(func() {
		mode = detect()
		if mode != ModeBwrap {
			fmt.Fprintf(os.Stderr, "go2lean: sandbox: %s (bwrap/prlimit missing, disabled, or not usable); replay refuses implicit fallback; weak-mode opt-outs permit host file/network access\n", mode)
		}
	})
	return mode
}

func detect() string {
	if os.Getenv("GO2LEAN_SANDBOX") == "off" {
		return ModeNone
	}
	if _, err := exec.LookPath("prlimit"); err != nil {
		return ModeNone
	}
	if _, err := exec.LookPath("bwrap"); err != nil {
		return ModeLimits
	}
	// bwrap needs user namespaces, which some kernels and containers
	// disable.
	if exec.Command("bwrap", "--ro-bind", "/", "/", "--unshare-all", "--die-with-parent", "true").Run() != nil {
		return ModeLimits
	}
	return ModeBwrap
}

// requireIsolation rejects implicit fallback. Weak modes require explicit
// consent and are never described as isolated execution.
func requireIsolation(mode, consent string) error {
	if mode == ModeBwrap || consent == "off" || (mode == ModeLimits && consent == "rlimits") {
		return nil
	}
	return fmt.Errorf("replay refused: isolation is %s; install working bubblewrap and prlimit (go2lean doctor), or explicitly set GO2LEAN_SANDBOX=rlimits/off for trusted code only", mode)
}

// bwrapArgs isolates a run: everything read-only, a private /tmp, the work
// directory w writable, the binary's directory visible, no network. mod,
// when set, is the module root of the package, kept visible read-only
// under the private home and /tmp: tests find files of the module outside
// their package by their source path (grpc-go's testdata.Path, from
// runtime.Caller), #221.
func bwrapArgs(w, bin, mod string) []string {
	args := []string{"--ro-bind", "/", "/"}
	if home, err := os.UserHomeDir(); err == nil && home != "/" {
		args = append(args, "--tmpfs", home)
	}
	args = append(args, "--dev", "/dev", "--proc", "/proc", "--tmpfs", "/tmp")
	if mod != "" {
		args = append(args, "--ro-bind", mod, mod)
	}
	return append(args, []string{
		"--ro-bind", filepath.Dir(bin), filepath.Dir(bin),
		"--bind", w, w,
		"--chdir", filepath.Join(w, "pkg"),
		"--unshare-all",
		"--die-with-parent",
		"--new-session",
	}...)
}

// BuildTest compiles pkg's tests plus the given files (base name to
// source, placed in pkgDir through an overlay) into the binary out. The
// package directory is not written. stderr holds the compiler's errors.
// vet is off: a package's own test files often fail a newer Go's vet, and
// that is no reason not to run the harness.
func BuildTest(root, pkg, pkgDir string, files map[string][]byte, out string, flags ...string) (stderr string, exit int, err error) {
	tmp, err := os.MkdirTemp("", "go2lean-overlay-")
	if err != nil {
		return "", 0, err
	}
	defer os.RemoveAll(tmp)
	overlay := map[string]map[string]string{"Replace": {}}
	for name, src := range files {
		p := filepath.Join(tmp, name)
		if err := os.WriteFile(p, src, 0o644); err != nil {
			return "", 0, err
		}
		overlay["Replace"][filepath.Join(pkgDir, name)] = p
	}
	ov, _ := json.Marshal(overlay)
	ovPath := filepath.Join(tmp, "overlay.json")
	if err := os.WriteFile(ovPath, ov, 0o644); err != nil {
		return "", 0, err
	}
	args := append([]string{"test", "-c", "-vet=off", "-overlay", ovPath, "-o", out}, flags...)
	cmd := exec.Command("go", append(args, pkg)...)
	cmd.Dir = root
	var eb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &eb, &eb
	err = cmd.Run()
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return eb.String(), ee.ExitCode(), nil
	}
	return eb.String(), 0, err
}

// moduleRoot is the directory holding the go.mod above dir, or "".
func moduleRoot(dir string) string {
	if dir == "" {
		return ""
	}
	d, err := filepath.Abs(dir)
	if err != nil {
		return ""
	}
	for {
		if _, err := os.Stat(filepath.Join(d, "go.mod")); err == nil {
			return d
		}
		up := filepath.Dir(d)
		if up == d {
			return ""
		}
		d = up
	}
}

// Run runs bin with args in a fresh copy of dir (its regular files and its
// testdata directory; an empty dir gives an empty one) under the machine's
// Mode and the limits. env adds to a minimal environment.
func Run(bin, dir string, args, env []string, lim Limits) (stdout, stderr string, exit int, err error) {
	stdout, stderr, exit, _, err = RunCollect(bin, dir, args, env, lim, "")
	return
}

// RunCollect is Run that also returns the files the run left under
// collect, a directory relative to the copy of dir (a fuzzer's
// testdata/fuzz), keyed by their path relative to it.
func RunCollect(bin, dir string, args, env []string, lim Limits, collect string) (stdout, stderr string, exit int, files map[string][]byte, err error) {
	if err := requireIsolation(Mode(), os.Getenv("GO2LEAN_SANDBOX")); err != nil {
		return "", "", 0, nil, err
	}
	w, err := os.MkdirTemp("", "go2lean-sbx-")
	if err != nil {
		return "", "", 0, nil, err
	}
	defer os.RemoveAll(w)
	if err := os.MkdirAll(filepath.Join(w, "pkg"), 0o755); err != nil {
		return "", "", 0, nil, err
	}
	if dir != "" {
		if err := copyPackage(dir, filepath.Join(w, "pkg")); err != nil {
			return "", "", 0, nil, err
		}
	}
	for _, d := range []string{"home", "tmp"} {
		os.MkdirAll(filepath.Join(w, d), 0o755)
	}
	limited := []string{"prlimit"}
	if lim.MemBytes > 0 {
		limited = append(limited, "--as="+strconv.FormatInt(lim.MemBytes, 10))
	}
	limited = append(limited,
		"--cpu="+strconv.FormatInt(lim.CPUSeconds, 10),
		"--fsize="+strconv.FormatInt(lim.FileBytes, 10),
		"--", bin)
	limited = append(limited, args...)
	ctx, cancel := context.WithTimeout(context.Background(), lim.Wall)
	defer cancel()
	var cmd *exec.Cmd
	switch Mode() {
	case ModeBwrap:
		cmd = exec.CommandContext(ctx, "bwrap", append(append(bwrapArgs(w, bin, moduleRoot(dir)), "--"), limited...)...)
	case ModeLimits:
		cmd = exec.CommandContext(ctx, limited[0], limited[1:]...)
		cmd.Dir = filepath.Join(w, "pkg")
	default:
		cmd = exec.CommandContext(ctx, bin, args...)
		cmd.Dir = filepath.Join(w, "pkg")
	}
	cmd.Env = []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + filepath.Join(w, "home"),
		"TMPDIR=" + filepath.Join(w, "tmp"),
		"LANG=C.UTF-8",
	}
	cmd.Env = append(cmd.Env, env...)
	// Kill the whole process group on timeout (a test may fork).
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.WaitDelay = 5 * time.Second
	var ob, eb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &ob, &eb
	err = cmd.Run()
	if collect != "" {
		files = map[string][]byte{}
		base := filepath.Join(w, "pkg", collect)
		filepath.WalkDir(base, func(p string, d fs.DirEntry, e error) error {
			if e == nil && d.Type().IsRegular() {
				if b, e := os.ReadFile(p); e == nil {
					rel, _ := filepath.Rel(base, p)
					files[rel] = b
				}
			}
			return nil
		})
	}
	if ctx.Err() != nil {
		fmt.Fprintf(&eb, "\ngo2lean: sandbox: killed after the wall-clock limit (%s)\n", lim.Wall)
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return ob.String(), eb.String(), ee.ExitCode(), files, nil
	}
	if err != nil && ctx.Err() != nil {
		return ob.String(), eb.String(), -1, files, nil
	}
	return ob.String(), eb.String(), 0, files, err
}

// copyPackage copies dir's regular files and its testdata tree to dst.
func copyPackage(dir, dst string) error {
	if err := os.MkdirAll(dst, 0o755); err != nil {
		return err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	for _, e := range entries {
		src := filepath.Join(dir, e.Name())
		switch {
		case e.Type().IsRegular():
			if err := copyFile(src, filepath.Join(dst, e.Name())); err != nil {
				return err
			}
		case e.IsDir() && (e.Name() == "testdata" || dataDir(src)):
			// testdata, and a directory of data files the package's tests
			// read by a relative path (cascadia's test_resources: its init
			// log.Fatal'd without it, and every replay ended there).
			err := filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
				if err != nil {
					return err
				}
				rel, _ := filepath.Rel(dir, p)
				if d.IsDir() {
					return os.MkdirAll(filepath.Join(dst, rel), 0o755)
				}
				if d.Type().IsRegular() {
					return copyFile(p, filepath.Join(dst, rel))
				}
				return nil
			})
			if err != nil {
				return err
			}
		}
	}
	return nil
}

// maxDataDir bounds the size of a data directory copied for a replay.
const maxDataDir = 64 << 20

// dataDir says whether dir holds data only: no Go file, no go.mod, not a
// hidden directory (.git), and under maxDataDir in all.
func dataDir(dir string) bool {
	if strings.HasPrefix(filepath.Base(dir), ".") {
		return false
	}
	var size int64
	data := true
	filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			data = false
			return filepath.SkipAll
		}
		name := d.Name()
		if strings.HasSuffix(name, ".go") || name == "go.mod" {
			data = false
			return filepath.SkipAll
		}
		if info, err := d.Info(); err == nil && !d.IsDir() {
			if size += info.Size(); size > maxDataDir {
				data = false
				return filepath.SkipAll
			}
		}
		return nil
	})
	return data
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}
