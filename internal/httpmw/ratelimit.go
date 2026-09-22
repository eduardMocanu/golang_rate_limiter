package httpmw

import (
	"context"
	"net"
	"net/http"
	"time"
)

type KeyExtractor func(r *http.Request) string

func KeyExtractorQuery(r *http.Request) string {
	return r.URL.Query().Get("user")
}

func KeyExtractorIP(r *http.Request) string {
	IP, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return IP
}

func FirstExtractor(fns ...KeyExtractor) KeyExtractor {
	return func(r *http.Request) string {
		for _, f := range fns {
			item := f(r)
			if item != "" {
				return item
			}
		}
		return ""
	}
}

type Limiter interface {
	Allow(ctx context.Context, key string) (bool, error)
}

func RateLimit(newLimiter Limiter, keyExtractor KeyExtractor, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := keyExtractor(r)
		ctx, cancel := context.WithTimeout(r.Context(), 50*time.Millisecond)
		defer cancel()
		allowed, err := newLimiter.Allow(ctx, key)
		if err != nil {
			http.Error(w, "rate limiter unavailable", http.StatusServiceUnavailable)
			return
		}

		if !allowed {
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
