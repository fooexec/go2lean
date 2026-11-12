package agent

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/fooexec/go2lean/internal/contract"
)

const systemPrompt = `You are the property-proposal agent of go2lean, a tool that finds bugs in Go
functions by translating them into a Lean model, searching the model for
counterexamples, and verifying each counterexample with a real Go test.

Your job: write *spec-in-Go* functions for the modelled functions of a package.
A spec is an executable oracle. go2lean compiles it into the model with the same
pipeline as the function it specifies, searches for inputs where the function's
result makes the spec return false, and then runs the real Go code on those
inputs. A spec that returns false on a real run is a confirmed bug.

Rules for a spec function:
- Signature: func Spec<Target><Suffix>(<exactly the parameters of the target, same
  names and types>, <the target's results>) bool, where a single result is
  named ret and several are named ret0, ret1, ... in order (an error result
  is "ret1 error"; test it only against nil).
- For a method T.M (value or pointer receiver), the target is written T.M in
  the marker, the spec is named SpecTM<Suffix>, and its parameters start with
  the receiver (same type as the method's receiver) before M's parameters.
- The marker line // go2lean:spec <Target> is part of the doc comment of EACH
  spec function: directly above its func line, no blank line in between. Example:

      // go2lean:spec Clamp
      // SpecClampInRange: the result lies within [lo, hi] whenever lo <= hi.
      func SpecClampInRange(v, lo, hi int, ret int) bool {
          if lo > hi {
              return true
          }
          return lo <= ret && ret <= hi
      }
- Return true when the property is vacuously satisfied (the inputs do not meet
  its precondition). Return false only when the result is wrong.
- Several specs per target are fine; give each a distinct, descriptive name.
- Stay inside the modelled subset (docs/semantics.md has the full list):
  integers of any width, bools, strings (len, indexing, slicing, ==, +,
  range over runes, []byte/[]rune conversions, the common strings.*
  functions, strconv.Atoi/Itoa, fmt.Sprintf with %s %d %v %t), structs,
  pointers, arrays, slices (index, len/cap, make, append, copy), maps
  (lookups, updates, delete, len, range), interface values (type
  assertions and type switches, method calls), closures and func values,
  errors compared with nil, encoding/json Marshal and Unmarshal (structs,
  maps, slices, interface{}; the OpenAI and Anthropic SDK types included),
  calls to functions and methods of the package and of the packages it
  imports. Floats are opaque (no float arithmetic or comparisons in a
  spec); goroutines, channels and select are not modelled; time.Now, I/O
  and the network give unknown values.
- A spec may call unexported functions of the package and build its own
  helper values (a converter struct literal, a replayed event sequence).
- A loop is fine; the model runs it with bounded fuel.
- The file contains spec functions only: never redefine or copy a function
  of the package. Import only the packages that the parameter and result
  types of the specs need (for example "time" for time.Duration); call
  nothing from them. Package-level variables of the target package are not
  modelled; use literals. Plain ASCII in code and comments.

Sequence specs: some properties are about a sequence of calls, not one
call (a stream converter that must end up emitting the whole message, an
invariant that must hold after every event, a state machine that must never
reach a bad state). Write them as a sequence spec:
- Marker: // go2lean:sequence <Target> in the doc comment, where Target is
  the function or method the spec drives (for example
  // go2lean:sequence streamState.processEvent).
- Signature: free. Take the events as a slice of a small struct or of
  integers (plus any other input you need), build the state the way the
  package does, call Target once per event, and return bool.
- Check an "always" property after every step (return false as soon as it
  breaks) and an "eventually" property after the last event (for a finite
  stream: when it ends). Derive each event's shape from its fields (a kind
  field selecting start, delta or stop; a length field) so that every
  order and every mix of events is a possible input.
- The search draws sequences of up to 8 events and enumerates every
  sequence of up to 3 events over the constants of the code; a false
  verdict is replayed in Go with the same sequence.

      // go2lean:sequence Converter.Process
      // SeqComplete: every byte received is in the message when the stream ends.
      func SeqComplete(events []Event) bool {
          var c Converter
          for _, e := range events {
              c.Process(e)
          }
          sum := 0
          for _, n := range c.Finish() {
              sum += n
          }
          return sum == c.total
      }

Good properties are ones a real bug could violate: results inside a range,
sign and overflow behaviour on large inputs, off-by-one at boundaries
(empty slice, length 1, index == len), relations to other functions of the
package (inverses, idempotence, monotonicity), and that documented
preconditions really protect the function. For code that converts or
transforms data, the strongest properties are relations:
- conservation: everything the input carries that the output format can
  carry reaches the output (every message, tool call, parameter, flag);
- agreement: two paths that should give the same answer do (streaming and
  non-streaming, a round trip back to the input format, the typed and the
  raw view of a value);
- protocol shape: the output obeys its format's grammar (event order, one
  open block at a time, ids that pair up, required fields present).

Where to spend the specs: on the entry points first (the model report
lists them: exported functions nothing else in the package calls, such as
the converter of a whole request, response or stream). Most bugs are in
how an entry point combines its helpers (a field it never copies, a case
it never routes, a helper it skips), and a property of a small helper
alone cannot see them. Write at least one conservation or agreement spec
for every entry point before writing specs for helpers. For a stream
converter (an entry point fed one event at a time), write at least one
sequence spec.

How to write a spec that finds bugs rather than one that passes:
- State the general property; never encode a particular trigger value.
  The search finds the inputs.
- Make the variation an input. When the input can arrive in several legal
  shapes (a value split into deltas or sent whole, events in different
  orders), derive the shape from a generated field (for example the bits
  of an integer field) instead of replaying one fixed shape.
- Normalise instead of skipping. If a generated field would put an input
  outside the property (a random finish reason, a stray flag), clear or
  fix that field and still check the input; returning true early for most
  inputs leaves the search nothing to check.
- When a constructor calls code the model cannot follow (tokenizers,
  network clients), build the value the constructor would build in its
  fallback path with a struct literal.

Output exactly one fenced Go code block containing the complete contents of a
file named go2lean_specs_test.go, starting with the package clause of the
target package. Outside the block, at most a few short lines of rationale.`

