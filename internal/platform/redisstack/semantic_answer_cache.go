package redisstack

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/chitandabb/GoAgent/internal/semanticcache"
	"github.com/google/uuid"
	rediscli "github.com/redis/go-redis/v9"
)

const vectorDimensions = 1024

var enforceCapacityScript = rediscli.NewScript(`
local excess = redis.call('ZCARD', KEYS[1]) - tonumber(ARGV[1])
if excess <= 0 then
  return 0
end
local popped = redis.call('ZPOPMIN', KEYS[1], excess)
for index = 1, #popped, 2 do
  redis.call('DEL', popped[index])
end
return #popped / 2
`)

type Authority interface {
	CurrentGeneration(context.Context) (int64, error)
	AuthorizePut(context.Context, semanticcache.PutInput) (int64, error)
	AuthorizeSemanticIndex(context.Context, semanticcache.SemanticIndexInput) (int64, error)
}

type Config struct {
	IndexName      string
	KeyPrefix      string
	MaxRecords     int
	TTLJitterRatio float64
}

type SemanticAnswerCache struct {
	client         *rediscli.Client
	authority      Authority
	indexName      string
	keyPrefix      string
	capacityKey    string
	maxRecords     int
	ttlJitterRatio float64
}

var _ semanticcache.SemanticProvider = (*SemanticAnswerCache)(nil)

func NewSemanticAnswerCache(
	ctx context.Context,
	client *rediscli.Client,
	authority Authority,
	cfg Config,
) (*SemanticAnswerCache, error) {
	if client == nil || authority == nil || strings.TrimSpace(cfg.IndexName) == "" ||
		strings.TrimSpace(cfg.KeyPrefix) == "" || cfg.MaxRecords < 1 || cfg.MaxRecords > 100_000 ||
		math.IsNaN(cfg.TTLJitterRatio) || math.IsInf(cfg.TTLJitterRatio, 0) ||
		cfg.TTLJitterRatio < 0 || cfg.TTLJitterRatio > 0.2 {
		return nil, errors.New("redis stack semantic answer cache configuration is invalid")
	}
	cache := &SemanticAnswerCache{
		client: client, authority: authority, indexName: strings.TrimSpace(cfg.IndexName),
		keyPrefix: strings.TrimSpace(cfg.KeyPrefix), maxRecords: cfg.MaxRecords,
		ttlJitterRatio: cfg.TTLJitterRatio,
	}
	cache.capacityKey = cache.keyPrefix + "capacity"
	if err := cache.ensureIndex(ctx); err != nil {
		return nil, err
	}
	return cache, nil
}

func (c *SemanticAnswerCache) Lookup(
	ctx context.Context,
	input semanticcache.LookupInput,
) (semanticcache.Answer, bool, error) {
	if c == nil || c.client == nil || c.authority == nil {
		return semanticcache.Answer{}, false, errors.New("redis stack semantic answer cache is unavailable")
	}
	if err := input.Validate(); err != nil {
		return semanticcache.Answer{}, false, err
	}
	generation, err := c.authority.CurrentGeneration(ctx)
	if err != nil {
		return semanticcache.Answer{}, false, err
	}
	values, err := c.client.HGetAll(ctx, c.answerKey(input.QuestionHash)).Result()
	if err != nil {
		return semanticcache.Answer{}, false, err
	}
	if len(values) == 0 {
		return semanticcache.Answer{}, false, nil
	}
	answer, _, err := decodeAnswer(values, semanticcache.LayerExact)
	if err != nil {
		return semanticcache.Answer{}, false, err
	}
	if answer.Generation != generation || !answer.ExpiresAt.After(input.Now.UTC()) {
		return semanticcache.Answer{}, false, nil
	}
	current, err := c.authority.CurrentGeneration(ctx)
	if err != nil {
		return semanticcache.Answer{}, false, err
	}
	if current != generation {
		return semanticcache.Answer{}, false, nil
	}
	return answer, true, nil
}

