package builtin

import (
	"crypto/sha256"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fooexec/go2lean/internal/contract"
	"github.com/fooexec/go2lean/internal/frontend/gossa"
)

const ordered = `func Compare(a, b string) int {
	if a < b { return -1 }; if a > b { return 1 }; return 0
}`

func fixture(t *testing.T, source string) string {
	t.Helper()
	t.Setenv("GOWORK", "off")
	t.Setenv("GOENV", "off")
	t.Setenv("GOFLAGS", "")
	t.Setenv("GOTOOLCHAIN", "local")
	dir := t.TempDir()
	for name, text := range map[string]string{
		"go.mod":     "module example.com/orderfixture\n\ngo 1.26.0\n",
		"fixture.go": "package fixture\n" + source,
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(text), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestCompareOrderRequiresExplicitCompatibleBinding(t *testing.T) {
	for _, tc := range []struct {
		name, code, selected, want string
	}{
		{"no selection", ordered, "", "explicitly select"},
		{"qualified selection", ordered, "fixture.Compare", "explicitly select"},
		{"keyword", ordered, "func", "explicitly select"},
		{"blank", ordered, "_", "explicitly select"},
		{"missing", ordered, "CompareSomethingElse", "not a package-level function"},
		{"variable", "var Compare = func(a,b string) int { return 0 }", "Compare", "not a package-level function"},
		{"method", "type C struct{}\nfunc(C) Compare(a,b string) int { return 0 }", "Compare", "not a package-level function"},
		{"named input", "type S string\nfunc Compare(a,b S) int { return 0 }", "Compare", "exact signature"},
		{"named output", "type N int\nfunc Compare(a,b string) N { return 0 }", "Compare", "exact signature"},
		{"wide output", "func Compare(a,b string) int64 { return 0 }", "Compare", "exact signature"},
		{"error result", "func Compare(a,b string) (int,error) { return 0,nil }", "Compare", "exact signature"},
		{"variadic", "func Compare(a string,b ...string) int { return 0 }", "Compare", "exact signature"},
		{"generic", "func Compare[T ~string](a,b T) int { return 0 }", "Compare", "exact signature"},
		{"collision", ordered + "\nconst " + CompareOrderOracle + "=0", "Compare", "already exists"},
		{"broken package", ordered + "\nvar x = missing", "Compare", "package load failed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := BindCompareOrder(fixture(t, tc.code), tc.selected, nil, ".")
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err=%v; want %q", err, tc.want)
			}
		})
	}
}

