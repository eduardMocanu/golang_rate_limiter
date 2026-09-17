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

func main() {

	b := Bucket{
		tokens:     3,
		lastRefill: time.Now(),
		capacity:   20,
		refillRate: 0.1,
	}

	for range 15 {
		time.Sleep(time.Second * 1)
		fmt.Println(b.Allow(time.Now()))
	}
}