func (c *SemanticAnswerCache) Put(ctx context.Context, input semanticcache.PutInput) error {
	if c == nil || c.client == nil || c.authority == nil {
		return errors.New("redis stack semantic answer cache is unavailable")
	}
	if err := input.Validate(); err != nil {
		return err
	}
	generation, err := c.authority.AuthorizePut(ctx, input)
	if err != nil {
		return err
	}
	values, expiresAt, err := encodeAnswer(input, generation, c.ttlJitterRatio)
	if err != nil {
		return err
	}
	key := c.answerKey(input.QuestionHash)
	pipe := c.client.TxPipeline()
	pipe.HSet(ctx, key, values)
	pipe.ExpireAt(ctx, key, expiresAt)
	pipe.ZAdd(ctx, c.capacityKey, rediscli.Z{Score: float64(expiresAt.UnixMilli()), Member: key})
	if _, err = pipe.Exec(ctx); err != nil {
		return err
	}
	return c.enforceCapacity(ctx)
}

func (c *SemanticAnswerCache) LookupSemantic(
	ctx context.Context,
	input semanticcache.SemanticLookupInput,
) (semanticcache.Answer, bool, error) {
	if c == nil || c.client == nil || c.authority == nil {
		return semanticcache.Answer{}, false, errors.New("redis stack semantic answer cache is unavailable")
	}
	if err := input.Validate(vectorDimensions, true); err != nil {
		return semanticcache.Answer{}, false, err
	}
	generation, err := c.authority.CurrentGeneration(ctx)
	if err != nil {
		return semanticcache.Answer{}, false, err
	}
	records, err := c.search(ctx, generation, input)
	if err != nil {
		return semanticcache.Answer{}, false, err
	}
	for _, record := range records {
		answer, question, decodeErr := decodeAnswer(record.fields, semanticcache.LayerSemantic)
		if decodeErr != nil {
			return semanticcache.Answer{}, false, decodeErr
		}
		answer.Similarity = 1 - record.distance
		if answer.Generation != generation || !answer.ExpiresAt.After(input.Now.UTC()) ||
			answer.Similarity < input.MinimumSimilarity ||
			!semanticcache.CompareQuestions(input.Question, question).Compatible {
			continue
		}
		current, currentErr := c.authority.CurrentGeneration(ctx)
		if currentErr != nil {
			return semanticcache.Answer{}, false, currentErr
		}
		if current != generation {
			return semanticcache.Answer{}, false, nil
		}
		return answer, true, nil
	}
	return semanticcache.Answer{}, false, nil
}

func (c *SemanticAnswerCache) IndexSemantic(ctx context.Context, input semanticcache.SemanticIndexInput) error {
	if c == nil || c.client == nil || c.authority == nil {
		return errors.New("redis stack semantic answer cache is unavailable")
	}
	if err := input.Validate(vectorDimensions, true); err != nil {
		return err
	}
	boundHash, err := semanticcache.ExactQuestionKey(input.Question)
	if err != nil || boundHash != input.QuestionHash {
		return semanticcache.ErrInvalidRecord
	}
	generation, err := c.authority.AuthorizeSemanticIndex(ctx, input)
	if err != nil {
		return err
	}
	key := c.answerKey(input.QuestionHash)
	values, err := c.client.HGetAll(ctx, key).Result()
	if err != nil {
		return err
	}
	answer, _, err := decodeAnswer(values, semanticcache.LayerExact)
	if err != nil || answer.Generation != generation || answer.SourceRunID != input.SourceRunID {
		return semanticcache.ErrInvalidRecord
	}
	return c.client.HSet(ctx, key, map[string]any{
		"question_text":                 strings.TrimSpace(input.Question),
		"embedding_profile_id":          input.ProfileID.String(),
		"embedding_profile_fingerprint": input.ProfileFingerprint,
		"normalization_version":         input.NormalizationVersion,
		"question_embedding":            encodeVector(input.Vector),
	}).Err()
}

