// Package osmodel is the model of the process environment, of temporary
// directories and of a file system: the frontend type-checks this file
// into the program it translates and makes the program's calls of
// os.Getenv, os.LookupEnv, os.Setenv, os.Unsetenv, os.Clearenv,
// os.MkdirTemp, os.ReadFile, os.WriteFile, os.Remove, os.RemoveAll,
// os.Mkdir, os.MkdirAll, os.Stat, os.Lstat, os.IsNotExist and os.IsExist
// call the functions here. A variable the program set reads back as it was
// set; one it never set reads the real environment, which the model does
// not know (havoc). A file the program wrote reads back; a path it never
// wrote or made is assumed not to exist, as in the replay's sandbox, and
// the run records the assumption. It is not used by go2lean itself.
package osmodel

import (
	"os"
	"sync"
	"time"
)

var (
	mu      sync.Mutex
	env     = map[string]string{}
	unset   = map[string]bool{}
	cleared bool
	temps   int                   // directories MkdirTemp handed out
	files   = map[string][]byte{} // the files the program wrote
	dirs    = map[string]bool{}   // the directories it made
	// known are paths whose whole content the model knows: a directory
	// MkdirTemp made (empty when made), a directory made inside one, and
	// a path the program removed. A path in one that the model does not
	// hold does not exist, with nothing assumed.
	known = map[string]bool{}
)

// absent says whether p is known not to exist (see known); callers hold mu.
func absent(p string) bool {
	for q := p; q != ""; q = parent(q) {
		if known[q] && q != p {
			return true
		}
		if known[q] && q == p && !dirs[p] {
			return true
		}
	}
	return false
}

// go2leanAssume records an assumption on the run that reaches it (the
// frontend lowers the call; it does nothing in Go).
func go2leanAssume(note string) {}

// LookupEnv is os.LookupEnv: the program's own value, else the real
// environment's.
func LookupEnv(key string) (string, bool) {
	mu.Lock()
	v, ok := env[key]
	gone := cleared || unset[key]
	mu.Unlock()
	if ok {
		return v, true
	}
	if gone {
		return "", false
	}
	return os.LookupEnv(key)
}

// Getenv is os.Getenv.
func Getenv(key string) string {
	v, _ := LookupEnv(key)
	return v
}

// Setenv is os.Setenv: an empty key, or one holding '=' or NUL, is
// rejected as in Go.
func Setenv(key, value string) error {
	if key == "" || has(key, '=') || has(key, 0) || has(value, 0) {
		return &os.SyscallError{Syscall: "setenv", Err: errInvalid{}}
	}
	mu.Lock()
	env[key] = value
	delete(unset, key)
	mu.Unlock()
	return nil
}

// Unsetenv is os.Unsetenv.
func Unsetenv(key string) error {
	mu.Lock()
	delete(env, key)
	unset[key] = true
	mu.Unlock()
	return nil
}

// Clearenv is os.Clearenv.
func Clearenv() {
	mu.Lock()
	env = map[string]string{}
	unset = map[string]bool{}
	cleared = true
	mu.Unlock()
}

// MkdirTemp is os.MkdirTemp: a new directory under dir (the temporary
// directory when dir is empty), its name the pattern with its last '*'
// replaced by a number, or the number appended. Each call gets a fresh
// number, so two calls never return the same path, as in Go; the
// directory itself is not modelled, and creating it does not fail.
func MkdirTemp(dir, pattern string) (string, error) {
	if dir == "" {
		dir = "/tmp"
	}
	if has(pattern, '/') {
		return "", &os.PathError{Op: "mkdirtemp", Path: pattern, Err: errPattern{}}
	}
	mu.Lock()
	temps++
	n := decimal(100000000 + temps)
	mu.Unlock()
	prefix, suffix := pattern, ""
	for i := len(pattern) - 1; i >= 0; i-- {
		if pattern[i] == '*' {
			prefix, suffix = pattern[:i], pattern[i+1:]
			break
		}
	}
	if dir[len(dir)-1] != '/' {
		dir += "/"
	}
	p := dir + prefix + n + suffix
	mu.Lock()
	dirs[p], known[p] = true, true
	mu.Unlock()
	return p, nil
}

