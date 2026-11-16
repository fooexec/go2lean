# Upstream reports: drafts

*Not posted. Collected here to be filed together, and the [blog draft](2026-11-go2lean.md) published, once there are enough. Every finding, reported or not, is listed in [findings](../findings.md).*

Every defect below was found by go2lean and reproduced by hand with a
plain `go test` outside the tool. Each draft is written as the issue body
to paste; none carries tool attribution beyond the last line. Before
filing, check that the project's tracker still has no report, and that the
repository is maintained (not archived). For security issues, use the
project's private channel (`SECURITY.md`, GitHub's private vulnerability
reporting) rather than a public issue.

## Status

| project | defect | severity | where found | status |
|---|---|---|---|---|
| panjf2000/ants | `Tune` to a smaller size does not limit concurrency | medium | [eleventh run](../eval/eleventh-run.md) | draft 1 below |
| panjf2000/ants | `PoolWithFunc.Invoke(nil)` accepted, task dropped | medium | [twelfth run](../eval/twelfth-run.md) | draft 2 below |
| panjf2000/ants | size above `MaxInt32` wraps; `Submit` can block forever | low | twelfth run | draft 3 below |
| hashicorp/go-retryablehttp | `Request.WriteTo` panics on a request with no body | low | [thirteenth run](../eval/thirteenth-run.md) | draft below |
| avast/retry-go | `Unrecoverable` ignored with a custom `RetryIf` and finite attempts; `MaxJitter(-1)` panics; `BackOffDelay(0)` underflows | medium | thirteenth run | draft below |
| grpc/grpc-go | `MaxConnectionAge` below 10 ns or negative panics on the first connection | low | [grpc-go](../eval/grpc.md) | draft below |
| casbin/casbin | `KeyMatch` over-grants a pattern with `*` in the middle | hardening | [specs from advisories](../eval/cve-specs.md) | draft below |
| go-chi/chi | `Profiler` redirects to `//pprof/` at the root; query string in the path | low | specs from advisories | draft below |
| gofiber/fiber (v2) | `BalancerForward` with no servers panics (divide by zero) on the first request; fixed in v3 only | low | specs from advisories | draft below |
| Masterminds/semver | `<=*`, `<=x`, `=<*` accept only `0.0.x`; `!=*` acts like `!= 0.0.0` | medium | [semver](../eval/semver.md) | draft below |
| gobwas/glob | `*{abc,b}*c??` does not match `abcXc`; `[\-]` does not compile; `QuoteMeta` still leaves `,` (#78 closed) | medium | [glob](../eval/glob.md) | draft below |
| gin-contrib/cors | a regexp origin is matched unanchored: the package's own example pattern lets `https://google.com.evil.net` read with credentials; regexps lowercased; wildcard overlap | medium (security) | [advisory specs](../eval/cve-specs.md) | draft below, **private channel** |
| rs/cors | `OriginAllowed` returns true without an `Origin` under `AllowAll`/`"*"`, against the doc #231 wrote | low | advisory specs | draft below |
| magiconair/properties | goroutine leak on every parse error | medium | [seventh run](../eval/seventh-run.md) | filed: magiconair/properties#101 |
| aymerick/raymond, mailgun/raymond | goroutine leak on every template parse error | medium | [lexer leaks](../eval/lexer-leaks.md) | not reportable: both archived |
| Masterminds/semver, dustin/go-humanize | edge cases near size limits | minor | [ninth run](../eval/ninth-run.md) | dropped: too minor |

Found but already reported upstream (not drafted): alitto/pond's subpool
hang (#152), go-retryablehttp's `Retry-After` overflow (PR #297), gocron's
`WithLimitedRuns` overrun (PR #984), gin's `FileAttachment` space as `+`
in `filename*` (open PR #4873), and the chi, echo and fiber advisory
variants in [specs from advisories](../eval/cve-specs.md), all fixed in
their latest releases.

---

## panjf2000/ants

# Draft: issue for panjf2000/ants (not posted)

**Title:** Tune to a smaller size does not limit concurrency: idle workers beyond the new capacity are reused

**Body:**

After `Tune` lowers a pool's capacity, the workers already idle in the
worker queue are still handed out by `Submit`. Until each of them has run
one more task, the pool runs more tasks at once than its capacity, and
`Free()` goes negative. At `c101e30` (v2, default options):

```go
package ants

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestTuneShrinkStillRunsOldCapacity(t *testing.T) {
	p, _ := NewPool(4)
	defer p.Release()

	// Four tasks leave four idle workers in the queue.
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		_ = p.Submit(func() { defer wg.Done(); time.Sleep(10 * time.Millisecond) })
	}
	wg.Wait()
	time.Sleep(10 * time.Millisecond)

	p.Tune(1)
	t.Logf("Cap=%d Running=%d Free=%d", p.Cap(), p.Running(), p.Free()) // Cap=1 Running=4 Free=-3

	var cur, peak int32
	for i := 0; i < 4; i++ {
		wg.Add(1)
		_ = p.Submit(func() {
			defer wg.Done()
			n := atomic.AddInt32(&cur, 1)
			for {
				m := atomic.LoadInt32(&peak)
				if n <= m || atomic.CompareAndSwapInt32(&peak, m, n) {
					break
				}
			}
			time.Sleep(50 * time.Millisecond)
			atomic.AddInt32(&cur, -1)
		})
	}
	wg.Wait()
	if peak > int32(p.Cap()) {
		t.Errorf("%d tasks ran at once in a pool of capacity %d", peak, p.Cap())
	}
}
```

```
Cap=1 Running=4 Free=-3
4 tasks ran at once in a pool of capacity 1
```

It also happens with `WithNonblocking(true)`. With a pool of 1, `Tune(2)`,
two blocked tasks, released, and then `Tune(1)`, the next two `Submit`
calls both succeed and run together, where the second should get
`ErrPoolOverload`.

The cause:
- `Tune` only stores the new capacity.
- `revertWorker` retires a worker when `Running() > Cap()`, but only when
  the worker comes back from a task.
- `retrieveWorker` first takes any worker from the queue (`p.workers.detach()`)
  without looking at the capacity. So the workers that were already idle
  when `Tune` was called each run one more task.

Using `Tune` to throttle a pool (shed load, lower concurrency on errors)
therefore has no effect for the next round of tasks. `Free()`, documented
as "-1 indicates this pool is unlimited", returns negative numbers in the
meantime.

Possible fixes:
- in `Tune`, when the size shrinks, retire idle workers down to the new
  capacity (as `purgeStaleWorkers` does for expired ones);
- or in `retrieveWorker`, check `p.Running() > p.Cap()` before handing out
  a queued worker, and retire it instead.

Related but different: #374 was workers created above the capacity by a
race; this needs no race and happens every time.

Found with go2lean, a tool that checks Go code against a model of its
semantics and replays each finding as a `go test`.

---

# Draft 2: issue for panjf2000/ants (not posted)

**Title:** PoolWithFunc.Invoke(nil) returns nil but the function never runs

**Body:**

`Invoke(nil)` is accepted (it returns `nil`), but the pool function is never
called with it, and the worker that received it exits. At `c101e30`:

```go
func TestInvokeNil(t *testing.T) {
	var calls int32
	p, _ := NewPoolWithFunc(2, func(arg any) { atomic.AddInt32(&calls, 1) })
	defer p.Release()
	_ = p.Invoke(nil)
	_ = p.Invoke(1)
	time.Sleep(100 * time.Millisecond)
	if n := atomic.LoadInt32(&calls); n != 2 {
		t.Errorf("fn called %d times, want 2", n) // 1
	}
}
```

The cause:
- `goWorkerWithFunc.run` reads `for arg := range w.arg` and returns on
  `arg == nil`;
- `nil` is also what `finish()` sends to stop the worker. A caller's nil
  argument is taken as the stop signal.

`PoolWithFuncGeneric` avoids this with a separate `exit` channel. Either
give `goWorkerWithFunc` the same, or have `Invoke` reject `nil` with an
error and say so in its doc. Today the task is dropped silently.

(#167 proposed allowing nil arguments in 2021 and was closed without being
merged. The doc still does not say that nil is not allowed.)

---

# Draft 3: issue for panjf2000/ants (not posted)

**Title:** Pool sizes above MaxInt32 are truncated: NewPool(1<<32+5) has capacity 5, Tune(1<<32-2) gives -2

**Body:**

`newPool` stores `capacity: int32(size)` and `Tune` stores
`atomic.StoreInt32(&p.capacity, int32(size))`, with no range check. A size
that does not fit in an int32 wraps silently:
- `NewPool(1<<32 + 5)` is a pool of capacity 5.
- `Tune(1<<32 - 2)` sets the capacity to -2. That is neither a valid size
  nor the -1 that means unlimited, so `retrieveWorker`'s
  `capacity == -1 || capacity > p.Running()` is always false, and a
  blocking `Submit` waits forever.
- `NewPoolWithFunc(59453308801, ...)` gives a negative capacity, and
  `Invoke` blocks.

```go
func TestSizeTruncated(t *testing.T) {
	p, _ := NewPool(1<<32 + 5)
	if p.Cap() != 1<<32+5 && p.Cap() != math.MaxInt32 {
		t.Errorf("Cap = %d", p.Cap()) // 5
	}
	q, _ := NewPool(10)
	q.Tune(1<<32 - 2)
	if q.Cap() <= 0 && q.Cap() != -1 {
		t.Errorf("Cap after Tune = %d", q.Cap()) // -2
	}
}
```

Sizes this large are unusual, but they come from config or from
arithmetic on `int`, and nothing reports the wrap. A fix is to clamp to
`math.MaxInt32`, or to return an error, in both places.

Found with go2lean, a tool that checks Go code against a model of its
semantics and replays each finding as a `go test`.

---

## hashicorp/go-retryablehttp

# Draft: issue for hashicorp/go-retryablehttp (not posted)

**Title:** Request.WriteTo panics on a request with no body

**Body:**

A request made with a nil body, `NewRequest("GET", url, nil)`, panics in
`WriteTo`. At `fd00458`:

```go
func TestWriteToNilBody(t *testing.T) {
	req, err := retryablehttp.NewRequest("GET", "http://example.com", nil)
	if err != nil {
		t.Fatal(err)
	}
	n, err := req.WriteTo(&bytes.Buffer{}) // panics
	if n != 0 || err != nil {
		t.Errorf("WriteTo = %d, %v; want 0, nil", n, err)
	}
}
```

```
panic: runtime error: invalid memory address or nil pointer dereference
```

`WriteTo` starts with `body, err := r.body()`. For a request without a body,
`r.body` is nil, so the call through it panics. `BodyBytes` guards the same
case (`if r.body == nil { return nil, nil }`); `WriteTo` does not. A GET is
the most common request without a body, and `WriteTo` is how `io.Copy` and
other `io.WriterTo` users read a request.

Possible fix, at the top of `WriteTo`:

```go
if r.body == nil {
	return 0, nil
}
```

Found with go2lean, a tool that checks Go code against a model of its
semantics and replays each finding as a `go test`.

---

## avast/retry-go

# Draft: issue for avast/retry-go (not posted)

**Title:** With a custom RetryIf, Unrecoverable stops Attempts(0) but not a finite Attempts(n)

**Body:**

An error wrapped with `retry.Unrecoverable` stops the retries when
`Attempts(0)` is set, even with a custom `RetryIf`. With a finite attempt
count and the same `RetryIf`, the error is retried until the attempts run
out. At `360fd45` (v5):

```go
func TestUnrecoverableWithRetryIf(t *testing.T) {
	for _, attempts := range []uint{0, 3} {
		calls := 0
		_ = retry.New(retry.Attempts(attempts), retry.Delay(time.Millisecond),
			retry.RetryIf(func(error) bool { return true })).
			Do(func() error {
				calls++
				if calls > 5 {
					return nil
				}
				return retry.Unrecoverable(errors.New("fatal"))
			})
		t.Logf("Attempts(%d): the function ran %d times", attempts, calls)
	}
}
```

```
Attempts(0): the function ran 1 times
Attempts(3): the function ran 3 times
```

In `doWithData`:
- the `Attempts(0)` loop checks `if !IsRecoverable(err)` before
  `r.retryIf(err)` (retry.go:206);
- the finite loop (`shouldRetry:`, retry.go:258) checks only
  `r.retryIf(err)`.

So `Unrecoverable` works on the finite path only through the default
`RetryIf`, which is `IsRecoverable`. Any custom `RetryIf` turns it off
there. `RetryIf`'s doc says "By default RetryIf stops execution if the error
is wrapped using `retry.Unrecoverable`". Whichever behaviour is intended
for a custom `RetryIf`, the two paths should agree. If `Unrecoverable`
should always win, add the same check to the finite loop:

```go
if !IsRecoverable(err) || !r.retryIf(err) {
```

Two smaller things found the same way:
- `MaxJitter(-1)` panics on the first retry (`invalid argument to Int63n`).
  The default delay type includes `RandomDelay`, which guards only
  `maxJitter == 0`. `if maxJitter <= 0 { return 0 }` would fix it.
- `BackOffDelay(0, ...)` underflows `n--` on a `uint` and returns the
  largest delay (about 218 years for a 100 ms delay), while
  `BackOffDelay(1, ...)` returns 100 ms. `Do` never passes 0, but
  `BackOffDelay` is exported as a `DelayTypeFunc`, and `FullJitterBackoffDelay`
  counts from 0. The `n--` came with #130. `if n > 0 { n-- }` would fix it.

Found with go2lean, a tool that checks Go code against a model of its
semantics and replays each finding as a `go test`.

---

## grpc/grpc-go

# Draft: issue for grpc/grpc-go (not posted)

Title: server: MaxConnectionAge below 10ns or negative panics in getJitter on the first accepted connection

### What version of gRPC are you using?

master at 70b8760 (1.86.0-dev)

### What version of Go are you using (`go version`)?

go1.26.6

### What operating system (Linux, Windows, …) and version?

Linux amd64

### What did you do?

Started a server with a small or negative `MaxConnectionAge` and connected a client:

```go
lis, _ := net.Listen("tcp", "127.0.0.1:0")
s := grpc.NewServer(grpc.KeepaliveParams(keepalive.ServerParameters{
	MaxConnectionAge: 5 * time.Nanosecond, // or any negative duration
}))
healthpb.RegisterHealthServer(s, health.NewServer())
go s.Serve(lis)

cc, _ := grpc.NewClient(lis.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
healthpb.NewHealthClient(cc).Check(ctx, &healthpb.HealthCheckRequest{})
```

### What did you expect to see?

Either the value is rejected or clamped by `KeepaliveParams`, or the
connection is closed after the (tiny) age. `KeepaliveParams` already clamps
`Time` to a minimum.

### What did you see instead?

The server process crashes when it accepts the first connection:

```
panic: invalid argument to Int64N

goroutine 34 [running]:
math/rand/v2.Int64N(...)
google.golang.org/grpc/internal/transport.getJitter(...)
	internal/transport/http2_server.go:1514
google.golang.org/grpc/internal/transport.NewServerTransport(...)
	internal/transport/http2_server.go:234
google.golang.org/grpc.(*Server).newHTTP2Transport(...)
	server.go:1054
google.golang.org/grpc.(*Server).handleRawConn(...)
	server.go:1011
```

`NewServerTransport` replaces only a zero `MaxConnectionAge` with the
default, then calls `getJitter(kp.MaxConnectionAge)`:

```go
r := int64(v / 10)
j := rand.Int64N(2*r) - r
```

For `0 < v < 10ns`, `r == 0`; for `v < 0`, `2*r < 0`. `rand.Int64N` panics
for `n <= 0`. The panic is on the goroutine serving the accepted
connection, so it takes down the whole process.

A possible fix is to return 0 from `getJitter` when `r <= 0`, and/or to
validate `MaxConnectionAge` (and `MaxConnectionAgeGrace`) in
`KeepaliveParams`.

---

## casbin/casbin

# Draft: issue for casbin/casbin (now apache/casbin) (not posted)

**Title:** KeyMatch ignores everything after the first `*`: `/foo/*/edit` also matches `/foo/x/delete`

**Body:**

`util.KeyMatch` (the `keyMatch` matcher function) compares only the part of
the pattern before its first `*`. A pattern with `*` in the middle therefore
matches far more than it reads. At `c157de4`:

```go
func TestKeyMatchMiddleStar(t *testing.T) {
	if util.KeyMatch("/foo/x/delete", "/foo/*/edit") {
		t.Error(`KeyMatch("/foo/x/delete", "/foo/*/edit") = true`)
	}
	if !util.KeyMatch2("/foo/x/edit", "/foo/*/edit") || util.KeyMatch2("/foo/x/delete", "/foo/*/edit") {
		t.Error("KeyMatch2 for comparison")
	}
}
```

`KeyMatch("/foo/x/delete", "/foo/*/edit")` is `true`; `KeyMatch2` answers
`false`, as the pattern reads.

In an authorization policy this is an over-grant: a rule
`p, alice, /foo/*/edit, POST` with `keyMatch` also allows
`/foo/anything/delete`. The function's comment and the docs
(casbin.apache.org/docs/function) only show `*` at the end
(`/alice_data/*`) and do not say what a `*` elsewhere means, so a policy
author can easily read `/foo/*/edit` as "any one segment".

Possible fixes, from least to most behaviour change:
- document that `keyMatch` treats everything from the first `*` on as
  "anything", and recommend `keyMatch2` for patterns with `*` inside;
- have `KeyMatch` return false (or `KeyMatchFunc` an error) for a pattern
  with a `*` that is not last;
- match the text after the `*` as a suffix.

Found with go2lean, a tool that checks Go code against a model of its
semantics and replays each finding as a `go test`.

---

## go-chi/chi

# Draft: issue for go-chi/chi (not posted)

**Title:** middleware.Profiler: redirect to `//pprof/` when mounted at the root, and the query string ends up in the path

**Body:**

`Profiler`'s index redirect builds its target from the raw `RequestURI`:
`http.Redirect(w, r, r.RequestURI+"/pprof/", 301)`. At `c64ed7d`:
- mounted at the root (`r.Mount("/", middleware.Profiler())`), a request
  for `/` redirects to `//pprof/`, which browsers read as a
  protocol-relative URL to the host `pprof`;
- with a query string, `/debug?x=1` redirects to `/debug?x=1/pprof/`: the
  suffix lands inside the query.

```go
func TestProfilerRedirect(t *testing.T) {
	r := chi.NewRouter()
	r.Mount("/", middleware.Profiler())
	for _, target := range []string{"/", "/?x=1"} {
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest("GET", target, nil))
		t.Logf("%s -> %d %q", target, w.Code, w.Header().Get("Location"))
	}
}
```

```
/            -> 301 "//pprof/"
/?x=1        -> 301 "/?x=1/pprof/"
```

Mounted at `/debug`, `/debug?x=1` likewise redirects to `/debug?x=1/pprof/`.

The redirect target is not attacker-chosen (`pprof`), so this is low
severity, but `Location` should stay a same-site path. Building it from the
path only fixes both, e.g.
`strings.TrimRight(r.URL.EscapedPath(), "/") + "/pprof/"`, plus the query
if wanted.

Found with go2lean, a tool that checks Go code against a model of its
semantics and replays each finding as a `go test`.

---

## gofiber/fiber

# Draft: issue for gofiber/fiber, v2 branch (not posted)

**Title:** v2: `proxy.BalancerForward` with an empty server list panics on the first request

**Body:**

On v2 (v2.52.16 and the `v2` branch at `9407e6e`),
`proxy.BalancerForward(nil)` or `BalancerForward([]string{})` returns a
handler without complaint. That handler's first request panics with
`integer divide by zero` in `roundrobin.get`:

```go
if r.current >= len(r.pool) {
	r.current %= len(r.pool) // len(r.pool) == 0
}
```

```go
func TestBalancerForwardEmpty(t *testing.T) {
	app := fiber.New()
	app.Use(proxy.BalancerForward(nil))
	req := httptest.NewRequest("GET", "/", nil)
	_, _ = app.Test(req) // panic: runtime error: integer divide by zero
}
```

`proxy.Balancer` rejects empty `Servers` when it is built (`configDefault`
panics with "Servers cannot be empty"). v3 gave `BalancerForward` the same
check in `08a4fb35` (June 2026), but it was not backported. With an empty
list coming from configuration (an environment variable left unset), a v2
service builds fine and then crashes on its first request instead of at
startup. Possible fix: the v3 check, at the top of `BalancerForward`:

```go
if len(servers) == 0 {
	panic("Servers cannot be empty")
}
```

Found with go2lean, a tool that checks Go code against a model of its
semantics and replays each finding as a `go test`.

---

## Masterminds/semver

# Draft: issue for Masterminds/semver (not posted)

**Title:** `<=*` (and `<=x`, `=<*`) accepts only `0.0.x`, not every version

**Body:**

The README's "Wildcards In Comparisons" says the wildcard "works for all
comparison operators", with `<= 2.x` equivalent to `< 3` and `*`
equivalent to `>= 0.0.0`. By the same rule `<=*` allows every version.
It accepts only `0.0.x`. At `dd2b995`:

```go
func TestLeWildcard(t *testing.T) {
	for _, c := range []string{"<=*", "<=x", "=<*", "<=1.x"} {
		cs, _ := semver.NewConstraint(c)
		var out []string
		for _, v := range []string{"0.0.0", "0.0.5", "0.1.0", "1.2.3", "2.0.0"} {
			if cs.Check(semver.MustParse(v)) {
				out = append(out, v)
			}
		}
		t.Logf("%-6q accepts %v", c, out)
	}
}
```

```
"<=*"  accepts [0.0.0 0.0.5]
"<=x"  accepts [0.0.0 0.0.5]
"=<*"  accepts [0.0.0 0.0.5]
"<=1.x" accepts [0.0.0 0.0.5 0.1.0 1.2.3]
```

The wildcard major is parsed as `0.0.0` with the minor and patch dirty,
and `constraintLessThanEqual` then compares the major and the minor, so
only versions below `0.1.0` pass. `<=1.x` works because its major is not
a wildcard. The caret fixes for #317 (`^*`) touch `constraintCaret` only.
A fix: in `constraintLessThanEqual`, accept every version when the
constraint's major is a wildcard, as `<= X.x` already does for the minor.

A smaller one of the same kind: `!=*` and `!=X` behave like `!= 0.0.0`
(they accept `1.2.3` and reject `0.0.0`), where "not any version" would
reject everything.

Found with go2lean, a tool that checks Go code against a model of its
semantics and replays each finding as a `go test`.

---

## gobwas/glob

# Draft: issue for gobwas/glob (not posted)

**Title:** `*{abc,b}*c??` does not match `abcXc`; `[\-]` does not compile

**Body:**

Two defects in v1 at `ff0540d`, found while checking `Match` against the
regexp a glob translates to:

```go
func TestRepro(t *testing.T) {
	g := glob.MustCompile("*{abc,b}*c??")
	t.Log(g.Match("abcXc"))                                        // false
	t.Log(regexp.MustCompile(`^.*(?:abc|b).*c..$`).MatchString("abcXc")) // true
	_, err := glob.Compile(`[\-]`)
	t.Log(err) // glob: syntax error at 4: unexpected end of input
}
```

1. **A wrong match.** `abcXc` is `b` + `cXc`: the first `*` takes `a`, the
   alternative takes `b`, the second `*` takes nothing, then `c??` matches
   `cXc`. When the second star finds its literal, `storeStar` cuts the
   restart points back to the root's `starsFloor` and drops the first
   star's. Resuming the first star with the shorter alternative would reach
   the second star earlier, which the reset assumes cannot happen, so the
   match fails.
2. **An escaped `-` in a class does not compile.** The grammar says `\c`
   matches `c` inside a class and that `-` is literal in a set, but
   `fetchRange` peeks for `-` after reading the `\` and takes the `\` as
   the low end of a range.

Also: #78 (`QuoteMeta` and `,`) was closed on 2026-10-26, but at
`ff0540d` `QuoteMeta("a,b")` is still `"a,b"`, so
`"{" + QuoteMeta("a,b") + ",c}"` matches `a` and `c`, not `a,b`.

Smaller ones, if useful:
- a `}` outside a group compiles as a literal (`a}b`), though the grammar
  excludes `}` from literals;
- with a separator that is not a valid rune, `*` treats it as U+FFFD and
  `?` does not;
- the lexer replaces an invalid UTF-8 error with a later one;
- `Lexer.Offset` after `[` is the end of the class, not as documented.

Found with go2lean, a tool that checks Go code against a model of its
semantics and replays each finding as a `go test`.

---

## gin-contrib/cors

# Draft: security report for gin-contrib/cors (not posted)

*The repository has no `SECURITY.md`. Use GitHub's private vulnerability
reporting if it is enabled (Security tab, "Report a vulnerability"), or ask
a maintainer for a private contact, rather than a public issue.*

**Title:** Regexp origins are matched unanchored: a pattern for `google.com` also allows `https://google.com.evil.net`, with credentials

**Body:**

An `AllowOrigins` entry written `/.../` (added in #159) is compiled and
matched with `regexp.MatchString`, which finds the pattern anywhere in the
origin (`validateOrigin`, config.go). Unless the user writes `^...$`, a
pattern for a trusted domain also matches origins that only contain it.
With the pattern from the package's own tests, at `b319528`:

```go
func TestRegexOriginUnanchored(t *testing.T) {
	r := gin.New()
	r.Use(cors.New(cors.Config{
		AllowOrigins:     []string{`/https?://(?:.+\.)?google\.com/g`},
		AllowCredentials: true,
	}))
	r.GET("/", func(c *gin.Context) { c.String(200, "secret") })
	for _, o := range []string{"https://mail.google.com", "https://google.com.evil.net", "https://notgoogle.com"} {
		req := httptest.NewRequest("GET", "/", nil)
		req.Header.Set("Origin", o)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		t.Logf("%-30s ACAO=%q ACAC=%q", o, w.Header().Get("Access-Control-Allow-Origin"), w.Header().Get("Access-Control-Allow-Credentials"))
	}
}
```

```
https://mail.google.com        ACAO="https://mail.google.com" ACAC="true"
https://google.com.evil.net    ACAO="https://google.com.evil.net" ACAC="true"
https://notgoogle.com          ACAO="" ACAC=""
```

Anyone who owns `evil.net` can serve a page from `google.com.evil.net`
and read credentialed responses cross-origin. The README does not
mention regexp origins, so nothing tells users to anchor them, and the
example in the tests is not anchored.

Possible fix: compile each regexp entry once in `newCors`, anchored as
`"^(?:" + pattern + ")$"`. An entry that is already anchored still
compiles to the same language.

Two smaller defects in the same code:
- `newCors` lowercases every `AllowOrigins` entry, regexps included, so
  `\S` becomes `\s` and `/https://\S+\.example\.com/` rejects
  `https://a.example.com`. Regexp entries should keep their case.
