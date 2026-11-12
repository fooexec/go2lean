package gossa

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/fooexec/go2lean/internal/ir"
)

// A concurrent sequence harness ends its setup with a setupEnd before
// anything reads a parameter (#87): the store of a captured parameter
// moves after it, and a harness whose prefix calls nothing gets none.
func TestSetupCut(t *testing.T) {
	dir := t.TempDir()
	files := map[string]string{
		"go.mod": "module example.com/m\n\ngo 1.22\n",
		"m.go": `package m

import "sync"

type db struct{ n []int }

func newDB(n int) *db { return &db{n: make([]int, n)} }

func (d *db) add(v int) { d.n[0] += v }

// go2lean:sequence db.add
func SeqConcAdd(a int, b int) bool {
	d := newDB(64)
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); d.add(a) }()
	go func() { defer wg.Done(); d.add(b) }()
	wg.Wait()
	return d.n[0] == a+b
}

// go2lean:sequence db.add
func SeqConcNoSetup(a int) bool {
	var d db
	d.n = []int{0}
	done := make(chan bool)
	go func() { d.add(a); done <- true }()
	<-done
	return d.n[0] == a
}
`,
	}
	for name, src := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(src), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	p, _, err := Translate(dir, ".")
	if err != nil {
		t.Fatal(err)
	}
	cuts := map[string]int{}
	for _, f := range p.Funcs {
		for _, b := range f.Blocks {
			if b.Term.Kind == ir.TermSync && b.Term.Sync.Kind == "setupEnd" {
				cuts[f.Name]++
			}
		}
	}
	if cuts["SeqConcAdd"] != 1 {
		t.Errorf("SeqConcAdd: %d setupEnd, want 1", cuts["SeqConcAdd"])
	}
	if cuts["SeqConcNoSetup"] != 0 {
		t.Errorf("SeqConcNoSetup: %d setupEnd, want 0 (its prefix calls nothing)", cuts["SeqConcNoSetup"])
	}
	for name, n := range cuts {
		if name != "SeqConcAdd" && name != "SeqConcNoSetup" && n > 0 {
			t.Errorf("%s: setupEnd outside a sequence harness", name)
		}
	}
}