// clean drops a trailing separator: "a/" and "a" are one path.
func clean(p string) string {
	for len(p) > 1 && p[len(p)-1] == '/' {
		p = p[:len(p)-1]
	}
	return p
}

// parent is the directory holding p ("" for a name without one).
func parent(p string) string {
	for i := len(p) - 1; i > 0; i-- {
		if p[i] == '/' {
			return p[:i]
		}
	}
	return ""
}

// under says whether p is in directory dir.
func under(p, dir string) bool {
	return len(p) > len(dir) && p[:len(dir)] == dir && p[len(dir)] == '/'
}

// ReadFile is os.ReadFile: what the program wrote, a copy.
func ReadFile(name string) ([]byte, error) {
	name = clean(name)
	mu.Lock()
	defer mu.Unlock()
	if b, ok := files[name]; ok {
		out := make([]byte, len(b))
		copy(out, b)
		return out, nil
	}
	if dirs[name] {
		return nil, &os.PathError{Op: "read", Path: name, Err: errIsDir{}}
	}
	if !absent(name) {
		go2leanAssume("os.ReadFile: assumed the file does not exist (the model's file system holds what the program wrote)")
	}
	return nil, &os.PathError{Op: "open", Path: name, Err: errNotExist{}}
}

// WriteFile is os.WriteFile: the file now holds a copy of data.
func WriteFile(name string, data []byte, perm os.FileMode) error {
	name = clean(name)
	mu.Lock()
	defer mu.Unlock()
	if dirs[name] {
		return &os.PathError{Op: "open", Path: name, Err: errIsDir{}}
	}
	b := make([]byte, len(data))
	copy(b, data)
	files[name] = b
	return nil
}

// Remove is os.Remove: a file, or an empty directory.
func Remove(name string) error {
	name = clean(name)
	mu.Lock()
	defer mu.Unlock()
	if _, ok := files[name]; ok {
		delete(files, name)
		known[name] = true
		return nil
	}
	if dirs[name] {
		for p := range files {
			if under(p, name) {
				return &os.PathError{Op: "remove", Path: name, Err: errNotEmpty{}}
			}
		}
		for p := range dirs {
			if under(p, name) {
				return &os.PathError{Op: "remove", Path: name, Err: errNotEmpty{}}
			}
		}
		delete(dirs, name)
		known[name] = true
		return nil
	}
	if !absent(name) {
		go2leanAssume("os.Remove: assumed the file does not exist (the model's file system holds what the program wrote)")
	}
	return &os.PathError{Op: "remove", Path: name, Err: errNotExist{}}
}

// RemoveAll is os.RemoveAll: path and everything in it; a path that does
// not exist is no error.
func RemoveAll(path string) error {
	path = clean(path)
	mu.Lock()
	defer mu.Unlock()
	for p := range files {
		if p == path || under(p, path) {
			delete(files, p)
		}
	}
	for p := range dirs {
		if p == path || under(p, path) {
			delete(dirs, p)
		}
	}
	known[path] = true
	return nil
}

// Mkdir is os.Mkdir: a new directory, an error when the path exists.
func Mkdir(name string, perm os.FileMode) error {
	name = clean(name)
	mu.Lock()
	defer mu.Unlock()
	if _, ok := files[name]; ok || dirs[name] {
		return &os.PathError{Op: "mkdir", Path: name, Err: errExist{}}
	}
	dirs[name] = true
	if absent(name) || known[parent(name)] {
		known[name] = true
	}
	return nil
}

