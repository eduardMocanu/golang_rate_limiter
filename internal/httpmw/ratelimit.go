package httpmw

import (
	"context"
	"net/http"
	"time"
)

func KeyExtractorQuery(r *http.Request) string {
	return r.URL.Query().Get("user")
}

type Limiter interface {
	Allow(ctx context.Context, key string) (bool, error)
}

func RateLimit(newLimiter Limiter, keyExtractor func(r *http.Request) string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := keyExtractor(r)
		ctx, cancel:= context.WithTimeout(r.Context(), 50 * time.Millisecond)
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