func (c *SemanticAnswerCache) ensureIndex(ctx context.Context) error {
	args := []any{
		"FT.CREATE", c.indexName, "ON", "HASH", "PREFIX", 1, c.keyPrefix, "SCHEMA",
		"generation", "NUMERIC",
		"embedding_profile_id", "TAG",
		"embedding_profile_fingerprint", "TAG",
		"normalization_version", "TAG",
		"question_embedding", "VECTOR", "HNSW", 6,
		"TYPE", "FLOAT32", "DIM", vectorDimensions, "DISTANCE_METRIC", "COSINE",
	}
	if err := c.client.Do(ctx, args...).Err(); err != nil && !strings.Contains(strings.ToLower(err.Error()), "index already exists") {
		return fmt.Errorf("create redis stack semantic cache index: %w", err)
	}
	return c.validateIndex(ctx)
}

func (c *SemanticAnswerCache) validateIndex(ctx context.Context) error {
	result, err := c.client.Do(ctx, "FT.INFO", c.indexName).Result()
	if err != nil {
		return fmt.Errorf("inspect redis stack semantic cache index: %w", err)
	}
	info, ok := redisPairs(result)
	if !ok {
		return errors.New("redis stack semantic cache index metadata is invalid")
	}
	definition, ok := redisPairs(info["index_definition"])
	if !ok || !redisEquals(definition["key_type"], "HASH") {
		return errors.New("redis stack semantic cache index key type is incompatible")
	}
	prefixes, ok := definition["prefixes"].([]any)
	if !ok || len(prefixes) != 1 || !redisEquals(prefixes[0], c.keyPrefix) {
		return errors.New("redis stack semantic cache index prefix is incompatible")
	}
	attributes, ok := info["attributes"].([]any)
	if !ok {
		return errors.New("redis stack semantic cache index attributes are invalid")
	}
	want := map[string]string{
		"generation": "NUMERIC", "embedding_profile_id": "TAG",
		"embedding_profile_fingerprint": "TAG", "normalization_version": "TAG",
	}
	vectorValid := false
	for _, rawAttribute := range attributes {
		attribute, valid := redisPairs(rawAttribute)
		if !valid {
			return errors.New("redis stack semantic cache index attribute is invalid")
		}
		identifier, identifierOK := redisString(attribute["identifier"])
		if !identifierOK {
			continue
		}
		if expectedType, required := want[identifier]; required && redisEquals(attribute["type"], expectedType) {
			delete(want, identifier)
		}
		if identifier == "question_embedding" && redisEquals(attribute["type"], "VECTOR") &&
			redisEquals(attribute["algorithm"], "HNSW") && redisEquals(attribute["data_type"], "FLOAT32") &&
			redisInteger(attribute["dim"]) == vectorDimensions && redisEquals(attribute["distance_metric"], "COSINE") {
			vectorValid = true
		}
	}
	if len(want) != 0 || !vectorValid {
		return errors.New("redis stack semantic cache index schema is incompatible")
	}
	return nil
}

type searchRecord struct {
	distance float64
	fields   map[string]string
}

func (c *SemanticAnswerCache) search(
	ctx context.Context,
	generation int64,
	input semanticcache.SemanticLookupInput,
) ([]searchRecord, error) {
	query := fmt.Sprintf(
		"(@generation:[%d %d] @embedding_profile_id:{%s} @embedding_profile_fingerprint:{%s} @normalization_version:{%s})=>[KNN %d @question_embedding $vector AS distance]",
		generation, generation, escapeTag(input.ProfileID.String()), escapeTag(input.ProfileFingerprint),
		escapeTag(input.NormalizationVersion), input.CandidateLimit,
	)
	fields := []any{
		"answer_content", "citations", "retrieved_sources", "source_run_id", "model_provider", "model_id",
		"prompt_version", "generation", "created_at", "expires_at", "question_text", "distance",
	}
	args := []any{"FT.SEARCH", c.indexName, query, "PARAMS", 2, "vector", encodeVector(input.Vector),
		"SORTBY", "distance", "RETURN", len(fields)}
	args = append(args, fields...)
	args = append(args, "LIMIT", 0, input.CandidateLimit, "DIALECT", 2)
	result, err := c.client.Do(ctx, args...).Result()
	if err != nil {
		return nil, err
	}
	return parseSearchResult(result)
}

