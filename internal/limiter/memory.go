package limiter

import (
	"fmt"
	"sync"
	"time"
)

type Bucket struct {
	Tokens     float64
	LastRefill time.Time
	Capacity   float64
	RefillRate float64
}

func (b *Bucket) Allow(now time.Time) bool {
	difference := now.Sub(b.LastRefill)

	seconds := difference.Seconds()

	tokensToAdd := seconds * b.RefillRate

	b.Tokens = min(b.Tokens+tokensToAdd, b.Capacity)
	b.LastRefill = now
	if b.Tokens >= 1 {
		b.Tokens -= 1
		return true
	}

	return false
}

type Limiter struct {
	Users      map[string]*Bucket
	Capacity   float64
	refillRate float64
	Mutex      sync.Mutex
	quit       chan struct{}
	closeOnce  sync.Once
}

func (limiter *Limiter) Allow(key string, now time.Time) bool {
	limiter.Mutex.Lock()
	defer limiter.Mutex.Unlock()
	value, ok := limiter.Users[key]
	if !ok {
		value = &Bucket{
			Tokens:     limiter.Capacity,
			LastRefill: now,
			Capacity:   limiter.Capacity,
			RefillRate: limiter.refillRate,
		}
		limiter.Users[key] = value
	}
	return value.Allow(now)
}

func NewLimiter(capacity, refillRate float64) *Limiter {
	limiter := &Limiter{Users: make(map[string]*Bucket), Capacity: capacity, refillRate: refillRate, quit: make(chan struct{})}
	go limiter.Evict(5)
	return limiter
}

func (limiter *Limiter) Evict(evictSeconds int64) {
	ticker := time.NewTicker(time.Duration(evictSeconds) * time.Second)
	defer ticker.Stop()
	for {
		select {
		case now := <-ticker.C:
			fmt.Println(limiter.Users)
			fmt.Println(limiter.sweep(now))
		case <-limiter.quit:
			return
		}
	}
}

func (limiter *Limiter) sweep(now time.Time) int {
	limiter.Mutex.Lock()
	defer limiter.Mutex.Unlock()

	removed := 0
	for key, value := range limiter.Users {
		elapsed := now.Sub(value.LastRefill).Seconds()
		if value.Tokens+elapsed*value.RefillRate >= value.Capacity {
			delete(limiter.Users, key)
			removed++
		}
	}
	return removed
}

func (limiter *Limiter) Close() {
	limiter.closeOnce.Do(func() { close(limiter.quit) })
}
