package geninst

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDisplay(t *testing.T) {
	for in, want := range map[string]string{
		"Go2LeanGen_Map__int__string": "Map[int, string]",
		"Go2LeanGen_Last__int":        "Last[int]",
		"Plain":                       "Plain",
	} {
		if got := Display(in); got != want {
			t.Errorf("Display(%q) = %q, want %q", in, got, want)
		}
	}
}

// Each exported generic function gets a wrapper at the first candidate
// type arguments its constraints accept; unexported ones, and ones whose
// signature names a type the package cannot spell, get none.
func TestWrappers(t *testing.T) {
	dir := t.TempDir()
	write := func(name, src string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(src), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("go.mod", "module example.com/g\n\ngo 1.22\n")
	write("g.go", `package g

import "cmp"

type Number interface{ ~int | ~float64 }

func Last[T any](xs []T) T { return xs[len(xs)-1] }

func Max[T cmp.Ordered](xs ...T) T { return xs[0] }

func Keys[K comparable, V any](m map[K]V) []K { return nil }

func Div[T Number](a, b T) T { return a / b }

func Join[S ~string](xs []S) S { return "" }

func hidden[T any](x T) T { return x }

func Plain(x int) int { return x }
`)
	overlay, done, err := Wrappers(dir, "./...")
	if err != nil {
		t.Fatal(err)
	}
	if len(done["example.com/g"]) != 5 {
		t.Fatalf("instantiated %v, want 5 functions", done)
	}
	src := string(overlay[filepath.Join(dir, FileName)])
	for _, want := range []string{
		"func Go2LeanGen_Last__int(p0 []int) int",
		"return Last[int](p0)",
		"func Go2LeanGen_Max__int(p0 ...int) int",
		"return Max[int](p0...)",
		"func Go2LeanGen_Keys__int__int(p0 map[int]int) []int",
		"func Go2LeanGen_Div__int(p0 int, p1 int) int",
		"func Go2LeanGen_Join__string(p0 []string) string",
	} {
		if !strings.Contains(src, want) {
			t.Errorf("missing %q in:\n%s", want, src)
		}
	}
	if strings.Contains(src, "hidden") || strings.Contains(src, "Plain") {
		t.Errorf("an unexported or non-generic function got a wrapper:\n%s", src)
	}
}
