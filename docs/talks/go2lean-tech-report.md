---
marp: true
theme: default
paginate: true
size: 16:9
title: "go2lean: a Lean model, a fuzzer, and real execution"
description: "What go2lean uses Lean for, how much semantics it covers, how it relates to fuzzing, and what it found: security advisories turned into properties."
style: |
  :root {
    --bg: #f8fafc;
    --ink: #0f172a;
    --muted: #64748b;
    --line: #cbd5e1;
    --blue: #2563eb;
    --blue2: #dbeafe;
    --red: #dc2626;
    --red2: #fee2e2;
    --green: #059669;
    --green2: #d1fae5;
    --amber: #d97706;
    --amber2: #fef3c7;
  }

  section {
    font-family: "Inter", "Noto Sans CJK SC", "PingFang SC", "Microsoft YaHei", sans-serif;
    background: var(--bg);
    color: var(--ink);
    padding: 52px 70px 56px 70px;
    display: flex;
    flex-direction: column;
    justify-content: flex-start;
    align-content: flex-start;
  }
  h1 { font-size: 54px; line-height: 1.1; letter-spacing: -0.02em; margin: 0 0 14px; }
  h2 { font-size: 38px; line-height: 1.18; margin: 0 0 24px; letter-spacing: -0.01em; }
  h3 { font-size: 25px; line-height: 1.25; margin: 0 0 10px; }
  p, li { font-size: 24px; line-height: 1.38; }
  ul { margin: 0; padding-left: 26px; }
  li { margin: 6px 0; }
  section::after { font-size: 16px; color: var(--muted); }
  code { font-size: 0.9em; background: #e2e8f0; border-radius: 6px; padding: 1px 7px; }
  strong { color: var(--ink); }

  .kicker { font-size: 20px; letter-spacing: 0.12em; text-transform: uppercase; color: var(--blue); font-weight: 700; margin-bottom: 18px; }
  .subtitle { font-size: 28px; color: var(--muted); line-height: 1.3; margin-bottom: 30px; }
  .pill { display: inline-block; font-size: 19px; border: 1px solid var(--line); background: #fff; border-radius: 999px; padding: 5px 16px; margin-right: 10px; color: var(--muted); }
  section.cover {
    background:
      radial-gradient(circle at 12% 18%, #dbeafe 0, transparent 30%),
      radial-gradient(circle at 82% 8%, #fee2e2 0, transparent 25%),
      linear-gradient(135deg, #f8fafc 0%, #eef2ff 100%);
    justify-content: center;
    align-content: center;
  }
  section.part {
    background: linear-gradient(135deg, #0f172a 0%, #1e293b 100%);
    color: #f8fafc; justify-content: center; align-content: center;
  }
  section.part h1 { color: #f8fafc; font-size: 60px; }
  section.part p { color: #94a3b8; font-size: 28px; }
  section.part .num { font-size: 120px; font-weight: 900; color: #475569; line-height: 0.9; margin-bottom: 8px; }
  section.part::after { color: #64748b; }

  .two { display: grid; grid-template-columns: 1fr 1fr; gap: 26px; align-items: stretch; }
  .four { display: grid; grid-template-columns: repeat(4, 1fr); gap: 18px; align-items: stretch; }
  .three { display: grid; grid-template-columns: repeat(3, 1fr); gap: 22px; align-items: stretch; }
  .card { background: #fff; border: 1px solid var(--line); border-radius: 18px; padding: 22px 24px; box-shadow: 0 10px 24px rgba(15,23,42,.06); }
  .card p, .card li { font-size: 22px; line-height: 1.34; }
  .card h3 { margin-top: 0; }
  .card.ok { border-top: 6px solid var(--green); }
  .card.warn { border-top: 6px solid var(--amber); }
  .card.danger { border-top: 6px solid var(--red); }
  .card.blue { border-top: 6px solid var(--blue); }

  .callout { border-left: 7px solid var(--blue); background: #fff; border-radius: 16px; padding: 20px 26px; box-shadow: 0 10px 24px rgba(15,23,42,.06); }
  .callout p { margin: 0; font-size: 30px; line-height: 1.32; font-weight: 700; }
  .callout.big-claim p { font-size: 36px; line-height: 1.3; }
  .callout.big-claim { padding: 30px 34px; }
  .callout.warn { border-left-color: var(--amber); }
  .callout.danger { border-left-color: var(--red); }
  .callout.ok { border-left-color: var(--green); }

  .big { font-size: 64px; font-weight: 820; letter-spacing: -0.04em; line-height: 1; color: var(--blue); }
  .big.green { color: var(--green); } .big.red { color: var(--red); } .big.amber { color: var(--amber); }
  .lbl { font-size: 21px; color: var(--muted); margin-top: 8px; line-height: 1.3; }

  .chain { display: grid; grid-template-columns: repeat(5, 1fr); gap: 16px; margin-top: 8px; }
  .step { min-height: 128px; background: #fff; border: 1px solid var(--line); border-radius: 18px; padding: 16px 16px; position: relative; }
  .step b { display: block; font-size: 23px; margin-bottom: 6px; }
  .step span { font-size: 19px; color: var(--muted); line-height: 1.3; display: block; }
  .step::after { content: "→"; position: absolute; right: -17px; top: 44px; font-size: 26px; color: var(--muted); }
  .step:last-child::after { content: ""; }
  .step.hl { border-color: var(--blue); background: var(--blue2); }

  table { border-collapse: collapse; width: 100%; font-size: 21px; }
  th { text-align: left; font-size: 19px; color: var(--muted); border-bottom: 2px solid var(--line); padding: 6px 12px; background: transparent; }
  td { padding: 8px 12px; border-bottom: 1px solid var(--line); background: transparent; }
  tr:nth-child(2n) td { background: transparent; }
  .g { color: var(--green); font-weight: 700; } .r { color: var(--red); font-weight: 700; } .a { color: var(--amber); font-weight: 700; }

  pre.flow { margin: 0; }
  .flow { background: #fff; border: 1px solid var(--line); border-radius: 18px; padding: 20px 26px; font-family: ui-monospace, SFMono-Regular, Menlo, Consolas, monospace; white-space: pre-wrap; font-size: 21px; line-height: 1.42; }
  .source { font-size: 15px; color: var(--muted); position: absolute; left: 70px; bottom: 26px; }
  h2 + .callout, h2 + .two, h2 + .three, h2 + .chain, h2 + table, h2 + .flow, h2 + ul, h2 + p { margin-top: 0; }
  .callout, .two, .three, .chain, table, .flow { margin-bottom: 0; }
  * + .callout, * + .two, * + .three, * + .chain, * + table, * + .flow, * + ul, * + p { margin-top: 30px; }
  .small { font-size: 22px; line-height: 1.38; color: var(--muted); margin-top: 36px; }
---

<!-- _class: cover -->
<!-- _paginate: false -->

<div class="kicker">Technical report</div>

# go2lean

# A Lean model, a fuzzer, and real execution

<div class="subtitle">What the model is for, how much of Go and C it covers, what it finds that fuzzing does not, and what turning advisories into properties found.</div>

<span class="pill">Go + C</span><span class="pill">Lean 4</span><span class="pill">2026-11</span>

---

## Four questions this talk answers

<div class="four">
<div class="card blue">

### What is Lean for?

A model that searches. Real code that judges.

</div>
<div class="card blue">

### How much is covered?

How faithful the model is, in Go and in C.

</div>
<div class="card blue">

### Why not just fuzz?

One spec, two searchers.

</div>
<div class="card blue">

### What did it find?

And the method: an advisory becomes a property.

</div>
</div>

---

## The claim

<div class="callout big-claim">
<p>One spec, written once. A model and a fuzzer both search it. Every finding is judged by running the real code.</p>
</div>

<div class="three">
<div class="card"><h3>The model</h3><p>Exhausts small domains. Says "none exists", not just "none found".</p></div>
<div class="card"><h3>The fuzzer</h3><p>Runs the real code at scale, where the model has no semantics.</p></div>
<div class="card"><h3>The replay</h3><p>The judge. A model counterexample is not yet a bug.</p></div>
</div>

---

<!-- _class: part -->
<!-- _paginate: false -->

<div class="num">1</div>

# What Lean is for

Not proofs, for now. A precise, executable semantics.

---

## The pipeline

<div class="chain">
<div class="step"><b>Source</b><span>Go through go/ssa, C through LLVM IR</span></div>
<div class="step hl"><b>Lean model</b><span>executable, fuel-bounded</span></div>
<div class="step"><b>Search</b><span>random, then exhaustive</span></div>
<div class="step"><b>Real replay</b><span>go test, or C under ASan</span></div>
<div class="step"><b>Verdict</b><span>confirmed, spurious, inconclusive</span></div>
</div>

<p>Two front ends feed <b>one</b> model. Only what real execution reproduces is reported as confirmed.</p>

---

## Why a Lean model, and not a script

<div class="three">
<div class="card ok">

### Gaps are explicit

Total functions. What the model does not know is reported as "stuck" or "assumed", never guessed.

</div>
<div class="card ok">

### A search can end

Small domains are enumerated. "No input exists" is a certificate, not luck.

</div>
<div class="card warn">

### Proofs: available, little used

The same semantics can state theorems. Today the use is the interpreter.

</div>
</div>

<div class="source">Why this matters: a hand-written interpreter's bugs become false "verified" results.</div>

---

<!-- _class: part -->
<!-- _paginate: false -->

<div class="num">2</div>

# How much is covered

Measured, not claimed.

---

## Go: the standard library, measured

<div class="three">
<div><div class="big green">72.2%</div><div class="lbl">of 16,909 standard-library functions translate completely</div></div>
<div><div class="big amber">1.8%</div><div class="lbl">are Go the model does not cover yet (the language gap)</div></div>
<div><div class="big">7.9%</div><div class="lbl">stand on the runtime, <code>unsafe</code>, assembly or the OS</div></div>
</div>

<p>165 packages, every one compiles. The rest: a dependency not reached, or a value of unknown origin. Both are filled in during a run.</p>

<div class="source">docs/eval/std-audit.md, 2026-11-11. The language gap fell from 3.4% to 1.8% over five fixes.</div>

---

## Go: is the model faithful?

<div class="two">
<div class="card ok">

### Checked against Go

- **685** semantics tests from Goose
- the model agrees on **672**
- **13** known disagreements, each listed with its reason

</div>
<div class="card warn">

### Where it differs, on purpose

- `math/big` sizing uses `math.Log`
- `math/rand` draws are havoc in range
- network and ticker tests are sampled

</div>
</div>

<p class="small">A disagreement that changes, better or worse, fails CI until the list is updated.</p>

---

## C: what the model runs

<div class="two">
<div class="card ok">

### Modelled

- integers, poison, byte-addressed memory
- heap: use after free, double free
- strings, `ctype`, `strtod`, `errno`
- floats as IEEE bits, `inttoptr`
- inputs built from the C types

</div>
<div class="card warn">

### Not yet

- `sprintf`, `sscanf`
- threads
- most of libc beyond the list
- C++ (the front end compiles `.c`)

</div>
</div>

<div class="source">Regression: 35 C functions with known outcomes, checked in CI. Every unmodelled call is reported, not guessed.</div>

---

<!-- _class: part -->
<!-- _paginate: false -->

<div class="num">3</div>

# And fuzzing?

Complementary, and we measured it.

---

## Same spec, two searchers

<div class="two">
<div class="card blue">

### Model search

- runs the **model**
- enumerates small domains
- guided by the code's own constants
- gives certificates

</div>
<div class="card blue">

### Fuzzer

- runs the **real code**
- coverage-guided, millions of runs
- no model gaps
- no certificates

</div>
</div>

<p>Both read the <b>same spec</b>. Every finding from either goes through the same replay.</p>

---

## We measured it, and it is not a win

<div class="three">
<div><div class="big">9</div><div class="lbl">defects found by the model search (of 29)</div></div>
<div><div class="big amber">14</div><div class="lbl">found by Go's fuzzer, with the same specs</div></div>
<div><div class="big green">8</div><div class="lbl">found by both</div></div>
</div>

<div class="callout warn">
<p>On functional defects the fuzzer finds as much or more. Do not sell the model as a better fuzzer.</p>
</div>

<div class="source">docs/eval/search-vs-fuzz.md: 29 real fix commits, protocol written before the run, rerun after fixes. Model-only: 1. Fuzzer-only: 6.</div>

---

## So what is the model for?

<div class="three">
<div class="card ok">

### Say "none exists"

Cannot be said by a fuzzer. cJSON: 74 of 79 API functions exhausted.

</div>
<div class="card ok">

### Confirmed means confirmed

A model counterexample is replayed in real code. Spurious ones are dropped, and counted.

</div>
<div class="card ok">

### Pick the fuzzer's work

The model settles what it can. The fuzzer takes the rest, on real code.

</div>
</div>

<p class="small">The model's weakness is the same as its strength: where it has no semantics, it is stuck. That is when the fuzzer matters.</p>

---

## A finding a crash-fuzzer cannot state

<pre class="flow">spec:  norm(norm(p)) == norm(p)

fasthttp URI.Path()
  "/%252e%252e/secret"  ->  "/%2e%2e/secret"
  parsed again          ->  "/secret"</pre>

<p>Nothing crashes, so a crash-guided fuzzer has no signal. The spec states the contract; the search breaks it.</p>

<div class="source">fasthttp 7b1ac76. Low severity: needs a second decoder, such as a proxy chain. Not yet reported upstream.</div>

---

<!-- _class: part -->
<!-- _paginate: false -->

<div class="num">4</div>

# What it found

Severity is our own reading. Mostly in other people's code.

---

## The numbers

<div class="three">
<div><div class="big">69</div><div class="lbl">confirmed defects, in 29 projects</div></div>
<div><div class="big green">40</div><div class="lbl">new: not known to the project</div></div>
<div><div class="big red">7</div><div class="lbl">rated high</div></div>
</div>

<table>
<tr><th>high</th><th>medium</th><th>low</th><th>minor</th></tr>
<tr><td class="r">7</td><td class="a">20</td><td>29</td><td>13</td></tr>
</table>

<div class="source">docs/findings.md, 2026-11-18. Every defect reproduced by hand outside the tool. 16 more were already reported upstream.</div>

---

## High severity: six are already public

<div class="three">
<div class="card danger">

### Redirects

- chi `RedirectSlashes`: Host into `Location`
- chi: the backslash bypass of that fix

</div>
<div class="card danger">

### Guards and origins

- echo: `/%61dmin/...` skips an `/admin` guard
- fiber CORS: wildcard matched by suffix only

</div>
<div class="card danger">

### Identity

- fiber proxy: client `X-Real-IP` kept
- chi `RealIP` trusts the leftmost `X-Forwarded-For`

</div>
</div>

<p class="small">Five were re-found from published advisories and are fixed upstream. One (chi <code>RealIP</code>) is a published advisory, still shipped. One is new, on the next slide.</p>

---

## The one new high: beego filter bypass

<div class="callout danger">
<p>With <code>RouterCaseSensitive = false</code>, a guard filter whose pattern has capitals never runs.</p>
</div>

<div class="two">
<div class="card">

### What happens

- `InsertFilter("/Admin/*", ...)`
- `GET /Admin/panel` returns **200, served unguarded**
- Same code with case-sensitive routing: 403
- Default config and lowercase patterns are not affected

</div>
<div class="card">

### Why

- `newFilterRouter` lowercases the pattern for its field
- but builds the match tree from the **original** pattern
- routes and request paths are lowercased, so the tree never matches

</div>
</div>

<p class="small">beego master 939cfde. Same class as CVE-2021-30080. Private report drafted, not filed. Internal only until disclosure.</p>

---

## stb_image: what the C line found

<div class="three">
<div class="card warn">

### GIF: out-of-bounds read

An LZW code chain reads `prefix[4098]` of a 4097-entry array. **Found by the fuzzer**, on a spec the model held.

</div>
<div class="card">

### JPEG: signed overflow

In the scalar IDCT. Rated **minor**: no memory error.

</div>
<div class="card">

### PSD: out-of-range float

A negative float to `unsigned char`. Rated **minor**.

</div>
</div>

<p class="small">The GIF read is already fixed on the project's <code>dev</code> branch, not yet on <code>master</code>. So it counts as re-found, not new.</p>

---

## Why stb_image had something

<div class="two">
<div class="card">

### The configuration gap

The project's own fuzzers build with SIMD on, so they run the SSE2 IDCT. The scalar one is never fuzzed under UBSan.

</div>
<div class="card">

### Our model runs C as written

It follows the portable code. Different build, different bugs found.

</div>
</div>

<div class="callout warn">
<p>Plain undefined behaviour is not the target. We now look for memory errors, and rate the rest minor.</p>
</div>

---

<!-- _class: part -->
<!-- _paginate: false -->

<div class="num">5</div>

# From a CVE to its relatives

The method behind the best results.

---

## The method

<div class="chain">
<div class="step"><b>Advisory</b><span>one published CVE or GHSA</span></div>
<div class="step hl"><b>Property</b><span>stated generally, never the trigger</span></div>
<div class="step"><b>Specs</b><span>an agent writes them from it</span></div>
<div class="step"><b>Versions</b><span>vulnerable, fixed, latest</span></div>
<div class="step"><b>Relatives</b><span>variants, incomplete fixes</span></div>
</div>

<ul>
<li>The property is kept in a catalog and reused on other projects</li>
<li>The same spec file runs <b>unchanged</b> on every version</li>
</ul>

---

## Go: one advisory found the next

<table>
<tr><th>chi version</th><th>result</th><th>what it shows</th></tr>
<tr><td>v5.2.1</td><td class="r">violated</td><td>Host into <code>Location</code>: advisory one</td></tr>
<tr><td>v5.2.3</td><td class="r">violated</td><td><code>//\evil.com/</code>: <b>advisory two</b>, never named in the guidance</td></tr>
<tr><td>HEAD</td><td class="g">held, 1.25M runs</td><td>both fixed</td></tr>
</table>

<p>The second advisory was a bypass <b>introduced by the first fix</b>. The property found it from the first one alone.</p>

---

## Go: a whole class from one example

<div class="two">
<div class="card blue">

### The guidance named

echo, `%2F` in a static-file path (one advisory).

The property: *the guard must agree with what is finally served.*

</div>
<div class="card danger">

### The search found

`/%61dmin/f.txt`: the `a` encoded. Routes past the guard, then the file handler decodes it and serves the file.

The class fixed a minor version later.

</div>
</div>

---

## C: cJSON, one CVE as the only guidance

<table>
<tr><th>finding</th><th>v1.7.16</th><th>v1.7.17</th><th>v1.7.18</th></tr>
<tr><td>CVE-2024-31755, <code>SetValuestring(NULL)</code></td><td class="r">found</td><td class="r">found</td><td class="g">fixed</td></tr>
<tr><td>CVE-2023-50471, NULL into array insert</td><td class="r">found</td><td class="g">fixed</td><td class="g">fixed</td></tr>
<tr><td>use after free, <code>ReplaceItemInObject</code></td><td class="r">found</td><td class="r">found</td><td class="r">found</td></tr>
</table>

<p class="small">Guidance: CVE-2023-50472 only. The last row is still open upstream, with a fix waiting. Floats had to be modelled first, or CVE-2023-50471 stayed out of reach.</p>

---

## C: a second chain, on the parser

<div class="two">
<div class="card blue">

### Given

CVE-2019-11834: an out-of-bounds read on a NUL in a string literal, fixed in 1.7.11.

The property: *a parser reads only the bytes it was given.*

</div>
<div class="card danger">

### Found

**CVE-2023-53154**: a heap over-read in `parse_string`, one byte past a buffer of exactly `len` bytes.

Held on v1.7.18 and HEAD, 13M fuzzer runs each.

</div>
</div>

<p class="small">The spec builds a value from a grammar and cuts it at every length. The raw-bytes specs never reached that check.</p>

---

## What did not work

<div class="three">
<div class="card warn">

### A wrong threat model

chi client IP: the spec never varied the TCP peer, the attacker's lever. **Missed twice.** A spec is only as good as its threat model.

</div>
<div class="card warn">

### Clean libraries

json-parser, parson, lcms2: **no finding**, tens of millions of runs. Memory safety on untrusted input is what fuzzers have already worked hardest.

</div>
<div class="card warn">

### The spec's own mistakes

A spec broke a documented contract. Another expected a result a release changed on purpose. We told them apart by reading, not by the tool.

</div>
</div>

---

## What we claim, and what we do not

<div class="two">
<div class="card ok">

### We claim

- advisory properties find relatives, and did
- the model's "none exists" is a certificate
- every finding was run in real code

</div>
<div class="card danger">

### We do not claim

- that the model beats a fuzzer: it does not
- that no counterexample means correct
- coverage of C++ or of Rust, yet

</div>
</div>

---

## Takeaways

<div class="three">
<div class="card blue">

### A spec is the asset

It is searched by a model and a fuzzer, and survives across versions.

</div>
<div class="card blue">

### An advisory is an oracle

Stated generally, it finds its variants and the fixes that missed.

</div>
<div class="card blue">

### Real code decides

Confirmed, spurious, inconclusive. Counted, never hidden.

</div>
</div>

<div class="callout">
<p>Next: properties a fuzzer cannot state, such as round trips, idempotence, and two implementations that must agree.</p>
</div>

---

## Sources

<ul>
<li>All findings, with severity and status: <code>docs/findings.md</code></li>
<li>Advisory method, Go: <code>docs/eval/cve-specs.md</code>; C: <code>docs/eval/cjson.md</code></li>
<li>Model against a fuzzer: <code>docs/eval/search-vs-fuzz.md</code></li>
<li>Coverage: <code>docs/eval/std-audit.md</code>, <code>docs/reference.md</code></li>
<li>The property catalog: <code>docs/specs/README.md</code></li>
</ul>

<p class="small">Counts as of 2026-11-18. Severity is go2lean's own reading, not the projects'.</p>