const reportPrompt = `You are the reporting agent of go2lean. You will get the package source, the
properties that were checked and the verdicts the pipeline produced. Write a
concise Markdown report for the package's maintainers, grounded in the source
(point at the actual line that is wrong, not a guess):

1. Confirmed bugs: for each, the property, the concrete input, what the real
   Go run did, and the one-line fix you would suggest.
2. Spurious counterexamples: which properties were wrong or too strong, and how
   you would restate them.
3. Properties that held: what the exhaustive engine certified and over what domain,
   and which ones are still only "no counterexample found". A spec that held
   was also run against mutants of the code under test (a branch negated, a
   comparison or + / - swapped): a spec no mutant fails checks nothing a
   one-line bug would break (its guards skip every input, or it is too weak).
   Say so, and restate such specs so that they constrain the result.

Suggested fixes to the package belong in prose or in a Go block that contains
no "go2lean:spec" or "go2lean:sequence" comment.

If some spec should be restated, you may also output one fenced Go code block
with a revised go2lean_specs_test.go; the pipeline will run it again. It is
recognised by its "go2lean:spec" and "go2lean:sequence" comments. The file must follow the same
rules as before: the package clause of the target package (given below),
imports only for the types in spec signatures, spec functions only, never a
copy of a package function, plain ASCII, inside the modelled subset. Omit
the block if nothing should change.`

// goBlock matches a fenced Go block whose fences each stand on their own
// line (indentation allowed, as inside a list item). Anchoring both fences
// keeps an indented fix snippet from swallowing the spec block after it.
var goBlock = regexp.MustCompile("(?ms)^[ \\t]*```go[ \\t]*$\\n(.*?)\\n[ \\t]*```[ \\t]*$")

// goFence matches the opening fence of a Go block.
var goFence = regexp.MustCompile("(?m)^[ \\t]*```go[ \\t]*$\\n")

// hasMarker says whether Go text carries a spec marker (go2lean:spec or
// go2lean:sequence): such a block is the spec file.
func hasMarker(s string) bool {
	return strings.Contains(s, "go2lean:spec") || strings.Contains(s, "go2lean:sequence")
}

// extractGo returns the spec file in a response: the last fenced Go block
// that carries a go2lean:spec comment, so that fix suggestions in other Go
// blocks are not mistaken for it. Without such a block it falls back to the
// first Go block; "" when there is none. A spec block cut off at the end of
// the response keeps its complete top-level declarations.
func extractGo(resp string) string {
	blocks := goBlock.FindAllStringSubmatch(resp, -1)
	if fences := goFence.FindAllStringIndex(resp, -1); len(fences) > 0 {
		// The text after the last opening fence, when no fence closes it.
		rest := resp[fences[len(fences)-1][1]:]
		if hasMarker(rest) && !strings.Contains("\n"+rest, "\n```") {
			if i := strings.LastIndex(rest, "\n}\n"); i >= 0 {
				blocks = append(blocks, []string{"", rest[:i+2]})
			}
		}
	}
	for i := len(blocks) - 1; i >= 0; i-- {
		if hasMarker(blocks[i][1]) {
			return strings.TrimSpace(blocks[i][1]) + "\n"
		}
	}
	if len(blocks) > 0 {
		return strings.TrimSpace(blocks[0][1]) + "\n"
	}
	return ""
}

// hasSpecBlock reports whether a response carries a spec file (used in the
// report round, where a plain fix snippet must not trigger a rerun).
func hasSpecBlock(resp string) bool {
	for _, b := range goBlock.FindAllStringSubmatch(resp, -1) {
		if hasMarker(b[1]) {
			return true
		}
	}
	return false
}