- `validateWildcardOrigin` checks only `HasPrefix(origin, prefix) &&
  HasSuffix(origin, suffix)`, so the two may overlap:
  `https://a*a.example.com` allows `https://a.example.com`. Adding
  `len(origin) >= len(prefix)+len(suffix)` fixes it.

Found with go2lean, a tool that checks Go code against a model of its
semantics and replays each finding as a `go test`.

---

## rs/cors

# Draft: issue for rs/cors (not posted)

**Title:** `OriginAllowed` returns true for a request without an `Origin` header when all origins are allowed

**Body:**

Since #231 (fixing #230), the doc of `OriginAllowed` says: "A request
without an Origin header has no origin to match, so this reports false.
Use it where an Origin is expected, such as Gorilla WebSocket's
CheckOrigin". That holds for a list of origins, but with `Default()`,
`AllowAll()` or an `AllowedOrigins` entry `"*"`, it returns true. At
`7ca5fe2`:

```go
r := httptest.NewRequest("GET", "/", nil) // no Origin
cors.Default().OriginAllowed(r)                                          // true
cors.AllowAll().OriginAllowed(r)                                         // true
cors.New(cors.Options{AllowedOrigins: []string{"*"}}).OriginAllowed(r)   // true
cors.New(cors.Options{AllowedOrigins: []string{"https://a.com"}}).OriginAllowed(r) // false
```

`OriginAllowed` passes the empty origin to `isOriginAllowed`, which
returns true early when `allowedOriginsAll` is set. A fix:

```go
origin := r.Header.Get("Origin")
if origin == "" {
	return false
}
```

Found with go2lean, a tool that checks Go code against a model of its
semantics and replays each finding as a `go test`.
