package pipeline

import (
	"regexp"
	"slices"
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

// A run searches synchronising targets first; within each group, specs,
// then test functions, then templates (#45).
func TestHuntOrder(t *testing.T) {
	w := contract.Workspace{Root: t.TempDir()}
	if err := w.Init(); err != nil {
		t.Fatal(err)
	}
	for _, tg := range []*contract.Target{{ID: "plain", Func: "Parse"}, {ID: "test", Func: "TestParse"}, {ID: "conc", Func: "Serve"}} {
		tg.Package, tg.Models = "example.com/p", map[string]string{"gossa": "models/x.json"}
		if err := w.WriteTarget(tg); err != nil {
			t.Fatal(err)
		}
	}
	r := &contract.ModelReport{Package: "example.com/p", Frontend: "gossa", Funcs: []contract.ModelFunc{
		{Name: "Parse"}, {Name: "TestParse"}, {Name: "Serve", Sync: true},
	}}
	if err := w.WriteModelReport(r); err != nil {
		t.Fatal(err)
	}
	props := []*contract.Property{
		{ID: "a", Target: "plain", Kind: contract.KindNoPanic, Frontend: "gossa"},
		{ID: "b", Target: "test", Kind: contract.KindNoPanic, Frontend: "gossa"},
		{ID: "c", Target: "plain", Kind: contract.KindPostcondition, Frontend: "gossa"},
		{ID: "d", Target: "conc", Kind: contract.KindNoPanic, Frontend: "gossa"},
	}
	var got []string
	for _, i := range huntOrder(w, props) {
		got = append(got, props[i].ID)
	}
	if want := []string{"d", "c", "b", "a"}; !slices.Equal(got, want) {
		t.Errorf("huntOrder = %v, want %v", got, want)
	}
}
