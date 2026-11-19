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

| | draft 1 | draft 2 |
|---|---|---|
| property as drafted | "never redirects to a host taken from the Host header" (the mechanism the advisory names) | "the redirect lands on the requested origin, resolved as a browser does" (the harm); backslash, `//`, `/\` listed as input classes |
| calibration: advisory 1 on v5.2.1 | **reproduced** (3 specs violated, replay confirmed) | **reproduced** (Host-header spec violated, replay confirmed) |
| v5.2.3 (advisory 1 fixed, advisory 2 present) | Host spec held in real Go; **advisory 2 not found** (no spec about the path) | **advisory 2 not found**: both specs fail on the fuzzer's first seed input, before any search |
| cause of the miss | the property named the mechanism, so the spec tested only it | the property was right; the agent's spec is false on a trivial input (its oracle needs a 301 for every path, among other conditions) |

## What this says

- **The calibration gate works.** Both drafts reproduce their own advisory
  on the version before the fix.
- **Draft 1's miss was in the property.** A property stated by the
  mechanism cannot reach the next advisory, which uses another mechanism for
  the same harm. The prompt now asks for the harm and lists the mechanisms
  as input classes (`internal/advisory`).
- **Draft 2's miss was in the spec.** The property was the right one, but
  the spec the agent wrote from it is false on its own seed input, on the
  fixed versions too, so it can find nothing there. A spec that fails on
  the fixed version for the wrong reason is not a transfer test.
- **So the gate needs a second half**: the spec must hold on the fixed
  version under the real fuzzer's seed corpus before its result on any
  other version counts. A spec that does not pass goes back to the agent
  with the failing input (a revision round that exists for verdicts, but
  not yet for this check). Tracked in #287.

Two runs on one family are a data point, not a rate. The next families for
the table are cJSON, fiber CORS and echo's static handler, which have two
advisories each.
