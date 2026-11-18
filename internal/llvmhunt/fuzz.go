package llvmhunt

import (
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/fooexec/go2lean/internal/contract"
	llvmfe "github.com/fooexec/go2lean/internal/frontend/llvm"
	"github.com/fooexec/go2lean/internal/sandbox"
)

// libFuzzer on the LLVM line (#270): a function whose model search found
// nothing it could stand on (held without exhausting its domain, or with
// trials stuck in unmodelled code) is compiled into a libFuzzer target
// that decodes its parameters from the fuzzer's bytes and calls it; the
// target runs under ASan and UBSan for a time budget. The fuzzer runs the
// real program, so it reaches what the model cannot (unmodelled libc,
// values the search rarely draws); the model gives what the fuzzer cannot
// (exhaustion of a domain). A crash input is run again and recorded as a
// counterexample with its verdict.

// fuzzHarness is the C source of the libFuzzer target of fn, or why fn's
// parameters cannot be decoded.
func fuzzHarness(src string, fn llvmfe.Func) (string, error) {
	var b strings.Builder
	fmt.Fprintf(&b, "// go2lean libFuzzer target for %s\n#include <stdint.h>\n#include <stdio.h>\n#include <stdlib.h>\n#include <string.h>\n", fn.Name)
	fmt.Fprintf(&b, "#define main go2lean_source_main\n#include %q\n#undef main\n\n", src)
	b.WriteString(`static const uint8_t *g2l_d;
static size_t g2l_n;

/* The next k bytes, little-endian; zero once the input runs out. */
static uint64_t g2l_take(int k) {
  uint64_t v = 0;
  for (int i = 0; i < k; i++) {
    uint64_t c = 0;
    if (g2l_n > 0) { c = *g2l_d++; g2l_n--; }
    v |= c << (8 * i);
  }
  return v;
}

/* A C string of up to 32 bytes. */
static char *g2l_str(void) {
  size_t n = g2l_take(1) % 33;
  char *s = malloc(n + 1);
  for (size_t i = 0; i < n; i++) s[i] = (char)g2l_take(1);
  s[n] = 0;
  return s;
}

int LLVMFuzzerTestOneInput(const uint8_t *data, size_t size) {
  g2l_d = data;
  g2l_n = size;
`)
	var args, frees []string
	for i, g := range fn.Gens {
		v := fmt.Sprintf("a%d", i)
		switch g.K {
		case "int":
			switch {
			case g.W == 1:
				fmt.Fprintf(&b, "  int %s = (int)(g2l_take(1) & 1);\n", v)
			case g.W == 8 || g.W == 16 || g.W == 32 || g.W == 64:
				fmt.Fprintf(&b, "  int%d_t %s = (int%d_t)g2l_take(%d);\n", g.W, v, g.W, g.W/8)
			default:
				return "", fmt.Errorf("parameter %s: a %d-bit integer", g.Name, g.W)
			}
		case "fp":
			// A small integer, as the model draws it.
			fmt.Fprintf(&b, "  double %s = (double)(int16_t)g2l_take(2);\n", v)
		case "buf":
			eb := max(g.EB, 1)
			// malloc of the exact size, so ASan sees a read past the end
			// (one byte for an empty buffer, which is still not NULL).
			fmt.Fprintf(&b, "  size_t %s_n = g2l_take(1) %% %d;\n  uint8_t *%s = malloc(%s_n ? %s_n * %d : 1);\n", v, g.MaxLen+1, v, v, v, eb)
			fmt.Fprintf(&b, "  for (size_t i = 0; i < %s_n * %d; i++) %s[i] = (uint8_t)g2l_take(1);\n", v, eb, v)
			frees = append(frees, v)
		case "zbuf":
			eb := max(g.EB, 1)
			fmt.Fprintf(&b, "  size_t %s_n = g2l_take(1) %% %d;\n  void *%s = calloc(%s_n ? %s_n : 1, %d);\n", v, g.MaxLen+1, v, v, v, eb)
			frees = append(frees, v)
		case "len":
			fmt.Fprintf(&b, "  long long %s = (long long)a%d_n;\n", v, g.Of)
		case "null":
			fmt.Fprintf(&b, "  void *%s = NULL;\n", v)
		case "typed":
			t, _ := g.Ty.(map[string]any)
			if t["k"] == "str" {
				fmt.Fprintf(&b, "  char *%s = g2l_str();\n", v)
				frees = append(frees, v)
				break
			}
			// An out parameter: one object of its scalar type.
			if inner, _ := t["t"].(map[string]any); t["k"] == "ref" && inner != nil {
				n := int(num(inner["b"]))
				if n <= 0 {
					n = 1
				}
				fmt.Fprintf(&b, "  void *%s = calloc(1, %d);\n", v, n)
				if inner["k"] == "int" && (n == 1 || n == 2 || n == 4 || n == 8) {
					fmt.Fprintf(&b, "  *(int%d_t *)%s = (int%d_t)g2l_take(%d);\n", n*8, v, n*8, n)
				}
				frees = append(frees, v)
				break
			}
			return "", fmt.Errorf("parameter %s: a pointer to a struct", g.Name)
		default:
			return "", fmt.Errorf("parameter %s: generator %s", g.Name, g.K)
		}
		args = append(args, v)
	}
	call := fmt.Sprintf("%s(%s)", fn.Name, strings.Join(args, ", "))
	if fn.Spec {
		fmt.Fprintf(&b, "  if (!%s) {\n    fputs(\"%s\\n\", stderr);\n    abort();\n  }\n", call, specViolated)
		// A spec does not take its arguments: free them, so a long run
		// does not grow.
		for _, f := range frees {
			fmt.Fprintf(&b, "  free(%s);\n", f)
		}
	} else {
		// A library function may keep or free what it is given.
		fmt.Fprintf(&b, "  (void)%s;\n", call)
	}
	b.WriteString("  return 0;\n}\n")
	return b.String(), nil
}