// MkdirAll is os.MkdirAll: the directory and its parents.
func MkdirAll(path string, perm os.FileMode) error {
	path = clean(path)
	mu.Lock()
	defer mu.Unlock()
	if _, ok := files[path]; ok {
		return &os.PathError{Op: "mkdir", Path: path, Err: errNotDir{}}
	}
	fresh := absent(path)
	for p := path; p != "" && p != "/"; p = parent(p) {
		if !dirs[p] && fresh {
			known[p] = true
		}
		dirs[p] = true
	}
	return nil
}

// Stat is os.Stat: the file or directory the program made.
func Stat(name string) (os.FileInfo, error) {
	name = clean(name)
	mu.Lock()
	defer mu.Unlock()
	if b, ok := files[name]; ok {
		return fileInfo{name: base(name), size: int64(len(b))}, nil
	}
	if dirs[name] {
		return fileInfo{name: base(name), dir: true}, nil
	}
	if !absent(name) {
		go2leanAssume("os.Stat: assumed the file does not exist (the model's file system holds what the program wrote)")
	}
	return nil, &os.PathError{Op: "stat", Path: name, Err: errNotExist{}}
}

// Lstat is os.Lstat: Stat, the model has no links.
func Lstat(name string) (os.FileInfo, error) { return Stat(name) }

// IsNotExist is os.IsNotExist on the errors of this file system (a
// *PathError, unwrapped once) and os.ErrNotExist.
func IsNotExist(err error) bool {
	if pe, ok := err.(*os.PathError); ok {
		err = pe.Err
	}
	_, ok := err.(errNotExist)
	return ok || err == os.ErrNotExist
}

// IsExist is os.IsExist, as IsNotExist.
func IsExist(err error) bool {
	if pe, ok := err.(*os.PathError); ok {
		err = pe.Err
	}
	_, ok := err.(errExist)
	return ok || err == os.ErrExist
}

func base(p string) string {
	for i := len(p) - 1; i >= 0; i-- {
		if p[i] == '/' {
			return p[i+1:]
		}
	}
	return p
}

// fileInfo is the os.FileInfo of a file or directory of the model.
type fileInfo struct {
	name string
	size int64
	dir  bool
}

func (f fileInfo) Name() string { return f.name }
func (f fileInfo) Size() int64  { return f.size }
func (f fileInfo) Mode() os.FileMode {
	if f.dir {
		return os.ModeDir | 0o755
	}
	return 0o644
}
func (f fileInfo) ModTime() time.Time { return time.Time{} }
func (f fileInfo) IsDir() bool        { return f.dir }
func (f fileInfo) Sys() any           { return nil }

// errNotExist and errExist stand for fs.ErrNotExist and fs.ErrExist in
// the errors of this file system: the model tests them by type (a
// sentinel reached through os's variables compares as havoc), and their
// Is makes errors.Is(err, fs.ErrNotExist) hold in Go.
type errNotExist struct{}

func (errNotExist) Error() string        { return "file does not exist" }
func (errNotExist) Is(target error) bool { return target == os.ErrNotExist }

type errExist struct{}

func (errExist) Error() string        { return "file already exists" }
func (errExist) Is(target error) bool { return target == os.ErrExist }

type errIsDir struct{}

func (errIsDir) Error() string { return "is a directory" }

type errNotEmpty struct{}

func (errNotEmpty) Error() string { return "directory not empty" }

type errNotDir struct{}

func (errNotDir) Error() string { return "not a directory" }

// has and decimal stand in for strings and strconv, which a program
// calling os need not import: the shim only uses packages os brings.
func has(s string, c byte) bool {
	for i := 0; i < len(s); i++ {
		if s[i] == c {
			return true
		}
	}
	return false
}

func decimal(n int) string {
	var b [20]byte
	i := len(b)
	for {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
		if n == 0 {
			return string(b[i:])
		}
	}
}

type errInvalid struct{}

func (errInvalid) Error() string { return "invalid argument" }

type errPattern struct{}

func (errPattern) Error() string { return "pattern contains path separator" }
