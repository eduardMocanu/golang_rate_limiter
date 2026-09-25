package limiter

import (
	"context"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
)

const (
	testCapacity   = 10
	testGoroutines = 100
)

// 100 goroutines hitting one key must allow exactly the capacity — never one
// more. refillRate is 0 so no tokens can appear part-way through, which keeps
// the expected number exact however slow the machine is.
func TestMemoryLimiterConcurrentAllow(t *testing.T) {
	l := &Limiter{
		Users:      map[string]*Bucket{},
		Capacity:   testCapacity,
		refillRate: 0,
	}

	var wg sync.WaitGroup
	var allowed atomic.Int64

	for range testGoroutines {
		wg.Add(1)
		go func() {
			defer wg.Done()

			ok, err := l.Allow(context.Background(), "alice")
			if err != nil {
				t.Error("unexpected error:", err)
				return
			}
			if ok {
				allowed.Add(1)
			}
		}()
	}
	wg.Wait()

	if got := allowed.Load(); got != testCapacity {
		t.Errorf("allowed = %d, want exactly %d", got, testCapacity)
	}
}

// Two limiters with separate clients stand in for two server replicas: they
// share no Go memory, so only Redis can be coordinating them.
func TestRedisLimiterConcurrentAllow(t *testing.T) {
	a := NewRedis(testRedisClient(t), testCapacity, 0.0001)
	b := NewRedis(testRedisClient(t), testCapacity, 0.0001)

	ctx := context.Background()
	key := t.Name()

	if err := a.client.Del(ctx, "rl:"+key).Err(); err != nil {
		t.Fatal("could not clear the key:", err)
	}
	t.Cleanup(func() { a.client.Del(context.Background(), "rl:"+key) })

	var wg sync.WaitGroup
	var allowed atomic.Int64

	for i := range testGoroutines {
		wg.Add(1)
		go func() {
			defer wg.Done()

			l := a
			if i%2 == 1 {
				l = b
			}

			ok, err := l.Allow(ctx, key)
			if err != nil {
				t.Error("unexpected error:", err)
				return
			}
			if ok {
				allowed.Add(1)
			}
		}()
	}
	wg.Wait()

	if got := allowed.Load(); got != testCapacity {
		t.Errorf("allowed = %d, want exactly %d", got, testCapacity)
	}
}

// testRedisClient connects to Redis, or skips the test if there isn't one.
func testRedisClient(t *testing.T) *redis.Client {
	t.Helper()

	addr := os.Getenv("REDIS_ADDR")
	if addr == "" {
		addr = "localhost:6379"
	}

	c := redis.NewClient(&redis.Options{Addr: addr})

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	if err := c.Ping(ctx).Err(); err != nil {
		err := c.Close()
		if err != nil {
			return nil
		}
		t.Skipf("no redis at %s: %v", addr, err)
	}

	t.Cleanup(func() {
		err := c.Close()
		if err != nil {
			return
		}
	})
	return c
}
