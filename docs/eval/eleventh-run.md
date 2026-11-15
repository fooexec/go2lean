# Eleventh run: concurrent libraries, specs from the docs (2026-11-15)

The tenth run's exact references found nothing in mature arithmetic and
JSON libraries. This run went where a model of goroutines should have the
edge: influential, maintained libraries that run goroutines for their
callers (pools, schedulers, caches with cleanup goroutines), with sequence
specs written from their documentation, as `go2lean agent` would write
them (the agent itself could not run here: no API key reached the
session). Tool at `d40efe7` plus the `sort.Ints` fix below.

| project | stars | specs | model | Go's fuzzer | finding |
|---|---|---|---|---|---|
| [panjf2000/ants](https://github.com/panjf2000/ants) `c101e30` | 14.5k | 1 sequence | inconclusive (needs slow code) | violated at the first input | **`Tune` to a smaller size does not limit concurrency**, unreported |
| [go-co-op/gocron](https://github.com/go-co-op/gocron) `a23a37d` | 7.2k | 1 sequence | (not run) | violated in Go | `WithLimitedRuns(n)` runs more than n times, open PR #984 (2026-11-10) |
| [hashicorp/golang-lru](https://github.com/hashicorp/golang-lru) `2bb2909` | 5.1k | 2 sequences | 2 spurious | held, 7.4M and 5.7M executions | none |
| [allegro/bigcache](https://github.com/allegro/bigcache) `728164c` | 8.2k | 1 sequence | 1 inconclusive (schedule not reproduced) | held, 6.4M executions | none |

dgraph-io/ristretto was left out: its `Set` may drop an entry by design,
so no spec stronger than "no panic" holds.

## ants: Tune does not shrink a pool

The spec drives a nonblocking pool with tasks that block on a gate, so
the number of busy workers is known, and checks the doc of each call:
`WithNonblocking` ("pool will return ErrPoolOverload when there is no
available workers"), `Tune` ("changes the capacity of this pool"),
`ReleaseTimeout` and `Reboot`. Go's fuzzer broke it at its first input,
`02029+22`: a pool of 1, `Tune(2)`, two busy tasks, released, `Tune(1)`,
then two `Submit`s, which both succeed and run together.

With default options: a pool of 4 runs four tasks, then `Tune(1)`; the
next four tasks all run at once, and `Free()` returns -3, where its doc
says "-1 indicates this pool is unlimited". `Tune` only stores the new
capacity; a worker over it retires when it comes back from a task
(`revertWorker`), but `retrieveWorker` hands out the idle workers already
in the queue without looking at the capacity. So shrinking a pool to shed
load has no effect on the next round of tasks. Not in the tracker (#374
was a race creating workers over the capacity, a different cause); a
draft issue waits for review.

## gocron: limited runs overrun

The spec runs a `DurationJob` on a fake clock with `WithLimitedRuns(n)`
("limits the number of executions of this job to n. Upon reaching the
limit, the job is removed from the scheduler") and counts the runs. It
failed in Go: a run is counted when it completes, so when the task takes
longer than the interval, new runs keep starting. With a real clock, a
50 ms interval and a 200 ms task, `WithLimitedRuns(1)` runs 5 times.
This is gocron's open PR #984, five days old, found independently.

## What the tool got wrong

- **`sort.Ints` was ignored** (a call with no result whose effects were
  not modelled), which made golang-lru's spec compare unsorted keys. It
  is now translated: it calls `slices.Sort`. Fixed here, with a semantics
  test.
- **A function value the model does not know**: golang-lru's eviction
  callback, a bound method value handed to a generic type (#253). The two
  verdicts that remain spurious.
- **A schedule the replay cannot follow** in bigcache's cleanup goroutine
  (#116).
- On ants the model's counterexample needed code too slow to replay; the
  same spec under `go2lean fuzz` found the defect at once.

## What this says

Concurrent libraries kept their bugs where the earlier libraries did not.
Two of four broke a documented contract, both through the order in which
workers and runs are handed out, which an output check on small inputs
never reaches: one new, one reported five days earlier. Both came from a
sequence spec that drives the API through states, not from the model's own
search; the model's part was the replay's checks and a spec format the
fuzzer can run.
