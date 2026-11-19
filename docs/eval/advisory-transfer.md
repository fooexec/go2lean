# Advisory transfer, leave one out: chi RedirectSlashes (2026-11-19)

The measure of #287: draft a catalog property from the first advisory of a
family with `go2lean advisory`, run it on the version before the fix, then on
the later versions, and see whether the next advisory is found without being
named. Here the family is chi's `RedirectSlashes` open redirects:
GHSA-vrw8-fxc6-2r93 (the Host header, fixed in v5.2.2) and GHSA-mqqf-5wvp-8fh8
(a backslash bypass the first fix introduced, fixed in v5.2.4).
The same run done by hand, with a property written by a person, found the
second advisory ([advisory specs](cve-specs.md)); this is the same question
asked of the generated property.

| | draft 1 | draft 2, first run | draft 2, third run (seed gate on) |
|---|---|---|---|
| property as drafted | "never redirects to a host taken from the Host header" (the mechanism the advisory names) | "the redirect lands on the requested origin, resolved as a browser does" (the harm); backslash, `//`, `/\` listed as input classes | same |
| advisory 1 on v5.2.1 | **reproduced** (3 specs violated, replay confirmed) | **reproduced** | **reproduced** |
| advisory 2 on v5.2.3 | Host spec held in real Go; **not found** (no spec about the path) | **not found**: both specs fail on the fuzzer's first seed input | **found, by the spec**: `SeqRedirectSlashesHostHeaderSameOrigin("example.com", "/\\evil.com", "")` is false on v5.2.3 and true on HEAD (checked by hand). The fuzzer's reported counterexample was another input (below) |
| cause | the property named the mechanism | the agent's spec was false on a trivial input | |

## What this says

- **The calibration gate works.** All drafts reproduce their own advisory
  on the version before the fix.
- **Draft 1's miss was in the property.** A property stated by the
  mechanism cannot reach the next advisory, which uses another mechanism for
  the same harm. The prompt now asks for the harm and lists the mechanisms
  as input classes (`internal/advisory`).
- **With the harm stated, the spec the agent wrote from advisory 1 alone is
  false on the backslash path on v5.2.3 and true on HEAD.** Its oracle
  resolves `Location` as a browser does, which is what advisory 2 is about.
  So the transfer worked: the spec can tell v5.2.3 from HEAD on advisory 2's
  input.
- **The search did not show it.** `go test -fuzz` stops at the first
  failing input and reported another one, after 871,559 executions:
  `"\r/0"`, a carriage return in the path. On v5.2.3 and on HEAD that gives
  `Location: /\r/0`, which the spec resolves the way a browser strips
  control characters, as `//0`. On the wire, `net/http` writes a carriage
  return in a header value as a space, so this is the spec's reading, not a
  redirect; it fails on HEAD too and is noise.
- **So a violation must be triaged across versions.** A counterexample that
  also fails on the latest version is the spec's noise (or a still-present
  defect, which is read by hand); one that fails on the vulnerable version
  and holds on the fix is the finding. Here the fuzzer's report was the
  first kind and hid the second. Next: run each counterexample on the later
  versions automatically, and keep the fuzzer going past a failure that
  also fails on the fixed version (tracked in #287).
- **The seed gate did not run in the third run.** It looked for spec
  properties before they were written to the workspace, found none, and was
  skipped without an error that stopped anything. Fixed (the specs are
  proposed first); the run above is the draft 2 specs as the agent wrote
  them, with the gate not yet effective. The second run's seed failure
  (`SeqRedirectSlashesPathOnlySameOrigin`, false at 0 executions) is still
  there and is what the gate will send back.

One family, a few runs: a data point, not a rate. The next families for the
table are cJSON, fiber CORS and echo's static handler, which have two
advisories each.
