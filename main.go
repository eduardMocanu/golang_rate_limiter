package main

import (
	"log"
	"net/http"
	"sync"
	"time"
)

type Bucket struct {
	tokens     float64
	lastRefill time.Time
	capacity   float64
	refillRate float64
}

func (b *Bucket) Allow(now time.Time) bool {
	difference := now.Sub(b.lastRefill)

	seconds := difference.Seconds()

	tokensToAdd := seconds * b.refillRate

	b.tokens = min(b.tokens+tokensToAdd, b.capacity)
	b.lastRefill = now
	if b.tokens >= 1 {
		b.tokens -= 1
		return true
	}

	return false
}

type Limiter struct {
	users      map[string]*Bucket
	capacity   float64
	refillRate float64
	mutex      sync.Mutex
}

func (limiter *Limiter) Allow(key string, now time.Time) bool {
	limiter.mutex.Lock()
	defer limiter.mutex.Unlock()
	value, ok := limiter.users[key]
	if !ok {
		value = &Bucket{
			tokens:     limiter.capacity,
			lastRefill: now,
			capacity:   limiter.capacity,
			refillRate: limiter.refillRate,
		}
		limiter.users[key] = value
	}
	return value.Allow(now)
}

func NewLimiter(capacity, refillRate float64) *Limiter {
	return &Limiter{users: make(map[string]*Bucket), capacity: capacity, refillRate: refillRate}
}

func KeyExtractorQuery(r *http.Request) string {
	return r.URL.Query().Get("user")
}

func RateLimit(limiter *Limiter, keyExtractor func(r *http.Request) string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := keyExtractor(r)

		if !limiter.Allow(key, time.Now()) {
			http.Error(w, "rate limited", http.StatusTooManyRequests)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func InitMux() *http.ServeMux {
	mux := http.NewServeMux()

	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		_, err := w.Write([]byte("hello"))
		if err != nil {
			return
		}
	})

	return mux
}

func main() {

	err := http.ListenAndServe("localhost:8080", RateLimit(NewLimiter(3, 0.2), KeyExtractorQuery, InitMux()))
	if err != nil {
		log.Fatal("Error")
		return
	}
}
