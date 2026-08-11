package redis

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"math/rand/v2"
	"strings"
	"time"

	"github.com/chitandabb/GoAgent/internal/conversationmemory"

	"github.com/google/uuid"
	rediscli "github.com/redis/go-redis/v9"
)

const (
	conversationMemoryCacheSchemaVersion = 1
	maxConversationMemoryCacheBytes      = 2 * 1024 * 1024
	conversationMemoryCacheKeyPrefix     = "mesguard:conversation-memory:v1:"
)

type ConversationMemoryCacheConfig struct {
	TTL              time.Duration
	JitterRatio      float64
	OperationTimeout time.Duration
	randomFloat64    func() float64
}

func (c ConversationMemoryCacheConfig) validate() error {
	if c.TTL < time.Minute || c.TTL > 7*24*time.Hour ||
		math.IsNaN(c.JitterRatio) || math.IsInf(c.JitterRatio, 0) ||
		c.JitterRatio < 0 || c.JitterRatio > 0.50 ||
		c.OperationTimeout < 5*time.Millisecond || c.OperationTimeout > time.Second {
		return errors.New("conversation memory cache configuration is invalid")
	}
	return nil
}

type conversationMemoryCommands interface {
	Get(context.Context, string) (string, error)
	Store(context.Context, string, string, string, time.Duration, time.Duration) error
	DeleteIndexed(context.Context, string, string) error
}

type ConversationMemoryCache struct {
	commands         conversationMemoryCommands
	ttl              time.Duration
	jitterRatio      float64
	operationTimeout time.Duration
	randomFloat64    func() float64
}

var _ conversationmemory.SnapshotCache = (*ConversationMemoryCache)(nil)

func NewConversationMemoryCache(
	client *rediscli.Client,
	config ConversationMemoryCacheConfig,
) (*ConversationMemoryCache, error) {
	if client == nil {
		return nil, errors.New("conversation memory Redis client is nil")
	}
	return newConversationMemoryCache(&goRedisConversationMemoryCommands{client: client}, config)
}

func newConversationMemoryCache(
	commands conversationMemoryCommands,
	config ConversationMemoryCacheConfig,
) (*ConversationMemoryCache, error) {
	if commands == nil || config.validate() != nil {
		return nil, errors.New("conversation memory cache configuration is invalid")
	}
	randomFloat64 := config.randomFloat64
	if randomFloat64 == nil {
		randomFloat64 = rand.Float64
	}
	return &ConversationMemoryCache{
		commands: commands, ttl: config.TTL, jitterRatio: config.JitterRatio,
		operationTimeout: config.OperationTimeout, randomFloat64: randomFloat64,
	}, nil
}

type conversationMemoryCacheEnvelope struct {
	SchemaVersion int                         `json:"schemaVersion"`
	Snapshot      conversationmemory.Snapshot `json:"snapshot"`
}

func (c *ConversationMemoryCache) Load(
	ctx context.Context,
	conversationID, snapshotID uuid.UUID,
) (conversationmemory.Snapshot, error) {
	if c == nil || c.commands == nil || conversationID == uuid.Nil || snapshotID == uuid.Nil {
		return conversationmemory.Snapshot{}, conversationmemory.ErrSnapshotCacheInvalid
	}
	operationCtx, cancel := context.WithTimeout(ctx, c.operationTimeout)
	defer cancel()
	encoded, err := c.commands.Get(operationCtx, conversationMemorySnapshotKey(conversationID, snapshotID))
	if errors.Is(err, rediscli.Nil) {
		return conversationmemory.Snapshot{}, conversationmemory.ErrSnapshotCacheMiss
	}
	if err != nil {
		return conversationmemory.Snapshot{}, err
	}
	if len(encoded) == 0 || len(encoded) > maxConversationMemoryCacheBytes {
		return conversationmemory.Snapshot{}, conversationmemory.ErrSnapshotCacheInvalid
	}
	var envelope conversationMemoryCacheEnvelope
	decoder := json.NewDecoder(bytes.NewBufferString(encoded))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&envelope); err != nil {
		return conversationmemory.Snapshot{}, fmt.Errorf("%w: %v", conversationmemory.ErrSnapshotCacheInvalid, err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return conversationmemory.Snapshot{}, conversationmemory.ErrSnapshotCacheInvalid
	}
	if envelope.SchemaVersion != conversationMemoryCacheSchemaVersion ||
		envelope.Snapshot.ConversationID != conversationID || envelope.Snapshot.ID != snapshotID ||
		envelope.Snapshot.Status != conversationmemory.SnapshotStatusActive || envelope.Snapshot.Validate() != nil {
		return conversationmemory.Snapshot{}, conversationmemory.ErrSnapshotCacheInvalid
	}
	return envelope.Snapshot, nil
}

