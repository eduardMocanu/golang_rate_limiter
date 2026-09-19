# Stage 2 — Distributed Rate Limiter (Redis)

Prerequisite: Stage 1 complete. Steps 1–8 done; step 9 optional but useful.

Goal: a rate limiter whose budget is shared across multiple server replicas,
correct under concurrency, and safe when Redis is unavailable.

---

## The problem being solved

Three replicas each allowing 100 req/s means your "100/s" limit is really
300/s. The state has to leave the process.

But moving state to Redis reintroduces the Step 4 race in a form no mutex can
fix and `-race` cannot see:

```
tokens = GET key          <- replica A reads 1
                          <- replica B reads 1 (same instant)
...compute refill...
SET key newTokens         <- both allow, both write. One token spent twice.
```

A `sync.Mutex` only coordinates goroutines inside one process. Three processes
have no shared mutex. **The read-modify-write must be atomic on the shared
store** — that single sentence is what Stage 2 teaches.

Second, quieter problem: Redis is now on the request path. It can be slow, it
can be down, and neither may take your service with it.

---

## Step 0 — Environment

**Build:**

```
docker run --rm -p 6379:6379 redis:7-alpine
go get github.com/redis/go-redis/v9
```

Connect with `redis.NewClient(&redis.Options{Addr: "localhost:6379"})` and
verify with `client.Ping(ctx)`.

Learn `redis-cli` alongside it — `GET`, `TTL`, `MONITOR`. `MONITOR` prints every
command the server receives and is the fastest way to see what your code is
actually sending.

**Done when:** a Go program prints `PONG`.

---

## Step 1 — Token bucket in Redis, the naive way

**Build:** the same algorithm you already have, with the *state* moved to Redis
and the math still in Go.

State per key is two numbers, which fits a Redis hash:

```
HSET rl:alice tokens 2.4 last_refill 1758300000.123
```

The flow:

1. `HMGET key tokens last_refill` — missing key means a fresh full bucket, the
   same "create on first sight" branch the in-memory version already has.
2. Compute — refill, check, consume. This is `bucket.allow` unchanged.
3. `HSET` the new values and `EXPIRE` the key, in a `TxPipeline` so it is one
   round trip.

**TTL:** `capacity / refillRate` seconds plus a margin. A bucket idle that long
is full, and a full bucket is indistinguishable from a fresh one — the same rule
that justified eviction in Stage 1, now enforced by Redis instead of a sweeper
goroutine.

**Gotchas:**

- Redis returns strings; `HMGET` gives `[]interface{}` with `nil` for missing
  fields. Parse carefully.
- On error, a zero count or zero tokens looks like a valid answer. Check `err`
  first, always.
- Namespace keys (`rl:<key>`) — never use raw user input as a bare key.

**Done when:** a single server enforces the limit using Redis instead of the
map, and `redis-cli HGETALL rl:alice` plus `TTL rl:alice` show sensible values.

Worth thirty seconds in `redis-cli` before moving on: run `INCR foo` a few
times. That command is atomic on the server — no read-modify-write on your side.
Contrast it with the three-step flow above, which is not. That contrast is the
whole of Step 2.

---

## Step 2 — Prove it races

**Build:** two `RedisLimiter` values with two separate clients — no shared Go
memory, so only Redis can be coordinating them. Hammer one key concurrently with
capacity 10 and count the allows.

You will get more than 10 through. Between the `HMGET` and the `HSET`, the other
instance reads the same value: both see `tokens = 1`, both allow, both write.
One token spent twice.

This is the Step 4 race from Stage 1, with two differences that matter:

- A `sync.Mutex` cannot fix it. Two processes share no mutex.
- `-race` cannot see it. The race is in Redis, not in Go memory.

**Done when:** you have reproduced over-admission, and can state why neither the
mutex nor the race detector helps.

---

## Step 3 — Refactor the signature

Crossing a network changes the contract. Do this before writing more Redis code.

```go
Allow(key string, now time.Time) bool
    ->
Allow(ctx context.Context, key string) (Decision, error)
```

- **`ctx`** carries the deadline and cancellation for the network call.
- **`error`** is new: the call can now fail in ways that are not "denied".
  "Allowed", "denied" and "broken" are three different outcomes and callers
  must distinguish them.
- **`now` disappears from the signature.** The clock moves to Redis (Step 5).
  The in-memory implementation keeps an internal clock field so its tests stay
  fast.

This is also where the **interface finally earns its place** — you now have two
implementations. Declare it in the consumer (`httpmw`), listing only the methods
that package uses. The `limiter` package should not import `httpmw`.

**Done when:** the in-memory limiter satisfies the new interface, existing
behaviour is unchanged, and `main` still compiles with one implementation.

---

## Step 4 — Token bucket in Lua

**Build:** move the whole read-modify-write into a Lua script executed by Redis.
Redis runs scripts atomically — no other command interleaves — so the race
disappears at its root.

Script contract:

```
KEYS[1] = bucket key
ARGV    = capacity, refill_rate, requested_tokens

1. HMGET key tokens last_refill
2. if missing -> tokens = capacity, last_refill = now
3. refill:  tokens = min(capacity, tokens + (now - last_refill) * rate)
4. if tokens >= 1  -> tokens = tokens - 1, allowed = 1
   else            -> allowed = 0
5. HSET key tokens last_refill
6. EXPIRE key <time to refill to full, plus margin>
7. return {allowed, tokens, retry_after}
```

It is the same algorithm you already wrote, in a different language, executed
somewhere else.

