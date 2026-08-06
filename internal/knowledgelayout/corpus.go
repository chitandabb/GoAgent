package knowledgelayout

import (
	"errors"
	"fmt"
	"net/url"
	"path/filepath"
	"strings"
)

type RoutingCorpus struct {
	DatasetVersion string                  `json:"datasetVersion"`
	Documents      []RoutingCorpusDocument `json:"documents"`
}

type RoutingCorpusDocument struct {
	DocumentID         string   `json:"documentId"`
	Title              string   `json:"title"`
	Publisher          string   `json:"publisher"`
	SourceURL          string   `json:"sourceUrl"`
	DownloadURL        string   `json:"downloadUrl"`
	UsageBasis         string   `json:"usageBasis"`
	FileName           string   `json:"fileName"`
	MediaType          string   `json:"mediaType"`
	SizeBytes          int64    `json:"sizeBytes"`
	SHA256             string   `json:"sha256"`
	PageCount          *int     `json:"pageCount,omitempty"`
	EmbeddedMediaCount int      `json:"embeddedMediaCount,omitempty"`
	Coverage           []string `json:"coverage"`
}

func (c RoutingCorpus) Validate() error {
	if !evaluationIDPattern.MatchString(c.DatasetVersion) || len(c.Documents) == 0 || len(c.Documents) > 100 {
		return errors.New("routing corpus identity is invalid")
	}
	seenIDs := make(map[string]struct{}, len(c.Documents))
	seenFiles := make(map[string]struct{}, len(c.Documents))
	for index, document := range c.Documents {
		if err := document.Validate(); err != nil {
			return fmt.Errorf("routing corpus document %d: %w", index, err)
		}
		if _, exists := seenIDs[document.DocumentID]; exists {
			return fmt.Errorf("duplicate routing corpus documentId %q", document.DocumentID)
		}
		if _, exists := seenFiles[document.FileName]; exists {
			return fmt.Errorf("duplicate routing corpus fileName %q", document.FileName)
		}
		seenIDs[document.DocumentID] = struct{}{}
		seenFiles[document.FileName] = struct{}{}
	}
	return nil
}

func (d RoutingCorpusDocument) Validate() error {
	if !evaluationIDPattern.MatchString(d.DocumentID) || strings.TrimSpace(d.Title) == "" ||
		strings.TrimSpace(d.Publisher) == "" || strings.TrimSpace(d.UsageBasis) == "" ||
		len([]rune(d.Title)) > 512 || len([]rune(d.Publisher)) > 256 || len([]rune(d.UsageBasis)) > 1000 {
		return errors.New("routing corpus document metadata is invalid")
	}
	if filepath.Base(d.FileName) != d.FileName || strings.TrimSpace(d.FileName) != d.FileName ||
		len(d.FileName) > 256 || strings.ContainsRune(d.FileName, '\x00') {
		return errors.New("routing corpus fileName is invalid")
	}
	if d.MediaType != "application/pdf" &&
		d.MediaType != "application/vnd.openxmlformats-officedocument.wordprocessingml.document" &&
		d.MediaType != "application/vnd.openxmlformats-officedocument.presentationml.presentation" {
		return errors.New("routing corpus mediaType is invalid")
	}
	if d.SizeBytes < 1 || !validSHA256(d.SHA256) || d.EmbeddedMediaCount < 0 || len(d.Coverage) == 0 || len(d.Coverage) > 16 {
		return errors.New("routing corpus file contract is invalid")
	}
	if d.PageCount != nil && *d.PageCount < 1 {
		return errors.New("routing corpus pageCount is invalid")
	}
	for _, rawURL := range []string{d.SourceURL, d.DownloadURL} {
		parsed, err := url.Parse(rawURL)
		if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil {
			return errors.New("routing corpus URL is invalid")
		}
	}
	seenCoverage := make(map[string]struct{}, len(d.Coverage))
	for _, coverage := range d.Coverage {
		if !evaluationIDPattern.MatchString(coverage) {
			return errors.New("routing corpus coverage label is invalid")
		}
		if _, exists := seenCoverage[coverage]; exists {
			return errors.New("routing corpus coverage labels must be unique")
		}
		seenCoverage[coverage] = struct{}{}
	}
	return nil
}

func (c RoutingCorpus) ValidateCases(cases []RoutingEvaluationCase) error {
	if err := c.Validate(); err != nil {
		return err
	}
	documents := make(map[string]RoutingCorpusDocument, len(c.Documents))
	for _, document := range c.Documents {
		documents[document.DocumentID] = document
	}
	seenLocations := make(map[string]struct{}, len(cases))
	for index, definition := range cases {
		if err := definition.Validate(); err != nil {
			return fmt.Errorf("routing evaluation case %d: %w", index, err)
		}
		if definition.DatasetVersion != c.DatasetVersion {
			return errors.New("routing evaluation case datasetVersion does not match corpus")
		}
		document, exists := documents[definition.DocumentID]
		if !exists || document.MediaType != "application/pdf" {
			return fmt.Errorf("routing evaluation case %q does not reference a PDF corpus document", definition.CaseID)
		}
		if document.PageCount != nil && definition.PageNumber > *document.PageCount {
			return fmt.Errorf("routing evaluation case %q page exceeds document pageCount", definition.CaseID)
		}
		location := fmt.Sprintf("%s:%d", definition.DocumentID, definition.PageNumber)
		if _, exists := seenLocations[location]; exists {
			return fmt.Errorf("duplicate routing evaluation page %s", location)
		}
		seenLocations[location] = struct{}{}
	}
	return nil
}
