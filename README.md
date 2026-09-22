# Rate Limiter

A token-bucket rate limiter in Go, as HTTP middleware, with two interchangeable
backends: an in-memory one for a single server, and a Redis-backed one that
shares a single budget across many servers.

My first Go project.

---

## What it does

Every caller gets a bucket holding up to N tokens. Each request spends one.
Tokens refill at a fixed rate. No tokens, no service — the request gets a
`429 Too Many Requests` instead of reaching the handler.

Defaults: capacity 10, refilling at 0.2 tokens/second. So a caller can burst 10
requests immediately, then gets one more every 5 seconds, and an idle bucket is
full again after 50 seconds.

The caller is identified by `?user=` if present, otherwise by client IP.

### Why a token bucket

It allows bursts without allowing sustained abuse, which is usually what you
actually want, and it stores only two numbers per caller regardless of traffic.

The trick that makes it cheap: there is no background process topping buckets
up. Each bucket stores `tokens` and `last_refill`, and the refill is computed
lazily whenever the bucket is touched:

```
elapsed = now - last_refill
tokens  = min(capacity, tokens + elapsed * refill_rate)
```

Three steps, and the order is not negotiable: **refill, then check, then
consume — and only consume if the check passed.** Almost every bug in this
project was a variation on getting that wrong.

---

## Layout

```
cmd/server/         starts everything and picks which pieces to use
internal/httpmw/    HTTP: key extraction, middleware, status codes
internal/limiter/   the decision: token bucket, memory and Redis versions
```

Dependencies point one way: `main` knows everyone, `httpmw` knows `limiter`,
`limiter` knows nobody. The limiter never mentions HTTP, so it could just as
easily throttle a queue consumer or your own outbound API calls.

`httpmw` declares a one-method interface describing what it needs:

```go
type Limiter interface {
    Allow(ctx context.Context, key string) (bool, error)
}
```

Both implementations satisfy it, so swapping memory for Redis is a one-line
change in `main` and nothing else moves.

---

## Running it

### Locally

Needs a Redis on `localhost:6379`:

```
brew services start redis
go run ./cmd/server
```

### With Docker

```
docker compose up --build
```

Two servers on ports 8081 and 8082, one Redis, one shared budget.

### Trying it

```bash
# burn through the bucket
for i in $(seq 1 12); do
  curl -s -o /dev/null -w "%{http_code} " "localhost:8080/?user=alice"
done; echo
# 200 200 200 200 200 200 200 200 200 200 429 429

# a different user is unaffected
curl -s -o /dev/null -w "%{http_code}\n" "localhost:8080/?user=bob"   # 200
```

The point of the Docker setup is that the budget is **shared**. Alternate
between the two servers and they draw from the same 10 tokens:

```bash
for i in $(seq 1 12); do
  port=$(( 8081 + i % 2 ))
  curl -s -o /dev/null -w "%{http_code} " "localhost:$port/?user=carol"
done; echo
```

Without Redis those would be two independent limiters and all 12 would succeed.

### When Redis is down

```
brew services stop redis   # or: docker compose stop redis
```

Every request gets `503 Service Unavailable`. That is a deliberate
**fail-closed** choice: if the limiter cannot do its job, nothing gets through.
The alternative is fail-open — let everyone past unprotected — which keeps the
site up but drops the protection exactly when something is already wrong.
Neither is right in general. This one picks safety over availability.

---

## How the distributed version works

Three servers each allowing 10 requests means the real limit is 30. The state
has to live outside the process, so it lives in Redis — two fields in a hash,
with a TTL so idle buckets clean themselves up.

The catch is that moving state to Redis reintroduces a race no mutex can fix:

```
read tokens        <- server A reads 1
                   <- server B reads 1 (same instant)
compute
write tokens       <- both allow. one token spent twice.
```

A `sync.Mutex` only coordinates goroutines inside one process, and `go
test -race` cannot see this at all, because the race is in Redis rather than in
Go memory.

