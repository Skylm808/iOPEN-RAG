package database

import (
	"context"
	"github.com/go-redis/redis/v8"
	"pai-smart-go/pkg/log"
)

var RDB *redis.Client

// InitRedis 初始化 Redis 客户端连接
func InitRedis(addr, password string, db int) {
	RDB = redis.NewClient(&redis.Options{
		Addr:     addr,
		Password: password, // no password set
		DB:       db,       // use default DB
	})

	// 测试连接
	ctx := context.Background()
	if err := RDB.Ping(ctx).Err(); err != nil {
		log.Fatal("failed to connect to redis", err)
	}

	log.Info("Redis client connected successfully")
}
