// Package zorya runs Zorya (github.com/Ledger-Donjon/zorya), a concolic
// executor of compiled programs, on a Go binary and replays what it reports.
//
// Zorya makes os.Args[1] symbolic and returns, for every branch that leads to
// a panic block, an input that takes the branch. It does not check the rest
// of the path, and an error return can count as a panic block, so each
// report is a candidate: Replay runs the binary on it and says whether the
// process panicked.
package zorya

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Candidate is one report of Zorya.
type Candidate struct {
	Kind        string // "satisfiable_state" or "bug"
	Instruction string // address of the branch
	Panic       string // address of the panic block
	Bytes       map[int]byte
	HasZeroByte bool // a zero byte cannot be passed in argv
}

// Verdict is the result of running the binary on a candidate.
type Verdict struct {
	Candidate Candidate
	Arg       string
	Replayed  bool   // false: the input cannot be passed in argv
	Panicked  bool   // the process panicked or crashed
	Detail    string // the first line of the panic, or why not replayed
}

type resultsFile struct {
	Binary  string `json:"binary"`
	Results []struct {
		Kind        string `json:"kind"`
		Instruction string `json:"instruction_address"`
		Panic       string `json:"panic_address"`
		Inputs      []struct {
			Name  string `json:"name"`
			Value uint64 `json:"value"`
		} `json:"constrained_inputs"`
	} `json:"results"`
}

// ParseResults reads Zorya's FOUND_SAT_STATE.json.
func ParseResults(b []byte) ([]Candidate, error) {
	var f resultsFile
	if err := json.Unmarshal(b, &f); err != nil {
		return nil, err
	}
	var out []Candidate
	for _, r := range f.Results {
		c := Candidate{Kind: r.Kind, Instruction: r.Instruction, Panic: r.Panic, Bytes: map[int]byte{}}
		for _, in := range r.Inputs {
			// arg1_byte_<i>: byte i of os.Args[1]
			rest, ok := strings.CutPrefix(in.Name, "arg1_byte_")
			if !ok {
				continue
			}
			i, err := strconv.Atoi(rest)
			if err != nil {
				continue
			}
			c.Bytes[i] = byte(in.Value)
			if byte(in.Value) == 0 {
				c.HasZeroByte = true
			}
		}
		out = append(out, c)
	}
	return out, nil
}

// Replay runs binary with each candidate's input as its first argument, over
// the seed (the argument Zorya started from) for the bytes it did not
// constrain.
func Replay(binary, seed string, cands []Candidate) []Verdict {
	var out []Verdict
	seen := map[string]bool{}
	for _, c := range cands {
		v := Verdict{Candidate: c}
		arg := []byte(seed)
		for i, b := range c.Bytes {
			for len(arg) <= i {
				arg = append(arg, 'a')
			}
			arg[i] = b
		}
		v.Arg = string(arg)
		if seen[v.Arg] {
			continue
		}
		seen[v.Arg] = true
		if c.HasZeroByte {
			v.Detail = "the input needs a zero byte, which argv cannot hold"
			out = append(out, v)
			continue
		}
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		cmd := exec.CommandContext(ctx, binary, v.Arg)
		stderr, err := cmd.CombinedOutput()
		cancel()
		v.Replayed = true
		text := string(stderr)
		if err != nil && (strings.Contains(text, "panic:") || strings.Contains(text, "fatal error:") || strings.Contains(text, "SIGSEGV")) {
			v.Panicked = true
			for _, l := range strings.Split(text, "\n") {
				if strings.HasPrefix(l, "panic:") || strings.HasPrefix(l, "fatal error:") || strings.Contains(l, "SIGSEGV") {
					v.Detail = l
					break
				}
			}
		} else if err != nil {
			v.Detail = "exit with an error but no panic: " + err.Error()
		} else {
			v.Detail = "returned normally"
		}
		out = append(out, v)
	}
	return out
}

// Options for Run.
type Options struct {
	Dir     string        // the Zorya checkout (scripts/zorya, results/)
	Ghidra  string        // GHIDRA_INSTALL_DIR
	Seed    string        // the concrete first argument, one byte per symbolic input byte
	Timeout time.Duration // default 10 minutes
}

// Run runs Zorya on binary and replays its reports.
func Run(binary string, o Options) ([]Verdict, error) {
	if o.Dir == "" {
		return nil, fmt.Errorf("zorya: the checkout directory is needed (-dir)")
	}
	script := filepath.Join(o.Dir, "scripts", "zorya")
	if _, err := os.Stat(script); err != nil {
		return nil, fmt.Errorf("zorya: %v", err)
	}
	if o.Seed == "" {
		o.Seed = "aaaaaaaa"
	}
	if o.Timeout == 0 {
		o.Timeout = 10 * time.Minute
	}
	abs, err := filepath.Abs(binary)
	if err != nil {
		return nil, err
	}
	os.RemoveAll(filepath.Join(o.Dir, "results"))
	ctx, cancel := context.WithTimeout(context.Background(), o.Timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "bash", script, abs, "--lang", "go", "--compiler", "gc",
		"--thread-scheduling", "all-threads", "--mode", "main", "--arg", o.Seed,
		"--negate-path-exploration", "--plugin", "none")
	cmd.Dir = o.Dir
	cmd.Env = append(os.Environ(), "ZORYA_DIR="+o.Dir)
	if o.Ghidra != "" {
		cmd.Env = append(cmd.Env, "GHIDRA_INSTALL_DIR="+o.Ghidra)
	}
	out, runErr := cmd.CombinedOutput()
	b, err := os.ReadFile(filepath.Join(o.Dir, "results", "FOUND_SAT_STATE.json"))
	if err != nil {
		tail := string(out)
		if len(tail) > 400 {
			tail = tail[len(tail)-400:]
		}
		return nil, fmt.Errorf("zorya: no report (%v): ...%s", runErr, tail)
	}
	cands, err := ParseResults(b)
	if err != nil {
		return nil, err
	}
	return Replay(abs, o.Seed, cands), nil
}

// Sorted returns the verdicts with the panics first.
func Sorted(vs []Verdict) []Verdict {
	out := append([]Verdict(nil), vs...)
	sort.SliceStable(out, func(i, j int) bool { return out[i].Panicked && !out[j].Panicked })
	return out
}
