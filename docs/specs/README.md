# Property catalog

Security and correctness properties, each written once so it can be
checked on any project that has the same kind of code. A property comes
from a published advisory (or a defect go2lean found), stated in general
terms with its threat model. The checks go through `go2lean agent`:

```bash
go2lean agent -guidance docs/specs/cors.md -module <dir> <package>
```

The agent reads the property, finds the package's own version of the
guard, and writes specs against the package's documented contract. A
property file is the guidance as it is given: it states the property and
the threat model, never an input that breaks it, so it does not lead the
agent to a known answer. What each run found is in the table below and in
[advisory specs](../eval/cve-specs.md).

## Layers

A property belongs to one of three layers:

1. **General**: about what the code must guarantee to the outside world
   (a redirect, a header, a file, an authorization decision). Any
   language can have the defect. Written without naming a language.
2. **Language or runtime**: about a construct only some languages have:
   goroutines and channels (Go), memory safety (C, C++, `unsafe` Rust),
   unwinding and `Send`/`Sync` (Rust). Each says where it applies.
3. **Binding**: how a language writes and checks a spec: the spec
   function's form, the marker, the fuzzer and the replay harness.

A general property is checked through a binding. A language property
names the bindings it applies to.

## Entry format

Every property file has these sections:
- **Applies to**: the layer, and for a language property the languages
  or runtimes.
- **Property**: one paragraph, the guarantee.
- **Threat model**: what the attacker controls and what it does not. The
  chi client-IP miss came from a spec that left out a direct attacker, so
  this section is never implicit.
- **Oracle**: how a spec decides the property holds: a reference written
  from the contract, a round trip, a browser's resolution of a URL.
- **Inputs**: the pieces a spec builds its inputs from.
- **Pitfalls**: how specs for this property went wrong before.
- **Source**: the advisories it was taken from.

## General properties

| property | file | source advisories |
|---|---|---|
| A router's own redirects stay on the same site | [redirect.md](redirect.md) | go-chi/chi GHSA-vrw8-fxc6-2r93, GHSA-mqqf-5wvp-8fh8 |
| CORS grants only configured origins | [cors.md](cors.md) | gofiber/fiber GHSA-fmg4-x8pw-hjhg, GHSA-g5jq-qmqr-qrwf |
| The client's identity does not come from what it sends | [client-ip.md](client-ip.md) | gofiber/fiber GHSA-3q5p-3558-364f, GHSA-gcfq-8gqf-4876; go-chi/chi GHSA-3fxj-6jh8-hvhx, GHSA-j6qf-3m9c-vwj3 |
| Static files stay under their root and behind their guards | [static-files.md](static-files.md) | labstack/echo GHSA-vfp3-v2rc-x9fp and its successor; beego GHSA-95f9-94vc-665h |
| An authorization matcher grants no more than its pattern | [authz-match.md](authz-match.md) | casbin `KeyMatch` (no advisory; found by go2lean) |

## Language and runtime properties

| file | applies to |
|---|---|
| [go.md](go.md) | Go: goroutine leaks, deadlocks, channel misuse, data races, nil and index panics |
| [llvm.md](llvm.md) | C, C++, Rust and other languages compiled to LLVM: the binding, and their own properties |
| [parser-bounds.md](parser-bounds.md) | C, C++, `unsafe` Rust: a parser reads only the bytes it was given |
| [c-api-memory.md](c-api-memory.md) | C, C++, `unsafe` Rust: a library's API is memory safe on what the API itself produces |

## Runs

Each row is one property on one project, with the version and what the
replay confirmed: Go for the Go line, the C program under ASan and UBSan
for the LLVM line. "Held" counts executions of Go's fuzzer on the agent's
specs.

| property | project | version | result |
|---|---|---|---|
| redirect | go-chi/chi | v5.2.1, v5.2.3, HEAD | re-found both advisories; HEAD held |
| redirect | gin-gonic/gin | `0f09c3a` | **new open redirect** with `RemoveExtraSlash`; `X-Forwarded-Prefix` variant already in PR #4923 |
| cors | gofiber/fiber | v2.52.1 to v3 HEAD | re-found GHSA-g5jq; latest held |
| cors | rs/cors | `7ca5fe2` | held (1.84M); `OriginAllowed` contract defect |
| cors | gin-contrib/cors | `b319528` | **regexp origins unanchored**; two smaller defects |
| client-ip | go-chi/chi | v5.3.2 | missed (threat model left out the direct attacker) |
| client-ip | gofiber/fiber | v2.52.13, v2.52.16 | re-found GHSA-gcfq; latest held |
| client-ip | labstack/echo | v5 `f275a6b` | held (1.2M each) |
| static-files | labstack/echo | v4.15.3, v4.16.5 | re-found the successor advisory; latest held |
| static-files | beego/beego | `939cfde` | **guard filter bypassed with case-insensitive routing** |
| authz-match | casbin/casbin | HEAD | `KeyMatch` over-grants a mid-pattern `*` |
| c-api-memory | DaveGamble/cJSON | v1.7.16 to HEAD | from CVE-2023-50472 alone: re-found CVE-2024-31755 and CVE-2023-50471, each held from its fix on; the open use after free of #1081 |
| parser-bounds | DaveGamble/cJSON | v1.7.17 to HEAD | from CVE-2019-11834 alone: re-found CVE-2023-53154 on v1.7.17; v1.7.18 and HEAD held: 5 of 9 specs exhausted, the other 4 held 13M to 21M libFuzzer runs each |
| parser-bounds | nothings/stb (`stb_image.h`) | `9600522` (master), `dev` | from stb#1225 alone: **signed overflow in the scalar JPEG IDCT**, which the project's fuzzers (SSE2 only) miss; six formats held |

The new defects are listed with all others in [findings](../findings.md),
and their reports wait in [upstream reports](../blog/upstream-reports.md).
