package pipeline

import (
	"regexp"
	"testing"

	"github.com/andreasfoo/go2lean/internal/contract"
)

// TestPropertyFilters checks -specs-only and -run: the templates go, and
// only the properties of targets matching the expression stay.
func TestPropertyFilters(t *testing.T) {
	w := contract.Workspace{Root: t.TempDir()}
	if err := w.Init(); err != nil {
		t.Fatal(err)
	}
	for _, tg := range []*contract.Target{{ID: "t1", Func: "TestFoo"}, {ID: "t2", Func: "TestBar"}, {ID: "t3", Func: "Queue.Push"}} {
		tg.Package, tg.Models = "example.com/p", map[string]string{"gossa": "models/x.json"}
		if err := w.WriteTarget(tg); err != nil {
			t.Fatal(err)
		}
	}
	props := []*contract.Property{
		{ID: "a", Target: "t1", Kind: contract.KindNoPanic},
		{ID: "b", Target: "t2", Kind: contract.KindNoPanic},
		{ID: "c", Target: "t3", Kind: contract.KindPostcondition},
		{ID: "d", Target: "t2", Kind: contract.KindNoHang},
	}
	if got := specsOnly(props); len(got) != 1 || got[0].ID != "c" {
		t.Errorf("specsOnly: %v", got)
	}
	if got := matchingTargets(w, props, regexp.MustCompile(`^TestFoo$`)); len(got) != 1 || got[0].ID != "a" {
		t.Errorf("-run ^TestFoo$: %v", got)
	}
	if got := matchingTargets(w, props, regexp.MustCompile(`Queue\.`)); len(got) != 1 || got[0].ID != "c" {
		t.Errorf("-run Queue.: %v", got)
	}
}
