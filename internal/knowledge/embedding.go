package knowledge

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"

	"github.com/google/uuid"
)

type EmbeddingInputType string

const (
	EmbeddingInputQuery    EmbeddingInputType = "query"
	EmbeddingInputDocument EmbeddingInputType = "document"
)

type EmbeddingProfile struct {
	ID                uuid.UUID
	Key               string
	Provider          string
	Model             string
	Dimensions        int
	DistanceMetric    string
	QueryInputType    EmbeddingInputType
	DocumentInputType EmbeddingInputType
	Normalize         bool
	ConfigVersion     string
	Fingerprint       string
}

func NewEmbeddingProfile(
	key, provider, model string,
	dimensions int,
	distanceMetric string,
	queryInputType, documentInputType EmbeddingInputType,
	normalize bool,
	configVersion string,
) (EmbeddingProfile, error) {
	profile := EmbeddingProfile{
		Key: strings.TrimSpace(key), Provider: strings.TrimSpace(provider), Model: strings.TrimSpace(model),
		Dimensions: dimensions, DistanceMetric: strings.ToLower(strings.TrimSpace(distanceMetric)),
		QueryInputType: queryInputType, DocumentInputType: documentInputType,
		Normalize: normalize, ConfigVersion: strings.TrimSpace(configVersion),
	}
	if err := profile.validateIdentity(); err != nil {
		return EmbeddingProfile{}, err
	}
	canonical, err := json.Marshal(struct {
		Key               string             `json:"key"`
		Provider          string             `json:"provider"`
		Model             string             `json:"model"`
		Dimensions        int                `json:"dimensions"`
		DistanceMetric    string             `json:"distanceMetric"`
		QueryInputType    EmbeddingInputType `json:"queryInputType"`
		DocumentInputType EmbeddingInputType `json:"documentInputType"`
		Normalize         bool               `json:"normalize"`
		ConfigVersion     string             `json:"configVersion"`
	}{
		Key: profile.Key, Provider: profile.Provider, Model: profile.Model,
		Dimensions: profile.Dimensions, DistanceMetric: profile.DistanceMetric,
		QueryInputType: profile.QueryInputType, DocumentInputType: profile.DocumentInputType,
		Normalize: profile.Normalize, ConfigVersion: profile.ConfigVersion,
	})
	if err != nil {
		return EmbeddingProfile{}, fmt.Errorf("encode embedding profile: %w", err)
	}
	digest := sha256.Sum256(canonical)
	profile.Fingerprint = hex.EncodeToString(digest[:])
	profile.ID = uuid.NewSHA1(uuid.NameSpaceURL, []byte("mesguard:embedding-profile:"+profile.Fingerprint))
	return profile, nil
}

func (p EmbeddingProfile) Validate() error {
	if p.ID == uuid.Nil || !validSHA256Hex(p.Fingerprint) {
		return errors.New("embedding profile stable identity is invalid")
	}
	if err := p.validateIdentity(); err != nil {
		return err
	}
	rebuilt, err := NewEmbeddingProfile(
		p.Key, p.Provider, p.Model, p.Dimensions, p.DistanceMetric,
		p.QueryInputType, p.DocumentInputType, p.Normalize, p.ConfigVersion,
	)
	if err != nil {
		return err
	}
	if rebuilt.ID != p.ID || rebuilt.Fingerprint != p.Fingerprint {
		return errors.New("embedding profile fingerprint does not match its configuration")
	}
	return nil
}

func (p EmbeddingProfile) validateIdentity() error {
	for name, value := range map[string]string{
		"key": p.Key, "provider": p.Provider, "model": p.Model, "config version": p.ConfigVersion,
	} {
		if value == "" || len(value) > 128 || value != strings.TrimSpace(value) {
			return fmt.Errorf("embedding profile %s is invalid", name)
		}
	}
	if p.Dimensions < 1 || p.Dimensions > 4096 {
		return errors.New("embedding profile dimensions are invalid")
	}
	if p.DistanceMetric != "cosine" {
		return errors.New("embedding profile distance metric must be cosine")
	}
	if p.QueryInputType != EmbeddingInputQuery || p.DocumentInputType != EmbeddingInputDocument {
		return errors.New("embedding profile must distinguish query and document input")
	}
	return nil
}

type EmbeddingRequest struct {
	Texts     []string
	InputType EmbeddingInputType
}

func (r EmbeddingRequest) Validate(maxBatch int) error {
	if maxBatch < 1 || len(r.Texts) < 1 || len(r.Texts) > maxBatch {
		return errors.New("embedding request batch is invalid")
	}
	if r.InputType != EmbeddingInputQuery && r.InputType != EmbeddingInputDocument {
		return errors.New("embedding request input type is invalid")
	}
	for _, text := range r.Texts {
		if strings.TrimSpace(text) == "" || strings.ContainsRune(text, 0) || len([]rune(text)) > 32_000 {
			return errors.New("embedding request text is invalid")
		}
	}
	return nil
}

type EmbeddingUsage struct {
	TotalTokens int
}

type EmbeddingResult struct {
	Vectors [][]float32
	Usage   EmbeddingUsage
}

func (r EmbeddingResult) Validate(expected, dimensions int, normalized bool) error {
	if expected < 1 || dimensions < 1 || len(r.Vectors) != expected || r.Usage.TotalTokens < 0 {
		return errors.New("embedding result dimensions are invalid")
	}
	for _, vector := range r.Vectors {
		if err := ValidateEmbeddingVector(vector, dimensions, normalized); err != nil {
			return err
		}
	}
	return nil
}

func ValidateEmbeddingVector(vector []float32, dimensions int, normalized bool) error {
	if dimensions < 1 || len(vector) != dimensions {
		return errors.New("embedding vector has an unexpected dimension")
	}
	var squaredNorm float64
	for _, value := range vector {
		if math.IsNaN(float64(value)) || math.IsInf(float64(value), 0) {
			return errors.New("embedding vector contains a non-finite value")
		}
		squaredNorm += float64(value) * float64(value)
	}
	if squaredNorm == 0 {
		return errors.New("embedding vector has zero norm")
	}
	if normalized && math.Abs(math.Sqrt(squaredNorm)-1) > 0.002 {
		return errors.New("embedding vector is not normalized")
	}
	return nil
}

type Embedder interface {
	Embed(context.Context, EmbeddingRequest) (EmbeddingResult, error)
}

type ChunkEmbeddingDraft struct {
	ChunkOrdinal  int
	ContentSHA256 string
	Vector        []float32
}

func (d ChunkEmbeddingDraft) Validate(profile EmbeddingProfile) error {
	if err := profile.Validate(); err != nil {
		return err
	}
	if d.ChunkOrdinal < 0 || !validSHA256Hex(d.ContentSHA256) {
		return errors.New("chunk embedding identity is invalid")
	}
	return ValidateEmbeddingVector(d.Vector, profile.Dimensions, profile.Normalize)
}
