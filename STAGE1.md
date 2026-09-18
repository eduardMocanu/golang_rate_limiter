# Stage 1 — In-Memory Rate Limiter

Goal: a correct, concurrency-safe token bucket rate limiter exposed as HTTP
middleware. Standard library only — no external dependencies.

Build it all in `cmd/server/main.go` first. Split into packages at step 7, once the shape
is obvious. Each step must compile and run before moving on.

---

## The algorithm: token bucket

A bucket holds up to `capacity` tokens and refills at `refillRate` tokens per
second. Each request spends one token. No tokens, no service.

The key trick: **never run a background refill goroutine.** Store `tokens` and
`lastRefill`, and compute the refill lazily whenever the bucket is touched.

The order of operations inside `Allow` is not negotiable:

1. **Refill** — add `elapsed * refillRate` tokens, clamped to `capacity`,
   and advance `lastRefill`.
2. **Check** — is there at least 1 token?
3. **Consume** — subtract 1 *only if* the check passed.

Getting this order wrong is the single most common bug in the whole project.

---

## Step 1 — One bucket, single-threaded

**Build:** a `Bucket` struct with `tokens`, `lastRefill`, `capacity`,
`refillRate`, and an `Allow() bool` method. Drive it from `main` with a
`time.Sleep` loop and print the decisions.

**Fix from the current draft:**

- [ ] Advance `lastRefill` when refilling — otherwise elapsed time is measured
      from creation forever and tokens are re-added on every call.
- [ ] Reorder to refill → check → consume. A denied request must not decrement.
- [ ] Guard with `tokens >= 1` *before* spending, not `tokens > 0` after.
- [ ] Make `tokens` a `float64` so partial refills are not truncated away.
      With integer tokens and a `refillRate` below 1, every refill under a full
      second rounds to zero and the rate silently collapses.
- [ ] Start the bucket full (`tokens = capacity`) unless you deliberately want
      a cold start.

**Teaches:** structs, pointer vs value receivers (a value receiver here means
your mutations silently vanish — worth triggering once on purpose), `time`.

**Done when:** a bucket with capacity 5 and rate 1/s allows 5 immediate calls,
denies the 6th, and allows one more after a one-second sleep.

---

## Step 2 — Make it testable

**Build:** change the signature to `Allow(now time.Time) bool` and pass the
clock in from the caller. Write `main_test.go` with table-driven cases.

The point is not the tests themselves — it is that a function calling
`time.Now()` internally cannot be tested without real sleeps. Injecting the
clock makes 10 minutes of simulated time take microseconds.

**Teaches:** `testing`, table-driven tests, dependency injection as the thing
that makes code testable.

**Done when:** `go test` passes and no test sleeps.

Cases worth covering: burst exhaustion, refill after elapsed time, clamping at
capacity (idle for an hour should not bank 3600 tokens), fractional refill
accumulating correctly across several small steps.

---

## Step 3 — Many buckets

**Build:** a `Limiter` struct wrapping `map[string]*Bucket`, with
`Allow(key string, now time.Time) bool` that creates the bucket on first sight
of a key.

**Teaches:** maps, the comma-ok idiom (`b, ok := m[key]`), zero values, and why
the map stores `*Bucket` rather than `Bucket` — map values are not addressable,
so a value map makes mutation awkward on purpose.

**Done when:** two different keys have fully independent budgets.

---

## Step 4 — Make it concurrency-safe

**Build:** add a `sync.Mutex` to `Limiter`; lock around the map access and the
bucket mutation. Write a test firing 100 goroutines at one key with a
`sync.WaitGroup`, counting allows.

**Do this first:** run `go test -race` *before* adding the mutex. Watching the
detector name the exact racing lines is the most instructive 30 seconds in the
project.

**Teaches:** goroutines, `sync.WaitGroup`, `sync.Mutex`, `defer mu.Unlock()`,
and the race detector.

**Done when:** `go test -race` is green, and 100 concurrent calls against a
bucket with capacity 10 allow exactly 10 — not 11, not 9.

---

## Step 5 — Expose it over HTTP

**Build:** an `http.HandleFunc` reading the key from a `?user=` query param.
Denied requests return `429 Too Many Requests`.

**Teaches:** `net/http`, handlers, request parsing, status codes.

