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
