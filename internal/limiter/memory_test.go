package limiter

import (
	"sync"
	"testing"
	"time"
)

// A fixed point in time. Any value works — what matters is that it never moves,
// so every test is deterministic.
var start = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

// A helper, so each test doesn't repeat the struct literal.
func newTestBucket(capacity, rate float64) *Bucket {
	return &Bucket{
		Tokens:     capacity,
		Capacity:   capacity,
		RefillRate: rate,
		LastRefill: start,
	}
}

func newTestLimiter(empty bool) *Limiter {
	capacity := 10.0
	rate := 0.2
	var users map[string]*Bucket
	if !empty {
		users = map[string]*Bucket{"ana": newTestBucket(capacity, rate), "edy": &Bucket{
			Tokens:     0,
			LastRefill: start,
			Capacity:   capacity,
			RefillRate: rate,
		}}
	} else {
		users = map[string]*Bucket{}
	}
	return &Limiter{
		Users:      users,
		Capacity:   capacity,
		refillRate: rate,
		Mutex:      sync.Mutex{},
		quit:       nil,
		closeOnce:  sync.Once{},
	}
}

func TestSweepDelete(t *testing.T) {
	tests := []struct {
		name        string
		now         time.Time
		edySurvives bool
		anaSurvives bool
		wantDeleted int
	}{
		{"Delete ana and keep edy start time", start, true, false, 1},
		{"Delete ana and keep edy by a second", start.Add(time.Second * 49), true, false, 1},
		{"Delete ana and edy", start.Add(time.Second * 50), false, false, 2},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			limiter := newTestLimiter(false)
			deleted := limiter.sweep(tt.now)

			if deleted != tt.wantDeleted {
				t.Errorf("Deleted: %v, wanted to delete: %v", deleted, tt.wantDeleted)
			}

			if _, ok := limiter.Users["edy"]; ok != tt.edySurvives {
				t.Errorf("edy survived: %v, want keep: %v", ok, tt.edySurvives)
			}

			if _, ok := limiter.Users["ana"]; ok != tt.anaSurvives {
				t.Errorf("ana survived: %v, want keep: %v", ok, tt.anaSurvives)
			}
		})
	}

}

func TestSweepEmptyMap(t *testing.T) {
	limiter := newTestLimiter(true)
	deleted := limiter.sweep(start)
	if deleted != 0 {
		t.Errorf("Deleted: %v, expected 0", deleted)
	}
}

// The simplest shape a test can have.
func TestBucketAllowsBurstUpToCapacity(t *testing.T) {
	b := newTestBucket(3, 1)

	for i := 1; i <= 3; i++ {
		if !b.Allow(start) {
			t.Errorf("request %d: denied, want allowed", i)
		}
	}

	if b.Allow(start) {
		t.Error("request 4: allowed, want denied (bucket should be empty)")
	}
}

// Table-driven: one function, many cases, each reported separately.
func TestBucketRefill(t *testing.T) {
	tests := []struct {
		name    string
		elapsed time.Duration
		want    bool
	}{
		{"no time passed", 0, false},
		{"half a token", 500 * time.Millisecond, false},
		{"exactly one token", 1 * time.Second, true},
		{"plenty of time", 10 * time.Second, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b := newTestBucket(5, 1) // capacity 5, 1 token/sec
			for range 5 {
				b.Allow(start) // drain it
			}

			got := b.Allow(start.Add(tt.elapsed))
			if got != tt.want {
				t.Errorf("after %v: Allow() = %v, want %v", tt.elapsed, got, tt.want)
			}
		})
	}
}

// An idle bucket must not bank tokens beyond its capacity.
func TestBucketDoesNotOverfill(t *testing.T) {
	b := newTestBucket(3, 1)
	for range 3 {
		b.Allow(start) // drain
	}

	// One hour at 1 token/sec is 3600 tokens' worth of time. Capacity is 3.
	if !b.Allow(start.Add(time.Hour)) {
		t.Fatal("denied after a long idle period, want allowed")
	}

	if want := 2.0; b.Tokens != want {
		t.Errorf("tokens = %v, want %v (refill capped at capacity, then one spent)", b.Tokens, want)
	}
}
