package main

import (
	"GO_rate_limiter/internal/httpmw"
	"GO_rate_limiter/internal/limiter"
	"net/http"
)

func main() {
	newLimiter := limiter.NewLimiter(3, 0.2)
	defer newLimiter.Close()

	err := http.ListenAndServe("localhost:8080", httpmw.RateLimit(newLimiter, httpmw.KeyExtractorQuery, httpmw.InitMux()))

	if err != nil {
		return
	}
}