func (c *ConversationMemoryCache) Store(ctx context.Context, snapshot conversationmemory.Snapshot) error {
	if c == nil || c.commands == nil || snapshot.Status != conversationmemory.SnapshotStatusActive ||
		snapshot.Validate() != nil {
		return conversationmemory.ErrSnapshotCacheInvalid
	}
	encoded, err := json.Marshal(conversationMemoryCacheEnvelope{
		SchemaVersion: conversationMemoryCacheSchemaVersion,
		Snapshot:      snapshot,
	})
	if err != nil || len(encoded) > maxConversationMemoryCacheBytes {
		return conversationmemory.ErrSnapshotCacheInvalid
	}
	operationCtx, cancel := context.WithTimeout(ctx, c.operationTimeout)
	defer cancel()
	return c.commands.Store(
		operationCtx,
		conversationMemorySnapshotKey(snapshot.ConversationID, snapshot.ID),
		conversationMemoryIndexKey(snapshot.ConversationID),
		string(encoded), c.jitteredTTL(), c.maximumTTL(),
	)
}

func (c *ConversationMemoryCache) DeleteConversation(ctx context.Context, conversationID uuid.UUID) error {
	if c == nil || c.commands == nil || conversationID == uuid.Nil {
		return conversationmemory.ErrSnapshotCacheInvalid
	}
	operationCtx, cancel := context.WithTimeout(ctx, c.operationTimeout)
	defer cancel()
	return c.commands.DeleteIndexed(
		operationCtx,
		conversationMemoryIndexKey(conversationID),
		conversationMemoryConversationPrefix(conversationID)+"snapshot:",
	)
}

func (c *ConversationMemoryCache) jitteredTTL() time.Duration {
	randomValue := c.randomFloat64()
	if math.IsNaN(randomValue) || math.IsInf(randomValue, 0) || randomValue < 0 || randomValue > 1 {
		randomValue = 0.5
	}
	multiplier := 1 - c.jitterRatio + (2 * c.jitterRatio * randomValue)
	return time.Duration(float64(c.ttl) * multiplier)
}

func (c *ConversationMemoryCache) maximumTTL() time.Duration {
	return time.Duration(float64(c.ttl) * (1 + c.jitterRatio))
}

func conversationMemoryConversationPrefix(conversationID uuid.UUID) string {
	return conversationMemoryCacheKeyPrefix + "{" + conversationID.String() + "}:"
}

func conversationMemorySnapshotKey(conversationID, snapshotID uuid.UUID) string {
	return conversationMemoryConversationPrefix(conversationID) + "snapshot:" + snapshotID.String()
}

func conversationMemoryIndexKey(conversationID uuid.UUID) string {
	return conversationMemoryConversationPrefix(conversationID) + "keys"
}

type goRedisConversationMemoryCommands struct {
	client *rediscli.Client
}

func (c *goRedisConversationMemoryCommands) Get(ctx context.Context, key string) (string, error) {
	return c.client.Get(ctx, key).Result()
}

func (c *goRedisConversationMemoryCommands) Store(
	ctx context.Context,
	key, indexKey, value string,
	ttl, indexTTL time.Duration,
) error {
	_, err := c.client.TxPipelined(ctx, func(pipe rediscli.Pipeliner) error {
		pipe.Set(ctx, key, value, ttl)
		pipe.SAdd(ctx, indexKey, key)
		pipe.Expire(ctx, indexKey, indexTTL)
		return nil
	})
	return err
}

func (c *goRedisConversationMemoryCommands) DeleteIndexed(
	ctx context.Context,
	indexKey, allowedPrefix string,
) error {
	members, err := c.client.SMembers(ctx, indexKey).Result()
	if err != nil {
		return err
	}
	keys := make([]string, 0, len(members)+1)
	for _, member := range members {
		if strings.HasPrefix(member, allowedPrefix) {
			keys = append(keys, member)
		}
	}
	keys = append(keys, indexKey)
	return c.client.Unlink(ctx, keys...).Err()
}
