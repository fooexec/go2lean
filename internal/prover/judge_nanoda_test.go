package prover

import (
	"strings"
	"testing"
)

// The log of a paranoid run on the packet number decoder's shape (#291): nanoda
// was killed for memory, every other checker accepted.
const nanodaKilled = `Running Lean paranoid kernel on solution
Lean paranoid kernel accepts the solution
Running lean4lean kernel on solution
lean4lean kernel accepts the solution
Running nanoda kernel on solution
nanoda kernel rejected the solution
Running con-leche kernel on solution
con-leche kernel accepts the solution
Running con-ron kernel on solution
con-ron kernel accepts the solution
Running Lean default kernel on solution
error: nanoda exited with 137
Lean default kernel accepts the solution
`

func TestAcceptedWithoutNanoda(t *testing.T) {
	if note, ok := acceptedWithoutNanoda(nanodaKilled); !ok || !strings.Contains(note, "nanoda") {
		t.Fatalf("nanoda killed for memory, all others accepting: %q %v", note, ok)
	}
	bad := map[string]string{
		"another exit code":     strings.Replace(nanodaKilled, "exited with 137", "exited with 1", 1),
		"lean4lean rejects":     strings.Replace(nanodaKilled, "lean4lean kernel accepts the solution", "lean4lean kernel rejected the solution", 1),
		"con-ron missing":       strings.Replace(nanodaKilled, "con-ron kernel accepts the solution\n", "", 1),
		"lean's kernel missing": strings.Replace(nanodaKilled, "Lean paranoid kernel accepts the solution\n", "", 1),
		"another error":         nanodaKilled + "error: type mismatch\n",
		"nanoda did not run":    strings.Replace(nanodaKilled, "Running nanoda kernel on solution\n", "", 1),
		"no error line":         strings.Replace(nanodaKilled, "error: nanoda exited with 137\n", "", 1),
		"two errors":            nanodaKilled + "error: nanoda exited with 137\n",
		"empty":                 "",
	}
	for name, log := range bad {
		if _, ok := acceptedWithoutNanoda(log); ok {
			t.Errorf("%s: accepted", name)
		}
	}
}
