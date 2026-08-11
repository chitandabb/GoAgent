//go:build integration

package redis

import (
	"context"
	"os"
	"testing"
	"time"

	rediscli "github.com/redis/go-redis/v9"
)

func TestConversationMemoryCacheAgainstRedis(t *testing.T) {
	address := os.Getenv("MESGUARD_TEST_REDIS_ADDR")
	if address == "" {
		t.Skip("MESGUARD_TEST_REDIS_ADDR is not configured")
	}
	client := rediscli.NewClient(&rediscli.Options{Addr: address, Protocol: 2})
	t.Cleanup(func() { _ = client.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := client.Ping(ctx).Err(); err != nil {
		t.Fatalf("ping test Redis: %v", err)
	}

	snapshot := redisActiveSnapshotFixture(t)
	key := conversationMemorySnapshotKey(snapshot.ConversationID, snapshot.ID)
	indexKey := conversationMemoryIndexKey(snapshot.ConversationID)
	outsideKey := "mesguard:conversation-memory:integration:outside:" + snapshot.ID.String()
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), time.Second)
		defer cleanupCancel()
		_ = client.Del(cleanupCtx, key, indexKey, outsideKey).Err()
	})

	cache, err := newConversationMemoryCache(&goRedisConversationMemoryCommands{client: client}, ConversationMemoryCacheConfig{
		TTL: time.Minute, JitterRatio: 0.10, OperationTimeout: 500 * time.Millisecond,
		randomFloat64: func() float64 { return 0.5 },
	})
	if err != nil {
		t.Fatalf("newConversationMemoryCache() error = %v", err)
	}
	if err := cache.Store(ctx, snapshot); err != nil {
		t.Fatalf("Store() error = %v", err)
	}
	loaded, err := cache.Load(ctx, snapshot.ConversationID, snapshot.ID)
	if err != nil || loaded.ID != snapshot.ID || loaded.PayloadSHA256 != snapshot.PayloadSHA256 {
		t.Fatalf("Load() = %+v, %v", loaded, err)
	}
	ttl, err := client.TTL(ctx, key).Result()
	if err != nil || ttl < 50*time.Second || ttl > 60*time.Second {
		t.Fatalf("snapshot TTL = %s, %v", ttl, err)
	}
	members, err := client.SMembers(ctx, indexKey).Result()
	if err != nil || len(members) != 1 || members[0] != key {
		t.Fatalf("cache index members = %v, %v", members, err)
	}

	if err := client.Set(ctx, outsideKey, "must survive", time.Minute).Err(); err != nil {
		t.Fatalf("seed outside key: %v", err)
	}
	if err := client.SAdd(ctx, indexKey, outsideKey).Err(); err != nil {
		t.Fatalf("seed polluted index member: %v", err)
	}
	if err := cache.DeleteConversation(ctx, snapshot.ConversationID); err != nil {
		t.Fatalf("DeleteConversation() error = %v", err)
	}
	if exists, err := client.Exists(ctx, key, indexKey).Result(); err != nil || exists != 0 {
		t.Fatalf("conversation cache keys remain = %d, %v", exists, err)
	}
	if exists, err := client.Exists(ctx, outsideKey).Result(); err != nil || exists != 1 {
		t.Fatalf("outside key existence = %d, %v, want 1", exists, err)
	}
}
