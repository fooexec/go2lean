package pipeline

import (
	"regexp"
	"slices"
	"testing"

	"github.com/fooexec/go2lean/internal/contract"
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
	order, _ := huntOrder(w, props, nil)
	for _, i := range order {
		got = append(got, props[i].ID)
	}
	if want := []string{"d", "c", "b", "a"}; !slices.Equal(got, want) {
		t.Errorf("huntOrder = %v, want %v", got, want)
	}
}

// With -changed, the targets that reach a touched function come first,
// before the synchronising ones.
func TestHuntOrderChanged(t *testing.T) {
	w := contract.Workspace{Root: t.TempDir()}
	if err := w.Init(); err != nil {
		t.Fatal(err)
	}
	for _, tg := range []*contract.Target{{ID: "plain", Func: "Parse"}, {ID: "conc", Func: "Serve"}, {ID: "edit", Func: "Edit"}} {
		tg.Package, tg.Models = "example.com/p", map[string]string{"gossa": "models/x.json"}
		if err := w.WriteTarget(tg); err != nil {
			t.Fatal(err)
		}
	}
	r := &contract.ModelReport{Package: "example.com/p", Frontend: "gossa", Funcs: []contract.ModelFunc{
		{ID: 0, Name: "Parse", File: "/r/p.go", Lines: []int{1, 9}},
		{ID: 1, Name: "Serve", Sync: true, File: "/r/p.go", Lines: []int{11, 19}},
		{ID: 2, Name: "Edit", File: "/r/p.go", Lines: []int{21, 29}, Calls: []int{3}},
		{ID: 3, Name: "helper", File: "/r/q.go", Lines: []int{1, 5}},
	}}
	if err := w.WriteModelReport(r); err != nil {
		t.Fatal(err)
	}
	props := []*contract.Property{
		{ID: "a", Target: "plain", Kind: contract.KindNoPanic, Frontend: "gossa"},
		{ID: "b", Target: "conc", Kind: contract.KindNoPanic, Frontend: "gossa"},
		{ID: "c", Target: "edit", Kind: contract.KindNoPanic, Frontend: "gossa"},
	}
	order, n := huntOrder(w, props, map[string][]lineRange{"/r/q.go": {{2, 2}}})
	var got []string
	for _, i := range order {
		got = append(got, props[i].ID)
	}
	if want := []string{"c", "b", "a"}; !slices.Equal(got, want) || n != 1 {
		t.Errorf("huntOrder = %v (%d touched), want %v (1)", got, n, want)
	}
}
