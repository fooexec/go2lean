// Package osmodel is the model of the process environment and of
// temporary directories: the frontend type-checks this file into the
// program it translates and makes the program's calls of os.Getenv,
// os.LookupEnv, os.Setenv, os.Unsetenv, os.Clearenv and os.MkdirTemp call
// the functions here. A variable the program set reads back as it was
// set; one it never set reads the real environment, which the model does
// not know (havoc). It is not used by go2lean itself.
package osmodel

import (
	"os"
	"sync"
)

var (
	mu      sync.Mutex
	env     = map[string]string{}
	unset   = map[string]bool{}
	cleared bool
	temps   int // directories MkdirTemp handed out
)

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
	return dir + prefix + n + suffix, nil
}

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
