package agent

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/fooexec/go2lean/internal/contract"
	"github.com/fooexec/go2lean/internal/llvmhunt"
)

// The agent on C (#270): it writes specs in C, functions marked
// __attribute__((annotate("go2lean:spec"))) that call the library and
// return non-zero when a property holds, in a spec file that includes the
// library's source; go2lean checks that the file compiles, then searches
// the specs on the LLVM line (llvmhunt) and replays each violation as a C
// program under ASan and UBSan.

// CSpecFile is the spec file the agent writes, next to the source.
const CSpecFile = "go2lean_specs.c"

const cSystemPrompt = `You are the property-proposal agent of go2lean, a tool that checks C code
against specs in a Lean model of its LLVM IR, then replays every violation as a
real C program under AddressSanitizer and UndefinedBehaviorSanitizer.

You write specs in C. A spec is a function

    __attribute__((annotate("go2lean:spec"))) int spec_name(PARAMS)

that calls the library under test and returns non-zero when the property holds
and 0 when it is violated. go2lean searches each spec's parameters for a
violation (a 0, or undefined behaviour inside the library). Rules:
- Parameters are inputs the search chooses: integers (intN_t, size_t, enums),
  NUL-terminated strings (const char *, with no length parameter after it), a
  buffer and its length (const char *buf, size_t len, the length named len,
  size, count or n), and pointers to the library's own structs. Prefer small
  integers and strings, and build the library's objects inside the spec through
  its API (a parser's parse function, a constructor), so the objects are valid.
- The spec may call malloc, calloc, realloc, free, memset, memcpy and the
  string.h functions strlen, strcmp, strncmp, memcmp, strchr, strcpy; other
  libc calls are not modelled and their results are unknown, so avoid them in
  the spec (in particular printf, sprintf, strtod, floats).
- The search draws integers mostly from small values (0 to 16), from the
  constants in the code, and from boundary values; other values are rare. So
  index a table of input pieces with a small integer and keep each table to
  at most 16 entries (use several parameters for several choices), and build
  the input valid by construction (a small grammar: pick a value kind, then
  its parts) rather than concatenating random pieces that rarely form a valid
  input. An input the code rejects early tests nothing.
- The spec's own code must have no undefined behaviour: reduce or check each
  integer parameter before arithmetic on it (w = 1 + (unsigned)w % 4, not
  3 * w), and index only within your buffers. Undefined behaviour in the
  spec's body is reported as the spec's error, not a finding.
- Keep each spec small and deterministic. Free what you allocate. Check the
  library's documented preconditions in the spec (return 1 early when an input
  is outside them) instead of reporting them as violations.
- Good properties: a round trip (print what you parsed and parse it again), a
  documented invariant of a result, the agreement of two functions the
  documentation relates, a security property from the guidance.

Answer with one C code block (` + "```c" + `) holding the whole spec file. Do not
include the library's source: go2lean prepends #include of it, so its static
functions and types are in scope. You may add #include lines for standard
headers.`

const cReportPrompt = `You are the reporting agent of go2lean. You get the library's source, your
spec file, and go2lean's results: for each spec, whether the search found a
violation and whether the replayed C program confirmed it under ASan/UBSan
("confirmed"), did not reproduce it ("spurious"), or could not tell. Write a
short report in Markdown: the confirmed violations first, each with the input,
why it is a real defect of the library (or why the spec was wrong), and a fix;
then what held. Be concrete and do not overstate.`

var cBlock = regexp.MustCompile("(?ms)^[ \\t]*```(?:c|C)[ \\t]*$\\n(.*?)\\n[ \\t]*```[ \\t]*$")

// extractC is the last C code block of a response.
func extractC(resp string) string {
	ms := cBlock.FindAllStringSubmatch(resp, -1)
	if len(ms) == 0 {
		return ""
	}
	return ms[len(ms)-1][1]
}