var execUnits = regexp.MustCompile(`stat::number_of_executed_units:\s*(\d+)`)

// fuzzResult is what one fuzzing run of a function gave.
type fuzzResult struct {
	Execs   string
	Verdict *contract.Verdict // nil when the fuzzer found nothing
	Skipped string            // why the function was not fuzzed
}

// fuzz builds fn's target and runs it for d; a crash is run again on its
// input and recorded.
func fuzz(ws contract.Workspace, src, key string, fn llvmfe.Func, d time.Duration, seed int) (*fuzzResult, error) {
	h, err := fuzzHarness(src, fn)
	if err != nil {
		return &fuzzResult{Skipped: err.Error()}, nil
	}
	dir := filepath.Join(ws.Root, "fuzz", key, fn.Name)
	if err := os.MkdirAll(filepath.Join(dir, "testdata", "corpus"), 0o755); err != nil {
		return nil, err
	}
	harness := filepath.Join(dir, "fuzz.c")
	if err := os.WriteFile(harness, []byte(h), 0o644); err != nil {
		return nil, err
	}
	bin := filepath.Join(dir, "fuzz")
	build := exec.Command("clang", "-g", "-O1", "-fsanitize=fuzzer,address,undefined", "-fno-sanitize-recover=all", "-w", harness, "-o", bin)
	if out, err := build.CombinedOutput(); err != nil {
		return &fuzzResult{Skipped: "build failed: " + firstLine(string(out))}, nil
	}
	env := []string{"ASAN_OPTIONS=detect_leaks=0", "UBSAN_OPTIONS=print_stacktrace=1"}
	secs := max(int(d.Seconds()), 1)
	lim := sandbox.Default
	lim.MemBytes, lim.Wall, lim.CPUSeconds = 0, d+time.Minute, int64(secs)+120
	_, stderr, _, files, err := sandbox.RunCollect(bin, dir,
		[]string{fmt.Sprintf("-max_total_time=%d", secs), fmt.Sprintf("-seed=%d", seed+1), "-timeout=10", "-print_final_stats=1",
			"-artifact_prefix=testdata/", "testdata/corpus"}, env, lim, "testdata")
	if err != nil {
		return nil, err
	}
	res := &fuzzResult{}
	if m := execUnits.FindStringSubmatch(stderr); m != nil {
		res.Execs = m[1]
	}
	var crashes []string
	for name := range files {
		base := filepath.Base(name)
		if strings.HasPrefix(base, "crash-") || strings.HasPrefix(base, "timeout-") || strings.HasPrefix(base, "oom-") {
			crashes = append(crashes, name)
		}
	}
	if len(crashes) == 0 {
		return res, nil
	}
	sort.Strings(crashes)
	name := crashes[0]
	input := files[name]
	if err := os.WriteFile(filepath.Join(dir, "testdata", filepath.Base(name)), input, 0o644); err != nil {
		return nil, err
	}
	cx := &contract.Counterexample{
		ID:       fmt.Sprintf("cx_llvm_%s_%s_noub_libfuzzer", key, fn.Name),
		Property: fmt.Sprintf("p_llvm_%s_%s_noub", key, fn.Name),
		Frontend: "llvm",
		Engine:   "libfuzzer",
		Inputs:   map[string]string{"bytes": hex.EncodeToString(input)},
	}
	if err := ws.WriteCounterexample(cx); err != nil {
		return nil, err
	}
	// The crash input, run once more on its own: the run is the replay.
	rlim := sandbox.Default
	rlim.MemBytes, rlim.Wall = 0, 30*time.Second
	stdout, rerr, exit, err := sandbox.Run(bin, dir, []string{filepath.Join("testdata", filepath.Base(name))}, env, rlim)
	if err != nil {
		return nil, err
	}
	v := &contract.Verdict{Counterexample: cx.ID, PocDir: dir,
		Evidence: contract.Evidence{Cmd: bin + " " + filepath.Join(dir, "testdata", filepath.Base(name)), Exit: exit, StdoutTail: tail(stdout), StderrTail: tail(rerr)}}
	report := sanitizerReport(rerr)
	if report == "" && fn.Spec && strings.Contains(rerr, specViolated) {
		report = specViolated
	}
	switch {
	case report != "":
		v.Status = contract.VerdictConfirmed
		v.Evidence.PanicMsg = report
	case strings.HasPrefix(filepath.Base(name), "timeout-"):
		v.Status = contract.VerdictInconclusive
		v.Diagnosis = &contract.Diagnosis{Reason: "fuzzer_timeout"}
	case strings.HasPrefix(filepath.Base(name), "oom-"):
		v.Status = contract.VerdictInconclusive
		v.Diagnosis = &contract.Diagnosis{Reason: "fuzzer_out_of_memory"}
	case exit == 0:
		v.Status = contract.VerdictSpurious
		v.Diagnosis = &contract.Diagnosis{Reason: "crash_did_not_reproduce"}
	default:
		v.Status = contract.VerdictInconclusive
		v.Diagnosis = &contract.Diagnosis{Reason: "crash_without_sanitizer_report"}
	}
	res.Verdict = v
	return res, ws.WriteVerdict(v)
}