**Done when:** `for i in $(seq 1 20); do curl -s -o /dev/null -w "%{http_code}\n" \
"localhost:8080/?user=alice"; done` shows 200s turning into 429s.

---

## Step 6 — Turn it into middleware

**Build:** rewrite step 5 as `func RateLimit(l *Limiter) func(http.Handler) http.Handler`,
wrapping any handler. The key extractor should itself be a
`func(*http.Request) string` passed in — do not hardcode where the key comes
from.

Add the response headers a real limiter owes its callers:
`Retry-After`, `X-RateLimit-Limit`, `X-RateLimit-Remaining`. Returning a bare
`bool` no longer suffices — introduce a `Decision` struct carrying
`Allowed`, `Remaining`, and `RetryAfter`.

Test with `net/http/httptest` — no real server, no real port.

**Teaches:** interfaces, closures, the middleware pattern. This is the most
idiomatic pattern in Go web code and the point where interfaces usually click.

**Done when:** the same middleware wraps two different handlers without changes,
and `httptest` asserts both the status code and the headers.

---

## Step 7 — Refactor into packages

**Build:**

```
cmd/server/main.go        wiring only, no logic
internal/limiter/         limiter.go (interface + Decision), memory.go, clock.go
internal/httpmw/          ratelimit.go
```

Define the interface *now* that you know what it should be:

```go
type Limiter interface {
    Allow(ctx context.Context, key string) (Decision, error)
}
```

`ctx` and `error` look like dead weight for an in-memory limiter. They are the
seam for Stage 2, where the limiter talks to Redis over a network that can be
slow or down. Adding them later means touching every call site.

`internal/` means nothing outside this module can import the package — the
compiler enforces "this is not a public API yet".

**Teaches:** package layout, exported vs unexported identifiers, import cycles,
designing an interface from a known implementation rather than guessing upfront.

**Done when:** `go build ./...` and `go test -race ./...` both pass, and
`cmd/server/main.go` contains no rate-limiting logic.

---

## Step 8 — Eviction (do not skip)

The map grows forever, keyed by whatever the caller sends. That is a memory leak
driven by attacker-controlled input.

**Build:** a goroutine with a `time.Ticker` sweeping buckets untouched for more
than N minutes, plus a `Close()` method that stops it. A bucket idle long enough
to be full is indistinguishable from a fresh one, so dropping it is safe.

**Teaches:** goroutine lifecycle, `time.Ticker`, `select`, stop channels, and
the discipline that whoever starts a goroutine is responsible for ending it.

**Done when:** a test inserting 1000 keys, advancing the clock, and triggering a
sweep leaves the map empty — and `Close()` leaves no goroutine running.

---

## Step 9 — Benchmark, then optimize

**Build:** `BenchmarkAllow` using `b.RunParallel`. Record the number. *Then*
shard the map — `shards[fnv32(key) % 256]`, each with its own mutex — and
compare.

One global mutex serializes every request in the process. Sharding removes the
contention without changing behaviour.

**Teaches:** `testing.B`, `RunParallel`, `-benchmem`, hashing, lock granularity,
and the habit of measuring before optimizing.

**Done when:** you can state the before/after ns/op and allocs/op, and every
test from earlier steps still passes unchanged. If the tests needed edits, the
abstraction leaked.

---

## Optional — a second algorithm

Implement fixed-window or sliding-window counter behind the same `Limiter`
interface, selected by config in `cmd/server/main.go`.

This is the real test of step 7: if the existing tests and middleware work
against the new implementation untouched, the abstraction was right.

---

## Stage 1 is done when

- `go test -race ./...` is green
- the server returns clean `429`s with correct headers under a `curl` loop
- the limiter holds under concurrent load with an exact, provable allow count
- memory is bounded under a flood of distinct keys
- swapping the algorithm touches only `cmd/server/main.go`

That last point is what makes Stage 2 (Redis-backed, distributed) a small change
instead of a rewrite.

---

## Housekeeping

- [ ] Rename the module: `first_GO` is not idiomatic. Go module names are
      lowercase with no underscores — use `ratelimiter` or
      `github.com/<you>/ratelimiter`.
- [ ] Add a `.gitignore` for `.idea/` and build output.
- [ ] Commit at the end of every step. Each step is a clean, reviewable commit.
