package httpmw

import (
	"GO_rate_limiter/internal/limiter"
	"fmt"
	"net/http"
	"time"
)

func KeyExtractorQuery(r *http.Request) string {
	return r.URL.Query().Get("user")
}

func RateLimit(newLimiter *limiter.Limiter, keyExtractor func(r *http.Request) string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := keyExtractor(r)

		if !newLimiter.Allow(key, time.Now()) {
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
		fmt.Println("ok")
		if err != nil {
			return
		}
	})

	return mux
}