// packageSource concatenates the package's non-test Go files.
func packageSource(dir string) (string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", err
	}
	var names []string
	for _, e := range entries {
		n := e.Name()
		if strings.HasSuffix(n, ".go") && !strings.HasSuffix(n, "_test.go") {
			names = append(names, n)
		}
	}
	sort.Strings(names)
	var b strings.Builder
	for _, n := range names {
		src, err := os.ReadFile(filepath.Join(dir, n))
		if err != nil {
			return "", err
		}
		fmt.Fprintf(&b, "// ---- %s ----\n%s\n", n, src)
	}
	return b.String(), nil
}

// reportSummary lists the modelled functions with a few of their issues
// each; a rejected function gets its reason in one line. Large packages
// have hundreds of partial functions, so the issues are capped to keep the
// prompt within what a model reads well.
func reportSummary(r *contract.ModelReport) string {
	const maxIssues = 3
	var b strings.Builder
	if eps := entryPoints(r); len(eps) > 0 {
		fmt.Fprintf(&b, "Entry points (exported, called by nothing else in the package): %s\n\n", strings.Join(eps, ", "))
	}
	for _, f := range r.Funcs {
		fmt.Fprintf(&b, "- %s: %s (%s)", f.Name, f.GoSig, f.Status)
		if f.SpecFor != "" {
			fmt.Fprintf(&b, " [spec for %s]", f.SpecFor)
		}
		if f.Status == "rejected" && len(f.Issues) > 0 {
			fmt.Fprintf(&b, ": %s", f.Issues[len(f.Issues)-1])
		}
		b.WriteString("\n")
		if f.Status == "rejected" {
			continue
		}
		for i, is := range f.Issues {
			if i == maxIssues {
				fmt.Fprintf(&b, "    ... %d more\n", len(f.Issues)-maxIssues)
				break
			}
			fmt.Fprintf(&b, "    issue: %s\n", is)
		}
	}
	return b.String()
}

// entryPoints lists the modelled exported functions and methods that no
// other function of the package calls (specs excluded).
func entryPoints(r *contract.ModelReport) []string {
	called := map[int]bool{}
	for _, f := range r.Funcs {
		for _, c := range f.Calls {
			if c != f.ID {
				called[c] = true
			}
		}
	}
	var eps []string
	for _, f := range r.Funcs {
		name := f.Name
		if i := strings.LastIndex(name, "."); i >= 0 {
			name = name[i+1:]
		}
		if f.SpecFor != "" || f.Status == "rejected" || called[f.ID] || name == "" || name[0] < 'A' || name[0] > 'Z' || strings.HasPrefix(name, "Spec") {
			continue
		}
		eps = append(eps, f.Name)
	}
	return eps
}

func proposePrompt(pkgPath, pkgName, source string, r *contract.ModelReport) string {
	return fmt.Sprintf(`Package %s (package clause: "package %s").

Source:
%s
Model report (which functions go2lean could model, and how):
%s
Write go2lean_specs_test.go for this package.`, pkgPath, pkgName, fence(source), reportSummary(r))
}

// guidanceSection appends the user's guidance (bug classes seen in the
// issue tracker, invariants to target) to the proposal prompt.
func guidanceSection(g string) string {
	if strings.TrimSpace(g) == "" {
		return ""
	}
	return "\n\nGuidance from the maintainers (bug classes and invariants to target first):\n" + g
}

func reportUserPrompt(pkgPath, pkgName, source, spec, verdicts string) string {
	return fmt.Sprintf(`Package %s (package clause: "package %s").

Source:
%s
The spec file that was checked (go2lean_specs_test.go; a revision replaces it
whole, so keep the specs that should stay):
%s
%s`, pkgPath, pkgName, fence(source), fence(spec), verdicts)
}

var packageClause = regexp.MustCompile(`(?m)^package\s+\S+`)

// normalizeSpec forces the package clause to the target package; models
// sometimes echo a placeholder name.
func normalizeSpec(spec, pkgName string) string {
	if loc := packageClause.FindStringIndex(spec); loc != nil {
		return spec[:loc[0]] + "package " + pkgName + spec[loc[1]:]
	}
	return "package " + pkgName + "\n\n" + spec
}

func repairPrompt(issues []string, previous string) string {
	var b strings.Builder
	b.WriteString("Your previous go2lean_specs_test.go could not be fully modelled:\n")
	for _, is := range issues {
		fmt.Fprintf(&b, "- %s\n", is)
	}
	fmt.Fprintf(&b, "\nPrevious file:\n%s\nRewrite the file so that every spec stays inside the modelled subset. Drop a spec rather than keep one that cannot be modelled.", fence(previous))
	return b.String()
}

func fence(src string) string { return "```go\n" + src + "```\n" }
