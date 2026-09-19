package limiter

import (
	"context"
	"time"

	"github.com/redis/go-redis/v9"
)

var luaScript = redis.NewScript(`
  local key = KEYS[1]
  local capacity = tonumber(ARGV[1])
  local rate = tonumber(ARGV[2])
  local now = tonumber(ARGV[3])

  local data = redis.call('HMGET', key, 'tokens', 'last_refill')
  local tokens = tonumber(data[1])
  local last_refill = tonumber(data[2])

  if tokens == nil then
    tokens = capacity
    last_refill = now
  end

  local elapsed = now - last_refill
  tokens = math.min(tokens + elapsed * rate, capacity)

  local allowed = 0
  if tokens >= 1 then
    tokens = tokens - 1
    allowed = 1
  end

  redis.call('HSET', key, 'tokens', tokens, 'last_refill', now)
  redis.call('EXPIRE', key, math.ceil(capacity / rate * 1.5))

  return allowed

`)

type RedisLimiter struct {
	client     *redis.Client
	capacity   float64
	refillRate float64
}

func NewRedis(client *redis.Client, capacity, refillRate float64) *RedisLimiter {
	return &RedisLimiter{
		client:     client,
		capacity:   capacity,
		refillRate: refillRate,
	}
}

func (rl *RedisLimiter) Allow(ctx context.Context, key string) (bool, error) {

	now := float64(time.Now().UnixMilli()) / 1000

	res, err := luaScript.Run(ctx, rl.client, []string{"rl:"+key}, rl.capacity, rl.refillRate, now).Int64()
	if err != nil{
		return false, err
	}
	return res == 1, nil
}
