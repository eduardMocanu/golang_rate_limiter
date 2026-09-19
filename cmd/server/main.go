package main

import (
	"GO_rate_limiter/internal/limiter"
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/redis/go-redis/v9"
)

func CreateClient() *redis.Client {
	return redis.NewClient(&redis.Options{Addr: string("localhost:6379")})
}

func main() {
	//newLimiter := limiter.NewLimiter(3, 0.2)
	//defer newLimiter.Close()
	//
	//err := http.ListenAndServe("localhost:8080", httpmw.RateLimit(newLimiter, httpmw.KeyExtractorQuery, httpmw.InitMux()))
	//
	//if err != nil {
	//	fmt.Println("error ", err)
	//	return
	//}

	client := CreateClient()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer func(client *redis.Client) {
		err := client.Close()
		if err != nil {
			fmt.Println("error on the client level")
		}
	}(client)
	defer cancel()
	a := limiter.NewRedis(client, 10, 0.2)
	b := limiter.NewRedis(client, 10, 0.2)

	var wg sync.WaitGroup
	var allowed atomic.Int64

	for i := range 50 {
		wg.Add(1)

		go func() {

			c := a
			if i%2 == 0 {
				c = b
			}
			defer wg.Done()
			all, err := c.Allow(ctx, "ana")
			if err != nil {
				return
			}
			if all {
				allowed.Add(1)
			}
		}()
	}
	wg.Wait()
	fmt.Println(allowed.Load())
}
