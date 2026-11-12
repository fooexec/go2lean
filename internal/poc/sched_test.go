package poc

import (
	"strings"
	"testing"

	"github.com/fooexec/go2lean/internal/contract"
)

// A false result the model found under a concurrent schedule: the harness
// repeats the call until a run is false, and runs that never take the
// model's interleaving leave the verdict open for the schedule's replay.
func TestScheduleDependentResultIsRepeated(t *testing.T) {
	abi := &contract.ABI{Results: []contract.ABIResult{{GoType: "bool"}}}
	prop := &contract.Property{Kind: contract.KindSequence}
	target := &contract.Target{Func: "SeqConc", Package: "example.com/m"}
	conc := &contract.Counterexample{ID: "cx1", ModelOutput: &contract.ModelOutput{Res: "value", Value: "false"},
		Schedule: []contract.SchedStep{{G: 0, Op: "spawn"}, {G: 1, Op: "close"}, {G: 0, Op: "send"}}}
	src, err := Harness("m", target, prop, conc, abi)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"go2leanI < 200", `fmt.Sprint(ret) == "false"`, "_ = go2leanLeaked"} {
		if !strings.Contains(src, want) {
			t.Fatalf("harness lacks %q:\n%s", want, src)
		}
	}

	v := &contract.Verdict{}
	judge(v, prop, conc, result{Ret: "true", Runs: 200})
	if v.Status != contract.VerdictInconclusive || v.Diagnosis.Reason != "schedule_not_reproduced" {
		t.Fatalf("concurrent run that held: %s %+v", v.Status, v.Diagnosis)
	}
	if !replayable(&prepared{cx: conc, v: v}) {
		t.Fatal("a concurrent false result is not replayed on the model's schedule")
	}
	v = &contract.Verdict{}
	judge(v, prop, conc, result{Ret: "false", Runs: 37})
	if v.Status != contract.VerdictConfirmed {
		t.Fatalf("concurrent run that failed: %s %+v", v.Status, v.Diagnosis)
	}

	// The same result from a sequential run is decided by that run.
	seq := &contract.Counterexample{ID: "cx2", ModelOutput: conc.ModelOutput, Schedule: []contract.SchedStep{{G: 0}}}
	src, err = Harness("m", target, prop, seq, abi)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(src, "go2leanI <") {
		t.Fatalf("sequential counterexample repeated:\n%s", src)
	}
	v = &contract.Verdict{}
	judge(v, prop, seq, result{Ret: "true", Runs: 1})
	if v.Status != contract.VerdictSpurious {
		t.Fatalf("sequential run that held: %s %+v", v.Status, v.Diagnosis)
	}
}

// A schedule step of a package the replay does not rewrite (a dependency's
// lock under -deps) is external: the controller lets it through (#56).
func TestScheduleLitMarksOtherPackagesExternal(t *testing.T) {
	sched := []contract.SchedStep{
		{G: 0, Op: "spawn", Pkg: "example.com/m"},
		{G: 1, Op: "lock", Pkg: "example.com/m/queue"},
		{G: 1, Op: "send"},
	}
	got := scheduleLit(sched, "example.com/m")
	want := `[]go2leanStep{{0, "spawn", []int{}, false}, {1, "lock", []int{}, true}, {1, "send", []int{}, false}}`
	if got != want {
		t.Fatalf("scheduleLit:\n got %s\nwant %s", got, want)
	}
}
