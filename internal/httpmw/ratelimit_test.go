package httpmw

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

// A fake limiter. It ignores the key entirely and returns whatever you set.
type stubLimiter struct {
	allowed bool
	err     error
}

func (s stubLimiter) Allow(ctx context.Context, key string) (bool, error) {
	return s.allowed, s.err
}

func TestRateLimitDenied(t *testing.T) {
	// A handler that records whether it ran.
	nextCalled := false
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		nextCalled = true
	})

	h := RateLimit(stubLimiter{allowed: false}, KeyExtractorQuery, next)

	req := httptest.NewRequest(http.MethodGet, "/?user=alice", nil)
	rec := httptest.NewRecorder()

	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusTooManyRequests {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusTooManyRequests)
	}
	if nextCalled {
		t.Error("next handler ran, want it skipped")
	}
}

func TestRateLimitRedisDown(t *testing.T) {
	nextCalled := false
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		nextCalled = true
	})

	h := RateLimit(stubLimiter{err: errors.New("redis is down")}, KeyExtractorQuery, next)

	req := httptest.NewRequest(http.MethodGet, "/?user=alice", nil)
	rec := httptest.NewRecorder()

	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusServiceUnavailable)
	}
	if nextCalled {
		t.Error("next handler ran, want it skipped")
	}
}
