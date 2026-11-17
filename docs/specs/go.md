# Go properties

## Applies to

Go only. The binding is go2lean's own: spec functions marked
`go2lean:spec` or `go2lean:sequence`, searched in the model and fuzzed by
`go2lean fuzz` (Go's fuzzer), with every finding replayed as a
`go test`. These properties are built in. The templates generate them
without a spec, and the agent adds sequence specs that drive the
package's API to them.

## Properties

### No goroutine leak

An operation that returns, including on an error, leaves behind no
goroutine of the package that is blocked forever on a channel or a lock.
A goroutine that the documentation says outlives the call (a server, a
ticker) is not a leak.
- **Threat model:** the caller chooses the inputs, including those that
  make the operation fail half-way (a lexer's parse error, a cancelled
  context, a closed connection).
- **Oracle:** the replay stage's goroutine dump before and after the call.
- **Found:**
  - magiconair/properties: every parse error (filed as #101);
  - aymerick/raymond: every template syntax error.

### No deadlock and no hang

A documented operation returns once its documented condition holds (a
pool drained, a task done), for every order of the calls the API allows.
- **Threat model:** the caller chooses the order and timing of calls
  across goroutines, within the documented contract.
- **Oracle:** the model's schedule, replayed by rewriting the package so
  that each channel, lock and `select` waits for its turn.
- **Found:**
  - alitto/pond: a subpool's `StopAndWait` (already reported);
  - panjf2000/ants: a size above `MaxInt32`.

### No data race

Two goroutines the API allows to run together never access the same
memory without synchronization when one of them writes.
- **Oracle:** Go's race detector on the replay.
- **Found:** eapache/go-resiliency, in the package's own test.

### No panic on documented input

A function does not panic on any input its documentation accepts:
indexing, nil dereference, closing a closed channel, sending on a closed
channel, a negative `make` or slice bound, an integer divide by zero, or
`rand` with a non-positive bound. Panics on nil or forged inputs, or that
the documentation names, are not counted.
- **Found:** most of the crash rows in [findings](../findings.md), among
  them grpc-go `MaxConnectionAge`, go-retryablehttp `WriteTo` and fiber
  `BalancerForward(nil)`.

## The general properties in Go

The general properties ([README](README.md)) need nothing language
specific: the agent writes them as `go2lean:sequence` specs over
`net/http` (or the framework's request type), and Go's fuzzer and the
model's search check them.
