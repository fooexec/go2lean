# Twelfth run: the agent on concurrent libraries (2026-11-15)

The eleventh run's specs were written by hand. This run let `go2lean
agent` write them, on two of the same kind of library: panjf2000/ants
(again, 14.5k stars) and alitto/pond (2.2k), both maintained goroutine
pools. Two models: MiniMax-M3 (through its Anthropic-compatible endpoint)
and Claude Opus 5.5 (the default). Every finding below was reproduced
again by hand, outside the agent.

| model | target | specs | outcome |
|---|---|---|---|
| MiniMax-M3 | ants | ~60, one function each (option setters, counters, the internal queues) | the spec file did not build; every replay failed to build (tool bug, fixed) |
| MiniMax-M3 | ants, after the fix | ~65 after 3 repairs | the search did not finish within an hour; stopped |
| MiniMax-M3 | pond | (prompt fixed) sequence specs | a dropped connection ended the run (tool bug, fixed) |
| Claude Opus 5.5 | pond | 4 sequence specs, built at once | **subpool loses a task and `StopAndWait` hangs**: pond's open issue #152 (2026-10-23), found independently |
| Claude Opus 5.5 | ants | 5 sequence specs | **`Invoke(nil)` is accepted and dropped**, and **sizes above MaxInt32 wrap** (`Tune(1<<32-2)` leaves a capacity of -2, and `Submit` then blocks forever): both unreported |

## The findings

- **pond, subpool.**
  - Setup: a nonblocking parent with no queue (`WithQueueSize(0)`) and all
    its workers busy.
  - `sub.TrySubmit` reports the task accepted, but the task never runs,
    its `Wait` never returns, and `sub.StopAndWait()` hangs.
  - Cause: `launchWorker` drops the parent's `ErrQueueFull`.
  - The agent's spec (`SeqSubpoolConservation`) found it in the model, and
    the replay confirmed the hang. Go's fuzzer confirmed it at its first
    input. The agent's report traced the cause to the line.
  - Reported five weeks earlier (#152, with an open fix, #153).
- **ants, `Invoke(nil)`.**
  - `goWorkerWithFunc` stops on a nil argument, the same value `finish()`
    sends, so the caller's task is lost while `Invoke` returns nil.
  - Two `Invoke` calls run the function once.
  - #167 proposed allowing nil arguments in 2021 and was closed without
    being merged. The doc does not say that nil is not allowed.
- **ants, int32 capacity.**
  - `newPool` and `Tune` convert the size to int32 unchecked, so
    `NewPool(1<<32+5)` is a pool of 5.
  - After `Tune(1<<32-2)` the capacity is -2, which is neither a size nor
    the -1 for unlimited, so `Submit` blocks forever.
- The eleventh run's `Tune` bug (a shrink does not limit concurrency) was
  not found. The agent's ants spec only releases its gated tasks with the
  whole pool, so no worker goes idle before a `Tune`. The agent's prompt
  was revised after that bug was known (below), so this rerun on ants was
  not blind; pond was.

Drafts for the two new ants issues wait for review, with the `Tune` one.

## What the tool got wrong, and fixed

- **A spec file that does not build reached the search.** The compile
  stage tolerates type errors (it translates what loads). So MiniMax-M3's
  spec file was accepted with these errors:
  - a variadic parameter before the result;
  - a call of a field;
  - an unused variable.

  Every replay then failed to build. The agent now builds the spec file
  with `go test -c` first and sends the compiler's errors back for repair.
  With the fix, MiniMax-M3 needed three rounds.
- **One dropped connection ended a run.** A proxy reset the connection
  during the second LLM call on pond, and the run, minutes of work, was
  lost. A failed call is now tried twice more.
- **The prompt undersold the model.** It said that goroutines, channels,
  `select` and floats were not modelled, which steers an agent away from
  concurrency specs, and it asked for no stateful sequences. It now lists
  the concurrency the model covers and asks for sequence specs that drive
  a stateful API through its operations, checking the documented contract
  after each step against a reference.

## What this says

The agent works when the model writing the specs is strong. Claude Opus
5.5 read each pool's docs and wrote a few deep sequence specs (gated tasks,
reference counters, conservation). Those found three real defects, two of
them unreported, in two runs of under an hour each. Its reports traced
each one to its cause.

MiniMax-M3, given the same prompt and package, wrote many shallow specs
of one function each. They did not build and took too long to search.

The gap is in what to check, not in the tool: the model's search, the
replay and the fuzzer were the same for both.
