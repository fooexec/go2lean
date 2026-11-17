package llvm

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// A pointer to a struct is read from the debug information as that
// struct, its members at their offsets, a self pointer unrolled to
// typeDepth and a char * as a string (#269).
func TestParamTypes(t *testing.T) {
	if _, err := exec.LookPath("clang"); err != nil {
		t.Skip("no clang")
	}
	dir := t.TempDir()
	src := filepath.Join(dir, "item.c")
	os.WriteFile(src, []byte(`
typedef struct item { struct item *next; int type; char *name; double num; } item;
int count(item *it, int n) { return it ? it->type + n : n; }
int plain(int *p, int n) { return p ? p[0] : n; }
int str(const char *s, const char **end, const unsigned char *buf, unsigned long len) { return s[0] + (end != 0) + buf[0] + len; }
`), 0o644)
	ts, err := ParamTypes(src, dir)
	if err != nil {
		t.Fatal(err)
	}
	if ts["plain"] == nil || ts["plain"][0] != nil || ts["plain"][1] != nil {
		t.Errorf("plain: %v, want [nil nil]", ts["plain"])
	}
	sb, _ := json.Marshal(ts["str"])
	if want := `[{"k":"str"},{"k":"ptr","t":{"k":"str"}},null,null]`; string(sb) != want {
		t.Errorf("str: %s, want %s", sb, want)
	}
	c := ts["count"]
	if len(c) != 2 || c[0] == nil || c[1] != nil {
		t.Fatalf("count: %v, want [typed nil]", c)
	}
	b, _ := json.Marshal(c[0])
	want := `{"k":"ptr","t":{"fields":[{"off":0,"t":{"k":"ptr","t":{"fields":[{"off":0,"t":{"k":"ptr","t":{"fields":[{"off":0,"t":{"k":"null"}},{"off":8,"t":{"b":4,"k":"int"}},{"off":16,"t":{"k":"str"}},{"off":24,"t":{"b":8,"k":"zero"}}],"k":"struct","size":32}}},{"off":8,"t":{"b":4,"k":"int"}},{"off":16,"t":{"k":"str"}},{"off":24,"t":{"b":8,"k":"zero"}}],"k":"struct","size":32}}},{"off":8,"t":{"b":4,"k":"int"}},{"off":16,"t":{"k":"str"}},{"off":24,"t":{"b":8,"k":"zero"}}],"k":"struct","size":32}}`
	if string(b) != want {
		t.Errorf("count's item:\n got %s\nwant %s", b, want)
	}
}
