# Stage 3 — Key Extraction

Small stage. The limiter answers "is this key allowed?" — this is about deciding
what the key *is*, and making that a first-class, swappable choice instead of one
hardcoded function.

Who you limit and how much you limit them are two independent decisions. They
should be two independent pieces of code.

---

## Step 1 — Give the shape a name

Today the middleware spells the type out inline:

```go
func RateLimit(l Limiter, keyExtractor func(r *http.Request) string, next http.Handler) http.Handler
```

Name it:

```go
type KeyFunc func(*http.Request) string
```

A named function type — the same idea as `type Celsius float64`, a name for a
shape that already existed. It makes the signature readable, gives the concept
something to search for, and leaves room to hang methods on it later.

Parameter names are dropped from the type. In a function *type* only the types
matter, so `func(*http.Request) string` is the convention.

**Done when:** `RateLimit` takes a `KeyFunc` and everything still compiles.

---

## Step 2 — Extractors that need no configuration

These *are* `KeyFunc` values directly. Pass them without parentheses.

```go
// ByIP limits per client IP address.
func ByIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// ByPath limits per endpoint rather than per caller.
func ByPath(r *http.Request) string {
	return r.URL.Path
}
```

`r.RemoteAddr` is `"127.0.0.1:54321"` — the port is part of it and differs on
every connection. Without splitting it off, every request gets its own bucket
and the limiter silently does nothing.

**Behind a proxy or load balancer**, `RemoteAddr` is the *proxy's* address and
every user shares one bucket. The fix is `X-Forwarded-For`, but only trust that
header when you control the proxy — clients can forge it freely. Leave a comment
either way so the assumption is written down.

**Done when:** two different machines (or `curl --interface`) get separate
budgets, and the same machine shares one across many connections.

---

## Step 3 — Extractors that need configuration

These are functions that *build* a `KeyFunc`:

```go
// ByQuery limits per value of a query parameter, e.g. ?user=alice
func ByQuery(param string) KeyFunc {
	return func(r *http.Request) string {
		return r.URL.Query().Get(param)
	}
}

// ByHeader limits per header value, e.g. X-API-Key
func ByHeader(name string) KeyFunc {
	return func(r *http.Request) string {
		return r.Header.Get(name)
	}
}
```

Two functions in one. The outer runs **once at startup** with the configuration;
the inner runs **once per request**. The returned function captures `param` in a
closure, so it still knows which parameter to read long after `ByQuery` returned.

The same two-time-zones structure as the middleware itself.

Call sites end up in two shapes, and that is fine:

```go
httpmw.ByQuery("user")   // called — returns a KeyFunc
httpmw.ByIP              // not called — already is one
```

**Done when:** `RateLimit(lim, httpmw.ByQuery("user"), mux)` behaves exactly as
the old hardcoded version did.

---

## Step 4 — Combine them

Most real services limit by API key when there is one and fall back to IP
otherwise:

```go
// First returns the result of the first extractor that yields a non-empty key.
func First(fns ...KeyFunc) KeyFunc
```

Write it yourself — it is a short loop, and it is good practice at both variadic
parameters and returning closures.

```go
httpmw.First(httpmw.ByHeader("X-API-Key"), httpmw.ByIP)
```

**Teaches:** composition. `First` takes `KeyFunc`s and returns a `KeyFunc`, so
its result can be passed anywhere one is expected — including into another
`First`. Same property that lets middleware stack.

**Done when:** a request with an API key is limited by that key, and one without
falls back to its IP.

---

## Step 5 — Decide what an empty key means

Every extractor can return `""` — missing parameter, missing header. Right now
that becomes the Redis key `rl:` shared by every anonymous caller, which is an
accident rather than a decision.

Two defensible answers:

1. **Reject it.** In the middleware, before calling `Allow`:

   ```go
   key := keyFn(r)
   if key == "" {
       http.Error(w, "missing rate limit key", http.StatusBadRequest)
       return
   }
   ```

2. **Make it impossible.** Use `First(..., ByIP)` as the default, so there is
   always a fallback that cannot be empty.

Pick one deliberately. The thing to avoid is a shared anonymous bucket nobody
chose.

**Done when:** a request with no key gets a defined, intentional response.

---

## Stage 3 is done when

- `KeyFunc` is a named type and `RateLimit` takes it
- at least three extractors exist, covering both shapes (configured and not)
- `First` composes them
- the empty-key case has a decided answer
- switching from per-user to per-IP limiting is a one-line change in `main`

That last point is the test of whether this stage was worth doing.

---

## Housekeeping left over from Stages 1–2

- [ ] `gofmt -w .` — `ratelimit.go` and `redis.go` are unformatted. Turn on
      format-on-save in GoLand.
- [ ] Remove the debug `fmt.Println` calls in `Limiter.Evict` (memory.go) — they
      print the whole map every 5 seconds.
- [ ] Unexport `Users`, `Capacity`, `Mutex` and all of `Bucket` in memory.go.
      Exported `Users` and `Mutex` let any package mutate the map without the
      lock, undoing the locking work from Stage 1.
- [ ] `main`: `fmt.Println("error")` discards the actual error. Use
      `log.Fatal(err)` so a failed bind says why.
- [ ] `Evict(evictSeconds int64)` would read better as a `time.Duration`, and
      unexported — `NewLimiter` already starts it, so nothing outside should.
- [ ] Rename the module: `GO_rate_limiter` is not idiomatic Go. Lowercase, no
      underscores — `ratelimiter`.
- [ ] Add an `-addr` flag so two servers can run at once and share one Redis
      budget. That is the demo this whole project exists to be able to show.
