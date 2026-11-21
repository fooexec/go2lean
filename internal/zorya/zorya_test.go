package zorya

import (
	"os/exec"
	"path/filepath"
	"testing"
)

const report = `{"binary":"x","results":[
 {"kind":"satisfiable_state","instruction_address":"0x1","panic_address":"0x2","constrained_inputs":[{"name":"arg1_byte_0","value":75}]},
 {"kind":"satisfiable_state","instruction_address":"0x3","panic_address":"0x4","constrained_inputs":[{"name":"arg1_byte_0","value":75},{"name":"arg1_byte_2","value":90}]},
 {"kind":"satisfiable_state","instruction_address":"0x5","panic_address":"0x6","constrained_inputs":[{"name":"arg1_byte_1","value":0}]}
]}`

func TestReplay(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "crash")
	cmd := exec.Command("go", "build", "-o", bin, ".")
	cmd.Dir = "../../testdata/zoryacrash"
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	cands, err := ParseResults([]byte(report))
	if err != nil || len(cands) != 3 {
		t.Fatalf("parse: %v %d", err, len(cands))
	}
	vs := Replay(bin, "aaaa", cands)
	if len(vs) != 3 {
		t.Fatalf("verdicts: %d", len(vs))
	}
	if vs[0].Panicked || !vs[0].Replayed {
		t.Errorf("first byte only: %+v", vs[0])
	}
	if !vs[1].Panicked {
		t.Errorf("K?Z must panic: %+v", vs[1])
	}
	if vs[2].Replayed || !vs[2].Candidate.HasZeroByte {
		t.Errorf("a zero byte cannot be replayed: %+v", vs[2])
	}
}
