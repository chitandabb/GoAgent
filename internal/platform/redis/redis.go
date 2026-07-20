package redis

import (
	"context"
	"fmt"

	"github.com/chitandabb/GoAgent/internal/platform/config"

	rediscli "github.com/redis/go-redis/v9"
)

func Open(ctx context.Context, cfg config.RedisConfig) (*rediscli.Client, error) {
	client := rediscli.NewClient(&rediscli.Options{
		Addr:     cfg.Address(),
		Password: cfg.Password,
		DB:       cfg.Database,
		Protocol: 2,
	})
	if err := client.Ping(ctx).Err(); err != nil {
		_ = client.Close()
		return nil, fmt.Errorf("ping redis: %w", err)
	}
	return client, nil
}
