package main

import (
	"GO_rate_limiter/internal/httpmw"
	"GO_rate_limiter/internal/limiter"
	"fmt"
	"net/http"

	"github.com/redis/go-redis/v9"
)

func CreateClient() *redis.Client {
	return redis.NewClient(&redis.Options{Addr: string("redis:6379")})
}

func main() {

	client := CreateClient()
	defer func(client *redis.Client) {
		err := client.Close()
		if err != nil {
			fmt.Println("error on the client level")
		}
	}(client)
	a := limiter.NewRedis(client, 10, 0.2)

	err := http.ListenAndServe(":8080", httpmw.RateLimit(a, httpmw.FirstExtractor(httpmw.KeyExtractorQuery, httpmw.KeyExtractorIP), httpmw.InitMux()))

	if err != nil {
		fmt.Println("error")
		return
	}
	return

}