// clangCheck compiles the spec file for syntax and types only and returns
// clang's diagnostics, or "" when it compiles.
func clangCheck(path string, flags ...string) string {
	out, err := exec.Command("clang", append(append([]string{"-fsyntax-only", "-w"}, flags...), path)...).CombinedOutput()
	if err == nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// RunC drives the agent on one C source file: propose a spec file,
// repair it until clang accepts it, search its specs on the LLVM line,
// then (optionally) ask for a report.
func RunC(ctx context.Context, root string, ws contract.Workspace, src string, llm LLM, opts Options, out io.Writer) error {
	if opts.MaxRepairs == 0 {
		opts.MaxRepairs = 2
	}
	src, err := filepath.Abs(src)
	if err != nil {
		return err
	}
	code, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	// The source must build with the flags given: otherwise every spec
	// fails on the source's own errors, and the agent repairs blind.
	if diag := clangCheck(src, opts.CFlags...); diag != "" && strings.HasSuffix(src, ".c") {
		return fmt.Errorf("agent: %s does not compile with the -cflags given (a define the project's build passes?):\n%s", filepath.Base(src), firstLines(diag, 10))
	}
	// stb_image.h is 278 KB; larger sources are cut.
	const maxSource = 320000
	shown := string(code)
	if len(shown) > maxSource {
		shown = shown[:maxSource] + "\n/* ... truncated ... */\n"
	}
	user := fmt.Sprintf("Library source (%s):\n\n```c\n%s\n```\n", filepath.Base(src), shown)
	if len(opts.Lib) > 0 {
		// A library of several files: the model reads its public header;
		// the implementation is linked, not shown, and not in scope for
		// the specs' includes.
		user = fmt.Sprintf("Public header of the library (%s). Its implementation is linked in; do not\ninclude any .c file, only this header, and call only what it declares.\n\n```c\n%s\n```\n", filepath.Base(src), shown)
	}
	if opts.Guidance != "" {
		user += "\nGuidance: write most specs for this property. A property it states outranks a\ncomment in the source that contradicts it.\n\n" + opts.Guidance + "\n"
	}
	agentDir := filepath.Join(ws.Root, "agent", llvmhunt.Key(src))
	if err := os.MkdirAll(agentDir, 0o755); err != nil {
		return err
	}
	specPath := filepath.Join(filepath.Dir(src), CSpecFile)
	header := fmt.Sprintf("/* Written by go2lean agent: specs for %s. */\n#include %q\n\n", filepath.Base(src), src)
	fmt.Fprintf(out, "== agent (%s) call 1\n", llm.Name())
	resp, err := completeRetrying(ctx, llm, cSystemPrompt, user, out)
	if err != nil {
		return err
	}
	os.WriteFile(filepath.Join(agentDir, "call-1-response.md"), []byte(resp), 0o644)
	spec := extractC(resp)
	var log bytes.Buffer
	var runErr error
	for specRound := 0; ; specRound++ {
		for round := 0; ; round++ {
			if spec == "" {
				return fmt.Errorf("agent: the response has no C code block")
			}
			if err := os.WriteFile(specPath, []byte(header+spec+"\n"), 0o644); err != nil {
				return err
			}
			diag := clangCheck(specPath, opts.CFlags...)
			if diag == "" {
				break
			}
			if round >= opts.MaxRepairs {
				return fmt.Errorf("agent: the spec file does not compile after %d repairs:\n%s", round, firstLines(diag, 20))
			}
			fmt.Fprintf(out, "== spec file does not compile; repair %d\n%s\n", round+1, firstLines(diag, 10))
			repair := user + "\nYour spec file:\n\n```c\n" + spec + "\n```\n\nclang rejected it:\n\n```\n" + firstLines(diag, 40) + "\n```\n\nAnswer with the whole corrected spec file in one C code block."
			resp, err = completeRetrying(ctx, llm, cSystemPrompt, repair, out)
			if err != nil {
				return err
			}
			spec = extractC(resp)
		}
		os.WriteFile(filepath.Join(agentDir, CSpecFile), []byte(header+spec+"\n"), 0o644)
		fmt.Fprintf(out, "== search and replay of the specs (%s)\n", specPath)
		log.Reset()
		so := cSearchOptions(opts)
		runErr = llvmhunt.Run(root, ws, specPath, so, io.MultiWriter(out, &log))
		if runErr != nil {
			fmt.Fprintf(out, "  error: %v\n", runErr)
		}
		// Specs with undefined behaviour in their own body test nothing: the
		// agent gets the reports and one more round.
		bad := specErrors(log.String())
		if len(bad) == 0 || specRound >= 1 || runErr != nil {
			break
		}
		fmt.Fprintf(out, "== %d spec(s) with undefined behaviour of their own; revision %d\n", len(bad), specRound+1)
		revise := user + "\nYour spec file:\n\n```c\n" + spec + "\n```\n\nThe replay found undefined behaviour in the body of these specs themselves, not in the library, so they test nothing yet:\n\n```\n" + strings.Join(bad, "\n") + "\n```\n\nFix those specs (bound each parameter before arithmetic on it) and keep the others. Answer with the whole spec file in one C code block."
		resp, err = completeRetrying(ctx, llm, cSystemPrompt, revise, out)
		if err != nil {
			return err
		}
		os.WriteFile(filepath.Join(agentDir, fmt.Sprintf("revision-%d-response.md", specRound+1)), []byte(resp), 0o644)
		spec = extractC(resp)
	}
	if !opts.ReportRound {
		return runErr
	}
	fmt.Fprintf(out, "== agent (%s) report\n", llm.Name())
	rep := user + "\nYour spec file:\n\n```c\n" + spec + "\n```\n\ngo2lean's results:\n\n```\n" + firstLines(log.String(), 400) + "\n```\n"
	report, err := completeRetrying(ctx, llm, cReportPrompt, rep, out)
	if err != nil {
		return err
	}
	path := filepath.Join(agentDir, "report.md")
	if err := os.WriteFile(path, []byte(report), 0o644); err != nil {
		return err
	}
	fmt.Fprintf(out, "== report written to %s\n", path)
	return runErr
}

// specErrors are the specs the search log reports with undefined
// behaviour in their own body, each with its sanitizer line.
func specErrors(log string) []string {
	var out []string
	var fn, verdict string
	for _, line := range strings.Split(log, "\n") {
		switch {
		case strings.HasPrefix(line, "  ") && !strings.HasPrefix(line, "   ") && !strings.Contains(line, " "+"static functions"):
			fn = strings.TrimSpace(line)
		case strings.HasPrefix(line, "    -> "):
			verdict = strings.TrimPrefix(line, "    -> ")
		case strings.Contains(line, llvmhunt.SpecUB+":"):
			out = append(out, fn+": "+verdict)
		}
	}
	return out
}

func firstLines(s string, n int) string {
	lines := strings.Split(s, "\n")
	if len(lines) > n {
		lines = append(lines[:n], fmt.Sprintf("... (%d more lines)", len(lines)-n))
	}
	return strings.Join(lines, "\n")
}

// cSearchOptions is the LLVM-line search a C spec file gets: the agent's
// -trials, -seed, -engine and -exhaust-len (their defaults are the values
// this line always used), and the C line's own fuel.
func cSearchOptions(opts Options) llvmhunt.Options {
	so := llvmhunt.Options{Trials: 2000, Seed: 42, Fuel: 20000, Engine: "all", ExhaustLen: 3, Init: -1, SpecsOnly: true,
		Fuzz: opts.FuzzTime, Memory: opts.Memory, Lib: opts.Lib, CFlags: opts.CFlags, LDFlags: opts.LDFlags, MaxLen: opts.MaxLen}
	if opts.Search.Trials > 0 {
		so.Trials = opts.Search.Trials
	}
	if opts.Search.Seed != 0 {
		so.Seed = opts.Search.Seed
	}
	if opts.Search.Engine != "" {
		so.Engine = string(opts.Search.Engine)
	}
	if opts.Search.ExhaustLen > 0 {
		so.ExhaustLen = opts.Search.ExhaustLen
	}
	return so
}
