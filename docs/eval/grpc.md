# grpc-go (M5, 2026-11-13)

What go2lean finds in a library everyone knows, run as a user runs it.
Milestone [#220](https://github.com/fooexec/go2lean/issues/220).

## Protocol

- **Target:** [grpc/grpc-go](https://github.com/grpc/grpc-go) at
  `70b8760`. Its 269 packages less the examples, interop, benchmarks,
  test utilities and commands leave 212. 3 of them hold only test files
  (`.../e2e_test`), which leaves **209 packages**.
- **Tool:** frozen at `a08f8f3`, which is `main` after #219 plus the two
  fixes this run led to before it restarted (below). Go 1.26.6, on 4
  cores.
- **Arm A: templates.** `go2lean run` with the default flags, one package
  at a time, two packages at once, a 30-minute cap per package. Every
  exported function and method gets a `no_panic` property, and one with a
  loop gets `no_hang`. No spec is written.
- **Arm B: an agent writes specs.** `go2lean agent -llm claude-code` on
  six packages whose documentation states results: `internal/grpcutil`,
  `metadata`, `internal/transport`, `status`, `codes` and
  `encoding/proto`. The agent is Claude, through the `claude` CLI, and
  sees only go2lean's prompts. See [below](#arm-b-the-agent).
- **Classification:** each confirmed finding is put in one of these
  classes:
  - a **bug** (it contradicts the documentation, or a caller can reach a
    panic that is not documented);
  - **documented** (a documented panic, or a resource the caller must
    release);
  - **misuse** (an argument outside what the API accepts);
  - **forged** (a value the package's own code never builds).
- **Per-package data:** [grpc/results.tsv](grpc/results.tsv).

## Arm A: templates

| | |
|---|---|
| packages | 209 (176 with properties; 1 stopped by the Lean build running out of memory) |
| properties | 2,860 |
| confirmed (tier 1) | 153 (15) |
| spurious | 56 |
| inconclusive | 40 |
| time | 11,659 s summed over the packages, 55 s median; the slowest are xDS packages (`xdsclient`: 1,039 s, `xds/server`: 751 s) |

### What the confirmed findings are

**No bug.** That is the expected result for a library with grpc-go's tests
and fuzzing; the classes say what the templates reach instead.

**Tier 1, 15 findings:**

| class | findings |
|---|---|
| documented | `metadata.Pairs` with an odd count; pemfile's `NewProvider` goroutine, released by `Close` (`Provider.Close`'s contract); `leakcheck.CheckTrackingBufferPool` and `ResetConcurrentHandshakeSemaphoreForTesting`, test helpers |
| misuse | a negative size to the buffer pools' `Get` (3: `NopBufferPool`, `NewTieredBufferPool`, `NewBinaryTieredBufferPool`; the binary pool panics with an index out of range, the others in `make`); `SliceBuffer.Slice` past the end; ALTS's `SliceForAppend` with `n < 0` and `CounterSide` on a short counter (internal); `ConstructHeaderMap` with an odd-length `added`, which `AppendToOutgoingContext` never builds |
| forged | `mem.Reader`'s `Read`, `ReadByte` and `Discard`, on a `Reader` whose length the test set without data (3); `ClientStream.Close` on a stream with a nil channel |

The four forged findings should not have been at tier 1. A fix since
ranks a receiver whose unexported fields the test set at tier 2: the
three `mem.Reader` findings are tier 2 on a rerun.

**Tiers 2 to 4, 138 findings:**

| kind | findings |
|---|---|
| nil dereference, of a nil the caller passes or a field it leaves nil | 117 |
| a goroutine left running, or a call that blocks: `CallbackSerializer`, `PubSub` and the cert `Distributor` run until their context is cancelled; `ClientConn.WaitForStateChange` blocks until the state changes | 10 |
| explicit checks on a nil argument: `RegisterCodec(nil)`, `RegisterCodecV2(nil)`, grpcsync's `onZero` | 3 |
| other documented panics and misuse: `AppendToOutgoingContext` with an odd count, a manual resolver used before it is built, RLS's `Throttler` with zero bins (a divide by zero and an index of -1), a negative length to `make` | 5 |
| other: a nil map written, a nil `LoadBalancingConfig` asserted, `reflect.Elem` on package state the model did not know | 3 |

`internal/stats`, rerun with the import fix below, adds 6 at tier 1. They
are all one deliberate assertion: `MetricsRecorderList.Record*` panics
when a call passes the wrong number of labels for its metric.

### What the tool got wrong

These are the problems the run showed, and what was done about each.

| problem | effect | status |
|---|---|---|
| A panic in a test file's `init` (credentials' tests load certificates from `testdata`) was taken as the target's. The sandbox hid the module's `testdata` too, by mounting a private `/tmp` and home over it | 6 false findings **confirmed at tier 1** | fixed ([#221](https://github.com/fooexec/go2lean/issues/221)): a panic before the PoC's test starts is inconclusive, and the module root stays visible, read-only |
| grpclog's `init` runs `SetLoggerV2(newLoggerV2())`. A call with arguments was left out of the initializer's slice, so the logger was nil in every trial | 16 spurious, 4 inconclusive | fixed: grpclog gives none |
| A PoC chains a constructor's and a method's ABIs. Each ABI chose its own aliases, and `internal/stats` imported `experimental/stats` and `grpc/stats` both as `stats` | 7 PoCs that did not compile | fixed: one alias table per package |
| A receiver built from unexported fields was ranked "plain inputs" | 4 tier-1 findings that are forged | fixed (above) |
| An input is a pointer to the zero value of another package's struct, whose fields the test cannot fill | 27 inconclusive | known: the PoC says so |
| `crypto/tls`, `crypto/x509`, `os.ReadFile`, protobuf reflection: the boundary | about 15 spurious | M2 (#53) |
| A stuck callee's results, an unknown function value, an assumed `globalServerOptions` | about 25 spurious | [#218](https://github.com/fooexec/go2lean/issues/218) and its kin |
| Concurrency: the replay left the model's schedule, or Go's scheduler did not take it | 10 inconclusive | #116 |
| `xdsclient`'s model is too large for the Lean build (killed, exit 137) | 1 package not searched | [#223](https://github.com/fooexec/go2lean/issues/223) |
| Time: one package at a time, with a barrier between search and replay; but most of a run is the Lean build of the search executable | idle CPUs | fixed ([#222](https://github.com/fooexec/go2lean/issues/222)): packages run side by side, and the build is timed on its own and is a third of its size |

## Arm B: the agent

Six packages, one `go2lean agent -llm claude-code` run each, with the
default two rounds (propose and repair, then a report that may revise the
specs). The agent saw the package source, its documentation and
go2lean's model report; it wrote 96 specs in the first round. On these packages `agent`
also searches `no_panic` and `no_hang` for unexported functions.

| package | time | what the agent's run found |
|---|---|---|
| `internal/transport` | 5,280 s | **one bug**, below; a flow-control invariant that fails on the internal type; `ceil(n, 0)` divides by zero, with no caller that passes 0 |
| `metadata` | 937 s | no bug. `Pairs` and `AppendToOutgoingContext` panic on an odd count, as documented. From the source: `MD.Set` keeps the caller's slice, and `FromIncomingContext` and `FromOutgoingContext` treat two keys that differ only in case differently |
| `internal/grpcutil` | 779 s | no bug. `div(d, 0)` divides by zero; every caller passes a non-zero constant |
| `codes` | 803 s | no bug. The specs tie `Code.String` and the canonical names together, and fail 23 or 24 of 24 mutants |
| `status` | 666 s | no bug. 8 spurious verdicts, from `errors.As`, `fmt.Sprintf` and `reflect.Zero`, which the model does not follow |
| `encoding/proto` | 347 s | no bug. Every input the engine can build for `v any` is a non-message, so the marshalling paths were never reached; the mutants showed it, and the agent restated the specs as total ones |

### The bug: `MaxConnectionAge` below 10 ns crashes the server

```go
// internal/transport/http2_server.go
func getJitter(v time.Duration) time.Duration {
	if v == infinity {
		return 0
	}
	// Generate a jitter between +/- 10% of the value.
	r := int64(v / 10)
	j := rand.Int64N(2*r) - r // panics when r <= 0
	return time.Duration(j)
}
```

`NewServerTransport` calls it on `MaxConnectionAge` from
`grpc.KeepaliveParams`, after replacing only a zero value. `KeepaliveParams`
checks `Time`, not `MaxConnectionAge`. So a server built with

```go
grpc.NewServer(grpc.KeepaliveParams(keepalive.ServerParameters{MaxConnectionAge: 5 * time.Nanosecond}))
```

or with any negative `MaxConnectionAge` dies with `panic: invalid argument
to Int64N` on the first connection it accepts. That is the whole process,
not one connection, because the panic is on the accept goroutine. We
reproduced it with a real server and client. It is not reported upstream
yet.

Who found what:
- **The template found the panic.** go2lean's `no_panic` on `getJitter`
  gave `v = -2743846568614014354` at the first random trial, and the replay
  confirmed it.
- **The agent found the path.** It read `NewServerTransport` and
  `KeepaliveParams` and said which configuration reaches the panic.
- **`go2lean run -unexported` ranks it at tier 2**, one of 41 findings
  that "depend on the callers", among nil receivers. Following the call
  site to an exported parameter, as the agent did, is mechanical:
  [#224](https://github.com/fooexec/go2lean/issues/224).

### The invariant: `inFlow.maybeAdjust`

The agent wrote a sequence spec from the comment on `inFlow.delta` ("the
extra window update given by receiver"): the window granted to the peer
never exceeds what `onData` will accept. go2lean found a 7-event
counterexample and Go confirmed it: two calls of `maybeAdjust(69999)` with
no read between them each return 69,999, so 139,998 bytes of window go to
the peer while `onData` accepts `limit + 69,999`. The transport calls
`maybeAdjust` once for the 5-byte header and once for the body, and
`onRead` resets `delta` between messages, so neither the agent nor we found
a path through the API that makes two such calls. It is recorded as an
invariant the type does not keep, not as a bug.

### What the agent's runs showed about the tool

| problem | status |
|---|---|
| On `status` and `encoding/proto`, the model answered the proposal with a tool call it cannot make, and the run stopped | fixed: asked once more |
| On `encoding/proto`, the report round did the same | fixed: asked once more |
| The fuzz targets exported for `encoding/proto` did not build: a skipped target's import stayed, and the input reader went unused | fixed |
| `context.Context` parameters, and a `*Status` from another package's unexported field, cannot be built by the engine or by Go's fuzzer; the agent restated those specs as sequences that build the value | known (the agent's way round is the right one) |
| `errors.As`, `fmt.Sprintf`, `fmt.Fprintf` and `reflect.Zero` gave the spurious verdicts | M2 (#53) |

## Rerun after the fixes (2026-11-13)

Templates again, same protocol, tool frozen at `e90696f` (#226, #227).
That build adds four things: a Lean build a third of the size,
`context.WithValue`, interface inputs that take the module's own
implementers, and tracing an unexported panic back to the API. The ranks
below are recomputed with `5418a14`, which ranks an interface input
holding an unexported type at tier 2.

| | arm A | rerun |
|---|---|---|
| properties | 2,860 | 2,858 |
| confirmed | 153 | 155 |
| tier 1 | 15 | 11 |
| spurious | 56 | 52 |
| inconclusive | 40 | 37 |
| time, summed | 11,659 s | 7,230 s |
| `xdsclient` | out of memory after 1,039 s | searched, 107 s |

The 11 tier-1 findings are arm A's documented panics and misuse. The
four forged ones are now at tier 2, so the templates alone still find no
bug.

The rerun showed two problems, both fixed:
- **An interface field took its own struct.** grpc's `oauth.TokenSource`
  embeds the `oauth2.TokenSource` it implements, and generating it
  recursed until the search overflowed its stack.
- **Forged values reached tier 1.** Nine `mem.BufferSlice` findings held
  `*buffer` values built field by field, which no caller can make.

**With `-unexported`, the bug comes first.** `go2lean run -unexported
./internal/transport` takes 556 s and confirms 58 findings. One is at
tier 1, the `MaxConnectionAge` bug, with the path the agent had to find:

```text
grpc.KeepaliveParams(kp.MaxConnectionAge) → serverOptions.keepaliveParams
  → ServerConfig.KeepaliveParams → transport.getJitter(v)
```

No agent and no spec was needed.

**The whole module with `-unexported`.** Frozen at `5418a14`, all 212
packages again:

| | rerun | `-unexported` |
|---|---|---|
| properties | 2,858 | 5,053 |
| confirmed | 155 | 515 |
| tier 1 | 11 | 12 |
| tier 2 | 52 | 262 |
| spurious | 52 | 157 |
| inconclusive | 37 | 391 |
| time, summed | 7,230 s | 11,431 s |

The one new tier-1 finding is `getJitter` above; the other eleven are the
rerun's. The extra 360 confirmed findings are panics in unexported
helpers that no exported path reaches without passing a check, so they
rank at tier 2 or below. The flag costs 58% more time and triples what
there is to triage below tier 1, for one finding. It stays opt-in: run it
on the packages that hold the logic (`internal/transport` here), not on
the whole module.

**The agent's `metadata` specs.** Rerun with the context model, their
spurious verdicts go from 8 to 1, a `fmt.Fprintf` the model does not
follow.

## What this run says

- **On the templates alone, grpc-go has no bug go2lean can reach.** 153
  confirmed panics, and every one is documented, a misuse or a forged
  input. A mature library with its own fuzzing should look like this. The
  run's value was in the tool: four fixes and three issues.
- **With an agent's specs, it found a real one.** The bug is a panic
  that a template finds. But it sits in an unexported helper, and the
  agent's reading of the call site made it a finding a maintainer can
  act on. Ranking can learn that step (#224).
- **Mutation testing keeps the agent honest.** Of the 96 specs, only the
  `inFlow` invariant failed in Go. Several held because their guards
  skipped every input the engine could build: a nil registry, an opaque
  `context.Context`, a non-message `any`. The mutants exposed each one
  (0 of N killed), and the agent's report rewrote them. The specs that
  stayed are exhausted over their domain and fail most mutants.
- **Time.** The template arm took 11,659 s over 209 packages; the agent
  arm 8,812 s for six, most of it `internal/transport` (304
  properties). Most of the template arm's time was not search. On
  `./balancer/...` (24 packages) in one workspace, the Lean build of the
  search executable took 651 of about 800 s. Running packages side by
  side changed the total by less than the noise: 770 and 844 s before,
  808 s after. The build itself was bloated, in two ways:
  - every reflect op repeated the program's whole type table, 15 KB at a
    time, so one protobuf closure was 1.9 MB of Lean;
  - helpers in the packages' test files pulled `go-cmp` and the test
    utilities into the model.

  With both fixed (#222), the generated Lean drops from 25 MB to 9.8 MB,
  the build from 604 s to 198 s, and the run from 808 s to 317 s.
