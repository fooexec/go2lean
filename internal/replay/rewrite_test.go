package replay

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestRewriteCompiles rewrites examples/conc (every kind of operation the
// replay gates) and builds the package's test binary with the overlay.
func TestRewriteCompiles(t *testing.T) {
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	pkg := "github.com/fooexec/go2lean/examples/conc"
	files, err := Rewrite(root, pkg)
	if err != nil {
		t.Fatal(err)
	}
	src := string(files["timeouts.go"])
	for _, want := range []string{`go2leanGate("recv")`, "go2leanGoCall(", `go2leanGate("select")`, "go2leanPick(", "go2leanWithTimeout(", "go2leanAfter(", "go2leanCondWait(", `go2leanCall0("unlock"`} {
		if !strings.Contains(src, want) {
			t.Errorf("rewritten timeouts.go lacks %s", want)
		}
	}
	dir := t.TempDir()
	overlay := map[string]map[string]string{"Replace": {}}
	for name, b := range files {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, b, 0o644); err != nil {
			t.Fatal(err)
		}
		overlay["Replace"][filepath.Join(root, "examples", "conc", name)] = p
	}
	ov, _ := json.Marshal(overlay)
	ovPath := filepath.Join(dir, "overlay.json")
	os.WriteFile(ovPath, ov, 0o644)
	for _, flags := range [][]string{nil, {"-race"}} {
		args := append(append([]string{"test", "-c"}, flags...), "-overlay", ovPath, "-o", filepath.Join(dir, "conc.test"), pkg)
		cmd := exec.Command("go", args...)
		cmd.Dir = root
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("rewritten package does not build (%v): %v\n%s", flags, err, out)
		}
	}
}