func parseSearchResult(value any) ([]searchRecord, error) {
	items, ok := value.([]any)
	if !ok || len(items) == 0 || len(items)%2 != 1 {
		return nil, semanticcache.ErrInvalidRecord
	}
	total, totalOK := redisInt64(items[0])
	if !totalOK || total < 0 || total > 0 && len(items) == 1 || total < int64((len(items)-1)/2) {
		return nil, semanticcache.ErrInvalidRecord
	}
	records := make([]searchRecord, 0, (len(items)-1)/2)
	for index := 1; index+1 < len(items); index += 2 {
		if _, valid := redisString(items[index]); !valid {
			return nil, semanticcache.ErrInvalidRecord
		}
		fieldItems, ok := items[index+1].([]any)
		if !ok || len(fieldItems)%2 != 0 {
			return nil, semanticcache.ErrInvalidRecord
		}
		fields := make(map[string]string, len(fieldItems)/2)
		for fieldIndex := 0; fieldIndex < len(fieldItems); fieldIndex += 2 {
			name, nameOK := redisString(fieldItems[fieldIndex])
			fieldValue, valueOK := redisString(fieldItems[fieldIndex+1])
			if !nameOK || !valueOK {
				return nil, semanticcache.ErrInvalidRecord
			}
			fields[name] = fieldValue
		}
		distance, err := strconv.ParseFloat(fields["distance"], 64)
		if err != nil || math.IsNaN(distance) || math.IsInf(distance, 0) {
			return nil, semanticcache.ErrInvalidRecord
		}
		records = append(records, searchRecord{distance: distance, fields: fields})
	}
	return records, nil
}

func encodeAnswer(input semanticcache.PutInput, generation int64, jitterRatio float64) (map[string]any, time.Time, error) {
	citations, err := json.Marshal(input.Answer.Citations)
	if err != nil {
		return nil, time.Time{}, fmt.Errorf("encode semantic cache citations: %w", err)
	}
	retrieved, err := json.Marshal(input.Answer.RetrievedSources)
	if err != nil {
		return nil, time.Time{}, fmt.Errorf("encode semantic cache retrieved sources: %w", err)
	}
	createdAt := input.Answer.CreatedAt.UTC()
	expiresAt := createdAt.Add(jitterTTL(input.TTL, jitterRatio, input.QuestionHash))
	return map[string]any{
		"answer_content": input.Answer.Content, "citations": citations, "retrieved_sources": retrieved,
		"source_run_id": input.Answer.SourceRunID.String(), "model_provider": input.Answer.ModelProvider,
		"model_id": input.Answer.ModelID, "prompt_version": input.Answer.PromptVersion,
		"generation": generation, "created_at": createdAt.UnixNano(), "expires_at": expiresAt.UnixNano(),
	}, expiresAt, nil
}

