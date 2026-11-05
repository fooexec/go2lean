package semantics

import (
	"os"
	"strings"
)

// go2lean's own tests of package osmodel: the environment the program
// writes reads back, and os.MkdirTemp returns fresh paths. The variable
// names are the suite's own, so the real environment does not hold them.

// A variable set reads back, through Getenv and LookupEnv.
func testSetenvGetenv() bool {
	if os.Setenv("GO2LEAN_SEM_A", "v1") != nil {
		return false
	}
	v, ok := os.LookupEnv("GO2LEAN_SEM_A")
	return os.Getenv("GO2LEAN_SEM_A") == "v1" && ok && v == "v1"
}

// Setting it again replaces the value; an empty value is still set.
func testSetenvOverwrite() bool {
	os.Setenv("GO2LEAN_SEM_B", "first")
	os.Setenv("GO2LEAN_SEM_B", "")
	v, ok := os.LookupEnv("GO2LEAN_SEM_B")
	return ok && v == ""
}

// Unsetenv removes it: LookupEnv says it is not set, Getenv gives "".
func testUnsetenv() bool {
	os.Setenv("GO2LEAN_SEM_C", "x")
	if os.Unsetenv("GO2LEAN_SEM_C") != nil {
		return false
	}
	_, ok := os.LookupEnv("GO2LEAN_SEM_C")
	return !ok && os.Getenv("GO2LEAN_SEM_C") == ""
}

// An empty key, one holding '=', or a value holding NUL is rejected, and
// the variable keeps the value it had.
func testSetenvInvalid() bool {
	os.Setenv("GO2LEAN_SEM_D", "keep")
	if os.Setenv("", "x") == nil || os.Setenv("GO2LEAN_SEM=D", "x") == nil ||
		os.Setenv("GO2LEAN_SEM_D", "a\x00b") == nil {
		return false
	}
	return os.Getenv("GO2LEAN_SEM_D") == "keep"
}

// Two directories from one pattern are different, and each name is the
// pattern with its last '*' replaced.
func testMkdirTempFresh() bool {
	d1, err1 := os.MkdirTemp("", "go2lean-sem-*-x")
	d2, err2 := os.MkdirTemp("", "go2lean-sem-*-x")
	if err1 != nil || err2 != nil {
		return false
	}
	defer os.RemoveAll(d1)
	defer os.RemoveAll(d2)
	i := strings.LastIndex(d1, "/")
	base := d1[i+1:]
	return d1 != d2 && strings.HasPrefix(base, "go2lean-sem-") && strings.HasSuffix(base, "-x") &&
		!strings.Contains(base, "*")
}

// A pattern holding a path separator is an error.
func testMkdirTempBadPattern() bool {
	_, err := os.MkdirTemp("", "a/b")
	return err != nil
}
