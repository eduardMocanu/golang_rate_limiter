package main

import (
	"fmt"
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
}

func (limiter *Limiter) Allow(key string, now time.Time) bool {
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

func main() {
	limiter := NewLimiter(3, 0.2)
	now := time.Now()
	for range 5 {
		fmt.Println("alice:", limiter.Allow("alice", now), " bob:", limiter.Allow("bob", now))
	}
}