func decodeAnswer(values map[string]string, layer string) (semanticcache.Answer, string, error) {
	if len(values) == 0 {
		return semanticcache.Answer{}, "", semanticcache.ErrInvalidRecord
	}
	generation, generationErr := strconv.ParseInt(values["generation"], 10, 64)
	createdAt, createdErr := strconv.ParseInt(values["created_at"], 10, 64)
	expiresAt, expiresErr := strconv.ParseInt(values["expires_at"], 10, 64)
	sourceRunID, sourceErr := uuid.Parse(values["source_run_id"])
	if generationErr != nil || createdErr != nil || expiresErr != nil || sourceErr != nil || generation < 1 ||
		createdAt <= 0 || expiresAt <= createdAt ||
		strings.TrimSpace(values["answer_content"]) == "" || len(values["answer_content"]) > semanticcache.MaxAnswerBytes {
		return semanticcache.Answer{}, "", semanticcache.ErrInvalidRecord
	}
	var citations, retrieved []semanticcache.Source
	if err := strictJSON([]byte(values["citations"]), &citations); err != nil {
		return semanticcache.Answer{}, "", err
	}
	if err := strictJSON([]byte(values["retrieved_sources"]), &retrieved); err != nil {
		return semanticcache.Answer{}, "", err
	}
	if len(citations) == 0 || len(citations) > semanticcache.MaxCitations ||
		len(retrieved) == 0 || len(retrieved) > semanticcache.MaxSources {
		return semanticcache.Answer{}, "", semanticcache.ErrInvalidRecord
	}
	return semanticcache.Answer{
		Content: values["answer_content"], Citations: citations, RetrievedSources: retrieved,
		SourceRunID: sourceRunID, ModelProvider: values["model_provider"], ModelID: values["model_id"],
		PromptVersion: values["prompt_version"], Generation: generation,
		CreatedAt: time.Unix(0, createdAt).UTC(), ExpiresAt: time.Unix(0, expiresAt).UTC(), Layer: layer,
	}, values["question_text"], nil
}

func strictJSON(data []byte, target any) error {
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return errors.Join(semanticcache.ErrInvalidRecord, err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errors.Join(semanticcache.ErrInvalidRecord, err)
	}
	return nil
}

func (c *SemanticAnswerCache) enforceCapacity(ctx context.Context) error {
	return enforceCapacityScript.Run(ctx, c.client, []string{c.capacityKey}, c.maxRecords).Err()
}

func (c *SemanticAnswerCache) answerKey(questionHash string) string {
	return c.keyPrefix + "answer:" + questionHash
}

func encodeVector(vector []float32) []byte {
	data := make([]byte, len(vector)*4)
	for index, value := range vector {
		binary.LittleEndian.PutUint32(data[index*4:], math.Float32bits(value))
	}
	return data
}

func escapeTag(value string) string {
	var escaped strings.Builder
	for _, current := range value {
		if !unicode.IsLetter(current) && !unicode.IsDigit(current) && current != '_' {
			escaped.WriteByte('\\')
		}
		escaped.WriteRune(current)
	}
	return escaped.String()
}

func redisString(value any) (string, bool) {
	switch current := value.(type) {
	case string:
		return current, true
	case []byte:
		return string(current), true
	default:
		return "", false
	}
}

func redisPairs(value any) (map[string]any, bool) {
	items, ok := value.([]any)
	if !ok || len(items)%2 != 0 {
		return nil, false
	}
	result := make(map[string]any, len(items)/2)
	for index := 0; index < len(items); index += 2 {
		key, valid := redisString(items[index])
		if !valid {
			return nil, false
		}
		result[key] = items[index+1]
	}
	return result, true
}

func redisEquals(value any, expected string) bool {
	actual, ok := redisString(value)
	return ok && strings.EqualFold(actual, expected)
}

func redisInteger(value any) int {
	parsed, _ := redisInt64(value)
	return int(parsed)
}

func redisInt64(value any) (int64, bool) {
	switch current := value.(type) {
	case int64:
		return current, true
	case uint64:
		if current > math.MaxInt64 {
			return 0, false
		}
		return int64(current), true
	case int:
		return int64(current), true
	case string:
		parsed, err := strconv.ParseInt(current, 10, 64)
		return parsed, err == nil
	case []byte:
		parsed, err := strconv.ParseInt(string(current), 10, 64)
		return parsed, err == nil
	default:
		return 0, false
	}
}

func jitterTTL(ttl time.Duration, ratio float64, questionHash string) time.Duration {
	if ttl <= 0 || ratio <= 0 || len(questionHash) < 16 {
		return ttl
	}
	prefix, err := strconv.ParseUint(questionHash[:16], 16, 64)
	if err != nil {
		return ttl
	}
	fraction := float64(prefix) / float64(^uint64(0))
	return time.Duration(float64(ttl) * (1 - ratio + 2*ratio*fraction))
}
