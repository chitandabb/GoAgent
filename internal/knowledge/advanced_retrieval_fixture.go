package knowledge

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"
	"time"
)

const AdvancedRetrievalMarkdownChunkerV1 = "markdown-rune-v1"

type AdvancedRetrievalEvaluationDocument struct {
	DocumentKey       string `json:"documentKey"`
	Title             string `json:"title"`
	MediaType         string `json:"mediaType"`
	SourceURL         string `json:"sourceUrl"`
	SourceRetrievedAt string `json:"sourceRetrievedAt"`
	ContentSHA256     string `json:"contentSha256"`
	Content           string `json:"content"`
}

func (d AdvancedRetrievalEvaluationDocument) Validate() error {
	if !retrievalEvaluationIDPattern.MatchString(d.DocumentKey) ||
		strings.TrimSpace(d.Title) == "" || d.Title != strings.TrimSpace(d.Title) || len([]rune(d.Title)) > 512 ||
		d.MediaType != "text/markdown" || strings.TrimSpace(d.Content) == "" || d.Content != strings.TrimSpace(d.Content) ||
		len([]rune(d.Content)) > 100_000 || !validSHA256Hex(d.ContentSHA256) || d.ContentSHA256 != SHA256Hex(d.Content) {
		return errors.New("advanced retrieval corpus document is invalid")
	}
	parsedURL, err := url.Parse(d.SourceURL)
	if err != nil || parsedURL.Scheme != "https" || parsedURL.Host == "" || parsedURL.User != nil {
		return errors.New("advanced retrieval corpus source URL is invalid")
	}
	hostname := strings.ToLower(parsedURL.Hostname())
	if hostname == "localhost" || strings.HasSuffix(hostname, ".localhost") {
		return errors.New("advanced retrieval corpus source URL is not public")
	}
	if address := net.ParseIP(hostname); address != nil &&
		(address.IsLoopback() || address.IsPrivate() || address.IsLinkLocalUnicast() || address.IsUnspecified()) {
		return errors.New("advanced retrieval corpus source URL is not public")
	}
	if _, err := time.Parse("2006-01-02", d.SourceRetrievedAt); err != nil {
		return errors.New("advanced retrieval corpus source retrieval date is invalid")
	}
	return nil
}

type AdvancedRetrievalEvaluationCorpus struct {
	DatasetVersion    string                                `json:"datasetVersion"`
	ChunkerVersion    string                                `json:"chunkerVersion"`
	ChunkMaxRunes     int                                   `json:"chunkMaxRunes"`
	ChunkOverlapRunes int                                   `json:"chunkOverlapRunes"`
	Documents         []AdvancedRetrievalEvaluationDocument `json:"documents"`
}

func (c AdvancedRetrievalEvaluationCorpus) Validate() error {
	if !retrievalEvaluationIDPattern.MatchString(c.DatasetVersion) ||
		c.ChunkerVersion != AdvancedRetrievalMarkdownChunkerV1 || len(c.Documents) < 2 || len(c.Documents) > 100 {
		return errors.New("advanced retrieval corpus dimensions are invalid")
	}
	options := TextChunkOptions{MaxRunes: c.ChunkMaxRunes, OverlapRunes: c.ChunkOverlapRunes}
	seen := make(map[string]struct{}, len(c.Documents))
	for index, document := range c.Documents {
		if err := document.Validate(); err != nil {
			return fmt.Errorf("advanced retrieval corpus document %d: %w", index, err)
		}
		if _, exists := seen[document.DocumentKey]; exists {
			return errors.New("advanced retrieval corpus document keys must be unique")
		}
		seen[document.DocumentKey] = struct{}{}
		if _, err := ChunkMarkdown(document.Content, options); err != nil {
			return fmt.Errorf("chunk advanced retrieval document %q: %w", document.DocumentKey, err)
		}
	}
	return nil
}

func BuildAdvancedRetrievalCorpusChunks(
	corpus AdvancedRetrievalEvaluationCorpus,
) (map[string][]ChunkDraft, error) {
	if err := corpus.Validate(); err != nil {
		return nil, err
	}
	options := TextChunkOptions{MaxRunes: corpus.ChunkMaxRunes, OverlapRunes: corpus.ChunkOverlapRunes}
	result := make(map[string][]ChunkDraft, len(corpus.Documents))
	for _, document := range corpus.Documents {
		chunks, err := ChunkMarkdown(document.Content, options)
		if err != nil {
			return nil, err
		}
		result[document.DocumentKey] = chunks
	}
	return result, nil
}

func ValidateAdvancedRetrievalFixture(
	corpus AdvancedRetrievalEvaluationCorpus,
	cases []AdvancedRetrievalEvaluationCase,
) (map[string][]ChunkDraft, error) {
	chunksByDocument, err := BuildAdvancedRetrievalCorpusChunks(corpus)
	if err != nil {
		return nil, err
	}
	if _, datasetVersion, _, err := indexAdvancedRetrievalCases(cases); err != nil {
		return nil, err
	} else if datasetVersion != corpus.DatasetVersion {
		return nil, errors.New("advanced retrieval corpus and cases use different dataset versions")
	}
	for _, definition := range cases {
		relevantChunkDocuments := make(map[string]struct{}, len(definition.RelevantDocumentKeys))
		for _, reference := range definition.RelevantChunks {
			chunks, exists := chunksByDocument[reference.DocumentKey]
			if !exists || reference.Ordinal >= len(chunks) || chunks[reference.Ordinal].ContentSHA256 != reference.ContentSHA256 {
				return nil, fmt.Errorf("case %q contains a stale gold chunk", definition.CaseID)
			}
			relevantChunkDocuments[reference.DocumentKey] = struct{}{}
		}
		for _, documentKey := range definition.RelevantDocumentKeys {
			if _, exists := chunksByDocument[documentKey]; !exists {
				return nil, fmt.Errorf("case %q references an unknown document", definition.CaseID)
			}
			if _, exists := relevantChunkDocuments[documentKey]; !exists {
				return nil, fmt.Errorf("case %q has no gold chunk for relevant document %q", definition.CaseID, documentKey)
			}
		}
	}
	return chunksByDocument, nil
}
