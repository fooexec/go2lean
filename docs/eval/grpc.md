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
| `xdsclient`'s model is too large for the Lean build (killed, exit 137) | 1 package not searched | to file |
| Time: one package at a time, with a barrier between search and replay | idle CPUs | [#222](https://github.com/fooexec/go2lean/issues/222), in progress |

## Arm B: the agent

ARM_B

## What this run says

ARM_SAYS
