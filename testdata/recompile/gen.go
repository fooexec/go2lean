// Command gen writes the differential check of go2lean's regexp compiler
// (reCompile, for patterns built at run time, #46) against Go: Check.lean
// runs the model's compiler and search on every pattern and string, and
// expected.txt holds Go's verdicts. scripts/recompile-diff.sh runs it.
package main

import (
	"fmt"
	"os"
	"regexp"
	"strings"
)

var pats = []string{
	`a{`, `a{,3}`, `a{2`, `a{2,`, `{`, `a{x}`, `x{2}{3}`, `a**`, `a*?`, `a+?`, `a??`, `a*+`, `a{2}?`, `a{1,2}?`,
	`]`, `a]`, `[]a]`, `[^]a]`, `[a-]`, `[-a]`, `[z-a]`, `[a`, `(`, `)`, `a)`, `()`, `(|a)`, `|`, `a||b`,
	`\`, `\q`, `\.`, `\Qa.b\E`, `(?i)a`, `(?P<n>a)`, `(?:a|b)+`, `\pL`, `[[:alpha:]]+`, `x{1001}`, `x{2,1}`,
	`x{1000}`, `x{0}`, `x{0,0}y`, `^$`, `\b`, `\B`, `\Aa`, `a\z`, `$`, `a$b`, `\d+\s\w`, `[\d\s]+`, `[^\d]+`,
	`\D\W\S`, `\n\t`, `\x41`, `\x{41}`, `\101`, `\1`, `\0`, `\a\f\v\r`, `.*`, `(a*)*`, `(a*)+`, `(a|b)*c`,
	`a{2,}`, `a{,}`, `é+`, `[é-ü]`, `\x{10FFFF}`, `[\]]`, `[\-]+`, `[a\-z]+`, `\-`, `\_`, `\ `, `\é`,
	`(a)(b)?(c)?`, `(a|ab)(c|bcd)(d*)`, `a*?b`, `(.)(.*?)(\.)`, `[^a-c]{2,3}`, `^(a+)+$`, `((a)|b)+`,
	`\w+@\w+\.com`, `(?s).`, `[a-c[]`, `[\w-]+`, `[a-\d]`, `\Z`, `^*`, `*a`, `+`, `?`, `a{1}{2}`, `(*)`,
	`[^\x00-\x{10FFFF}]`, `[]`, `[^]`, `x{01}`, `x{1,02}`, `a{ 1}`, `.`, `\.\*\+\?\(\)\[\]\{\}\|\^\$\\`,
	// Named groups (#173): both spellings, nested, and the names Go rejects.
	`(?<n>a)`, `(?P<v0>[^/]+)/(?P<v1>b*)`, `(?P<x>(?<y>a)b)`, `(?P<>a)`, `(?<>a)`, `(?P<a-b>a)`, `(?P<n a)`,
	`(?P<n>`, `(?P=n)`, `(?Pn>a)`, `(?<_1>x)+`,
}

var strs = []string{"", "a", "aa", "aaa", "ab", "abc", "abcd", "abcbcd", "a{", "a{,3}", "]", "x", "xx", "A", "é", "ü", " 1 b",
	"a\nb", "hello world", "b@x.com", "z", "-", "_", " ", "a.b", "abc.", "{", "a]", "xxy", "\x00", "y", ".*+?()[]{}|^$\\"}

func lean(s string) string {
	var b []string
	for i := 0; i < len(s); i++ {
		b = append(b, fmt.Sprint(s[i]))
	}
	return "[" + strings.Join(b, ",") + "]"
}

func main() {
	dir := os.Args[1]
	lf, _ := os.Create(dir + "/Check.lean")
	ef, _ := os.Create(dir + "/expected.txt")
	fmt.Fprintln(lf, "import Go2Lean.Dialect.GoSSA\nopen Go2Lean.GoSSA\n")
	fmt.Fprint(lf, "def pats : List (List Nat) := [")
	for i, p := range pats {
		if i > 0 {
			fmt.Fprint(lf, ",\n  ")
		}
		fmt.Fprint(lf, lean(p))
	}
	fmt.Fprint(lf, "]\n\ndef strs : List (List Nat) := [")
	for i, s := range strs {
		if i > 0 {
			fmt.Fprint(lf, ", ")
		}
		fmt.Fprint(lf, lean(s))
	}
	fmt.Fprintln(lf, "]")
	for _, p := range pats {
		re, err := regexp.Compile(p)
		if err != nil {
			fmt.Fprintf(ef, "%q 1\n", p)
			continue
		}
		fmt.Fprintf(ef, "%q 0\n", p)
		for _, s := range strs {
			m := re.FindStringSubmatchIndex(s)
			if m == nil {
				fmt.Fprintf(ef, "  nil\n")
			} else {
				fmt.Fprintf(ef, "  %s\n", strings.Trim(fmt.Sprint(m), "[]"))
			}
		}
	}
	// Patterns are quoted by Lean the same way, via the generated names list.
	fmt.Fprint(lf, "def names : List String := [")
	for i, p := range pats {
		if i > 0 {
			fmt.Fprint(lf, ", ")
		}
		fmt.Fprintf(lf, "%q", fmt.Sprintf("%q", p))
	}
	fmt.Fprintln(lf, "]")
	fmt.Fprint(lf, `
def showCaps (c : Array (Option Nat)) : String :=
  " ".intercalate (c.toList.map fun o => match o with | some n => toString n | none => "-1")

def main : IO Unit := do
  for (p, nm) in pats.zip names do
    match reCompile p with
    | .error e => IO.println s!"{nm} {e}"
    | .ok (prog, start, ncap) =>
      IO.println s!"{nm} 0"
      for s in strs do
        match reSearch prog.toArray start ncap s.toArray with
        | none => IO.println "  STUCK"
        | some none => IO.println "  nil"
        | some (some c) => IO.println s!"  {showCaps c}"
`)
	lf.Close()
	ef.Close()
}