In Go use `redis.NewScript(src)` and `script.Run(ctx, client, keys, args...)`.
go-redis sends `EVALSHA` (the script's hash) and transparently falls back to
`EVAL` with the full source if the server replies `NOSCRIPT` — which happens
after a Redis restart or a script cache flush. Do not hand-roll this.

**Gotchas:**

- **Keep scripts short.** Redis is single-threaded; a slow script blocks the
  entire server for every client.
- **Lua numbers are doubles**, and Redis returns strings. `tonumber()` everything
  coming out of `HMGET`.
- **Redis Cluster:** every key a script touches must live in the same hash slot.
  One key per call avoids the problem entirely.
- **Namespace keys**: `rl:{user}` — never raw user input as a bare key.

**Step 8 of Stage 1 comes free here.** `EXPIRE` is the eviction sweeper: a
bucket idle long enough to be full simply expires. No ticker, no goroutine, no
`Close()`. Set the TTL to the time it takes to refill from empty
(`capacity / rate`) plus a margin — the same "a full bucket is indistinguishable
from a fresh one" rule you reasoned through in Stage 1.

**Done when:** N concurrent clients across multiple processes against one Redis
allow *exactly* the capacity, repeatably.

---

## Step 5 — Use Redis's clock

**Build:** call `redis.call('TIME')` inside the script instead of passing `now`
from Go.

Three app servers have three slightly different clocks. NTP drift of even a
second makes token math disagree, and a client hitting the "wrong" replica gets
a different answer. One shared store should imply one shared clock.

`TIME` returns `{seconds, microseconds}`; combine them for sub-second precision.

**Done when:** the script takes no timestamp from the caller.

---

## Step 6 — Timeouts and failure policy

Redis will be unavailable at some point. Decide now, explicitly.

**Build:**

1. A per-call timeout: `context.WithTimeout(ctx, 50*time.Millisecond)`. A
   limiter that adds latency is worse than the traffic it blocks.
2. A documented policy on error:
   - **fail open** (allow) — protects availability, loses protection exactly
     when something is already wrong
   - **fail closed** (deny) — protects the backend, turns a Redis blip into a
     full outage

Neither is correct in general. Pick per use case and write down why: a login
endpoint probably fails closed, a public read endpoint probably fails open.

Set `PoolSize`, `DialTimeout`, `ReadTimeout` on the client. Defaults are not
tuned for a hot path.

**Done when:** killing the Redis container produces the behaviour you chose,
with no goroutine pile-up and no latency spike.

---

## Step 7 — Circuit breaker with local fallback

**Build:** a type holding both a Redis limiter and an in-memory limiter,
satisfying the same interface. After N consecutive failures it stops calling
Redis and serves from memory; it probes periodically and recovers.

This is where Stage 1's code comes back as the safety net rather than being
thrown away. The limit becomes per-replica while degraded — imperfect, but
bounded, and vastly better than either failure mode alone.

**Teaches:** the decorator pattern over an interface, and why "accept
interfaces" matters — nothing above this layer knows it exists.

**Done when:** killing Redis mid-load causes a brief error window, then steady
service from the fallback; restarting Redis recovers automatically.

---

## Step 8 — Observability

**Build:** counters for allowed / denied / errors / fallback-active, and a
latency histogram for the Redis call. Prometheus (`promhttp`) is the standard
choice; structured logs via `log/slog` (stdlib).

A rate limiter you cannot observe is one you cannot tune. The first question in
production is always "is it rejecting legitimate traffic?", and you need
denial rate by key to answer it.

**Done when:** `/metrics` shows the limiter's behaviour under load, and the
fallback state is visible.

---

## Step 9 — Prove it distributed

**Build:** `docker-compose.yml` with Redis, three replicas of your server, and
nginx round-robining across them. Then an integration test: hammer nginx with a
global limit of 10/s and assert the *total* allowed across all three replicas is
10, not 30.

This is the test that proves Stage 2 did what it set out to do. Without it you
have three independent limiters that happen to share a database.

**Done when:** the global limit holds regardless of how requests distribute.

---

## Step 10 — Optional, in rough order of value

- **Local token leasing.** Each replica leases a batch of tokens from Redis and
  serves locally until they run out. Approximate, but removes Redis from the
  per-request path and keeps p99 flat. Roughly what Stripe and Cloudflare do.
  This is the most valuable item here and the most interesting to build.
- **Per-tier limits** — free / pro / enterprise, loaded at runtime rather than
  compiled in.
- **GCRA** (leaky bucket as a meter) — equivalent behaviour storing a single
  timestamp instead of two fields. Elegant, and smaller in Redis.
- **Sliding window counter** — cheaper than the log, more accurate than fixed
  windows. Worth implementing behind the same interface for comparison.

---

## Stage 2 is done when

- the global limit holds across three replicas, proven by a test
- concurrent load allows exactly the capacity, not capacity+N
- Redis being down degrades service instead of ending it
- a Redis restart mid-load recovers without intervention
- p99 latency added by the limiter is measured and acceptable
- metrics show allow/deny/error/fallback rates

---

## What to carry forward from Stage 1

- `Bucket.Allow`'s refill→check→consume ordering is now the Lua script.
- Injecting the clock is now `redis.call('TIME')`.
- The mutex is now Redis's single-threaded execution.
- The eviction sweeper is now `EXPIRE`.
- The in-memory limiter is now the fallback path.

Every Stage 1 idea has a Stage 2 counterpart. That is why the order was worth
keeping.