The fix is to stop doing the three steps from Go and hand all three to Redis as
a Lua script. Redis executes scripts atomically, so nothing can interleave
between the read and the write. Same algorithm, same arithmetic — the only thing
that changed is where it runs.

Two details worth noting:

- **`EXPIRE` replaced the entire eviction system.** The in-memory version needs
  a background goroutine, a ticker, a quit channel and a `Close()` method to
  stop unused buckets accumulating forever. In Redis that is one line, because a
  bucket idle long enough to be full is indistinguishable from a fresh one.
- **Every call has a 50ms deadline**, derived from the request's own context. So
  a slow Redis cannot hang a request, and a client that disconnects cancels the
  Redis call on its way out.

---

## What building it was like

I had just finished the Tour of Go. I knew the syntax and none of the habits.

The whole project was written in small steps — write something, break it, find
out why. That turned out to be the useful part, because nearly every bug was a
real Go or distributed-systems lesson wearing a disguise.

**The token maths took four tries.** The first version never updated
`last_refill`, so it measured elapsed time from the bucket's creation forever and
re-granted the same tokens on every call — at one token per second the bucket
gained tokens *quadratically* and the limiter simply stopped limiting. It also
spent a token before checking whether it had one, so denied requests pushed the
count negative and a persistent client dug a hole it could never climb out of.
Then an off-by-one (`> 1` instead of `>= 1`) meant a capacity-1 bucket could
never allow anything. Then integer truncation silently ate every fractional
refill below one whole token.

**Reading the clock twice cost a token.** `time.Since(lastRefill)` and
`time.Now()` in the same function are two different instants. Eighty-three
nanoseconds of gap made `elapsed` negative and turned 3 tokens into
2.99999998 — so three allowed requests became two. An operation that reasons
about time should sample the clock exactly once, and then it is worth passing
that instant in as a parameter so tests can simulate an hour in a microsecond.

**Concurrency crashed the process, not just the numbers.** A hundred goroutines
against one map produced `fatal error: concurrent map writes` — not a panic, not
recoverable, the whole program gone. Running the race detector *before* adding
the mutex, and reading its output, explained more about shared memory than any
amount of reading would have.

**The background sweeper was wrong in three separate ways at once.** It was
never called. Its condition compared a token count that nothing updated while
the bucket sat idle, so it could essentially never fire. And its `select` had a
single case, which meant the goroutine could never be told to stop — the
deferred `ticker.Stop()` was unreachable code.

**Twice, code was written correctly and then not wired up.** The middleware got
built and the server was handed the unwrapped mux. The sweeper goroutine got
started on the line after `ListenAndServe`, which blocks forever. Both compiled
cleanly, because to the type system a wrapped handler and a bare one are the
same thing.

**Redis made an old bug new again.** The read-modify-write race from the
in-memory version came straight back the moment state moved out of the process —
except this time no mutex could fix it and the race detector was blind to it.
That is the thing this project was really for.

Things that clicked late: pointer receivers (a value receiver silently discards
your mutations), closures as the reason middleware works at all, interfaces
being about substitution rather than protection, and that a package boundary is
a decision about *what other people can reach*, not a folder for tidiness.

---

## What is missing

- **Tests.** The design is testable — the clock is injectable, `sweep` takes a
  time and returns a count — but the tests were never written. This is the
  biggest gap.
- **No `Decision` type.** `Allow` returns a bare `bool`, so responses cannot
  carry `Retry-After` or `X-RateLimit-Remaining` headers. Clients have to guess
  when to retry.
- **No metrics.** No visibility into how often the limiter denies, or why.
- **No circuit breaker.** When Redis is down every request fails. The in-memory
  limiter is right there and could serve as a degraded fallback.
- **One global limit** for every caller and every endpoint, compiled in. No
  tiers, no per-route configuration.
- **Sharding.** The in-memory version serializes every request through one
  mutex. Fine at this scale, the first thing to fix at a larger one.