func TestCompareOrderBindingProvenanceAndNoCheckoutWrites(t *testing.T) {
	dir := fixture(t, ordered)
	b, err := BindCompareOrder(dir, "Compare", nil, ".")
	if err != nil {
		t.Fatal(err)
	}
	if b.Package != "example.com/orderfixture" || b.ID != CompareOrderID || b.Version != CompareOrderVersion ||
		b.Stage != "type-compatible-source-only" || b.Law == "" ||
		b.SHA256 != fmt.Sprintf("%x", sha256.Sum256([]byte(b.Source))) || len(b.Assumptions) == 0 {
		t.Fatalf("missing provenance: %+v", b)
	}
	if _, err := os.Stat(b.File); !os.IsNotExist(err) {
		t.Fatalf("binding wrote into checkout: %v", err)
	}
	first := b.Overlay()
	first[b.File][0] = '!'
	if string(b.Overlay()[b.File]) != b.Source {
		t.Fatal("overlay mutated binding source")
	}
	if _, err := BindCompareOrder(dir, "Compare", b.Overlay(), "."); err == nil {
		t.Fatal("existing overlay helper accepted")
	}
	if err := os.WriteFile(b.File, []byte("package fixture\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := BindCompareOrder(dir, "Compare", nil, "."); err == nil {
		t.Fatal("existing disk file accepted")
	}
}

func TestCompareOrderUnexportedSelectionAndAliases(t *testing.T) {
	dir := fixture(t, "type S = string\ntype N = int\nfunc cmp(a,b S) N { return 0 }")
	b, err := BindCompareOrder(dir, "cmp", nil, ".")
	if err != nil {
		t.Fatal(err)
	}
	if b.Function != "cmp" {
		t.Fatal("explicit unexported binding lost")
	}
}

func TestCompareOrderRequiresOnePackage(t *testing.T) {
	dir := fixture(t, ordered)
	if _, err := BindCompareOrder(dir, "Compare", nil); err == nil {
		t.Fatal("no package selection accepted")
	}
	child := filepath.Join(dir, "child")
	if err := os.Mkdir(child, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(child, "child.go"), []byte("package child\n"+ordered), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := BindCompareOrder(dir, "Compare", nil, "./..."); err == nil || !strings.Contains(err.Error(), "expected one package") {
		t.Fatalf("ambiguous package selection accepted: %v", err)
	}
}

func TestCompareOrderGeneratedOverlayTranslates(t *testing.T) {
	dir := fixture(t, ordered)
	b, err := BindCompareOrder(dir, "Compare", nil, ".")
	if err != nil {
		t.Fatal(err)
	}
	progs, _, err := gossa.TranslateAllOpts(dir, gossa.Options{Overlay: b.Overlay()}, ".")
	if err != nil {
		t.Fatal(err)
	}
	target, oracle := false, false
	for _, p := range progs {
		for _, f := range p.Funcs {
			if f.Name == b.Target {
				target = f.Status == contract.ModelComplete
			}
			if f.Name == b.Oracle {
				oracle = f.Status == contract.ModelComplete && f.SpecFor == b.Target
			}
		}
	}
	if !target || !oracle {
		t.Fatalf("generated target/oracle not completely translated: target=%v oracle=%v", target, oracle)
	}
	if _, err := os.Stat(b.File); !os.IsNotExist(err) {
		t.Fatalf("translation wrote generated source into checkout: %v", err)
	}
}

// These are trusted unit fixtures checking generated-oracle semantics in Go.
// They are NOT real-project findings, Lean/Go parity or release acceptance.
func TestCompareOrderGeneratedOracleInGo(t *testing.T) {
	for _, tc := range []struct {
		name, code string
		valid      bool
	}{
		{"lexical", ordered, true},
		{"different magnitudes", `func Compare(a,b string) int {
			if a<b { return -int(^uint(0)>>1)-1 }; if a>b { return int(^uint(0)>>1) }; return 0
		}`, true},
		{"equivalent different strings", `import "strings"
		func Compare(a,b string) int { return strings.Compare(strings.ToLower(a),strings.ToLower(b)) }`, true},
		{"bad reflexivity", `func Compare(a,b string) int { if a<b { return -1 }; return 1 }`, false},
		{"bad sign reversal", `func Compare(a,b string) int { if a==b { return 0 }; return -1 }`, false},
		{"nontransitive equivalence", `func Compare(a,b string) int {
			if a=="a" && b=="c" { return -1 }; if a=="c" && b=="a" { return 1 }; return 0
		}`, false},
		{"cycle", `func Compare(a,b string) int {
			if a==b { return 0 }
			if a=="a" && b=="c" { return 1 }; if a=="c" && b=="a" { return -1 }
			if a<b { return -1 }; return 1
		}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := fixture(t, tc.code)
			b, err := BindCompareOrder(dir, "Compare", nil, ".")
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(b.File, []byte(b.Source), 0o600); err != nil {
				t.Fatal(err)
			}
			test := fmt.Sprintf(`package fixture
import "testing"
func TestLaw(t *testing.T) {
	domain := []string{"", "a", "b", "c", "A", "\xff"}
	failures := 0
	for _,a := range domain { for _,b := range domain { for _,c := range domain {
		aa,ab,ba,bc,ac := %s(a,b,c)
		if !%s(a,b,c,aa,ab,ba,bc,ac) { failures++ }
	}}}
	if (failures==0) != %t { t.Fatalf("unexpected law results: failures=%%d", failures) }
}`, b.Target, b.Oracle, tc.valid)
			if err := os.WriteFile(filepath.Join(dir, "law_test.go"), []byte(test), 0o600); err != nil {
				t.Fatal(err)
			}
			cmd := exec.Command("go", "test", "-count=1", ".")
			cmd.Dir = dir
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("generated Go law: %v\n%s", err, out)
			}
		})
	}
}
