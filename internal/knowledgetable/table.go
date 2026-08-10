// Package knowledgetable defines the provider-neutral contract for recovering
// searchable table structure from a bounded layout-region crop.
package knowledgetable

import (
	"context"
	"errors"
	"math"
	"strings"
	"unicode/utf8"

	"github.com/chitandabb/GoAgent/internal/knowledgeparser"
)

const (
	maxTableCells       = 10_000
	maxTableRunes       = 100_000
	maxTableWarnings    = 32
	maxTableWarningSize = 256
)

var (
	ErrUnavailable  = errors.New("knowledge table recovery is unavailable")
	ErrInvalidInput = errors.New("knowledge table recovery input is invalid")
)

type Request struct {
	Asset  knowledgeparser.VisualAsset
	Reason string
}

func (r Request) Validate() error {
	if err := r.Asset.Validate(); err != nil {
		return err
	}
	if r.Asset.Kind != knowledgeparser.VisualAssetLayoutRegion || r.Asset.PageNumber == nil ||
		(r.Asset.MediaType != "image/png" && r.Asset.MediaType != "image/jpeg") || len(r.Asset.Content) == 0 {
		return ErrInvalidInput
	}
	reason := strings.TrimSpace(r.Reason)
	if reason == "" || reason != r.Reason || len(reason) > 256 || strings.ContainsRune(reason, 0) {
		return ErrInvalidInput
	}
	return nil
}

type Cell struct {
	Row        int    `json:"row"`
	Column     int    `json:"column"`
	RowSpan    int    `json:"rowSpan"`
	ColumnSpan int    `json:"columnSpan"`
	Text       string `json:"text"`
	Header     bool   `json:"header"`
}

type Usage struct {
	PromptTokens     int `json:"promptTokens"`
	CompletionTokens int `json:"completionTokens"`
	TotalTokens      int `json:"totalTokens"`
}

func (u Usage) Validate() error {
	if u.PromptTokens < 0 || u.CompletionTokens < 0 || u.TotalTokens < 0 ||
		u.TotalTokens < u.PromptTokens || u.TotalTokens-u.PromptTokens < u.CompletionTokens {
		return errors.New("knowledge table recovery usage is invalid")
	}
	return nil
}

type Result struct {
	Provider      string   `json:"provider"`
	Model         string   `json:"model"`
	PromptVersion string   `json:"promptVersion"`
	Markdown      string   `json:"markdown"`
	Cells         []Cell   `json:"cells"`
	Confidence    float64  `json:"confidence"`
	Warnings      []string `json:"warnings"`
	Partial       bool     `json:"partial"`
	Reason        string   `json:"reason,omitempty"`
	Usage         *Usage   `json:"usage,omitempty"`
}

func (r Result) Validate() error {
	if !validIdentity(r.Provider, 64) || !validIdentity(r.Model, 256) ||
		!validIdentity(r.PromptVersion, 128) {
		return errors.New("knowledge table recovery identity is invalid")
	}
	markdown := strings.TrimSpace(r.Markdown)
	if markdown == "" || markdown != r.Markdown || strings.ContainsRune(markdown, 0) ||
		utf8.RuneCountInString(markdown) > maxTableRunes {
		return errors.New("knowledge table recovery markdown is invalid")
	}
	if len(r.Cells) == 0 || len(r.Cells) > maxTableCells ||
		math.IsNaN(r.Confidence) || math.IsInf(r.Confidence, 0) || r.Confidence < 0 || r.Confidence > 1 {
		return errors.New("knowledge table recovery structure is invalid")
	}
	seen := make(map[[2]int]struct{}, len(r.Cells))
	cellRunes, nonEmptyCells := 0, 0
	for _, cell := range r.Cells {
		if cell.Row < 0 || cell.Column < 0 || cell.Row > maxTableCells || cell.Column > maxTableCells ||
			cell.RowSpan < 1 || cell.ColumnSpan < 1 || cell.RowSpan > maxTableCells || cell.ColumnSpan > maxTableCells ||
			cell.Row+cell.RowSpan > maxTableCells || cell.Column+cell.ColumnSpan > maxTableCells ||
			strings.ContainsRune(cell.Text, 0) {
			return errors.New("knowledge table recovery cell is invalid")
		}
		coordinate := [2]int{cell.Row, cell.Column}
		if _, duplicate := seen[coordinate]; duplicate {
			return errors.New("knowledge table recovery cell coordinate is duplicated")
		}
		seen[coordinate] = struct{}{}
		cellRunes += utf8.RuneCountInString(cell.Text)
		if strings.TrimSpace(cell.Text) != "" {
			nonEmptyCells++
		}
		if cellRunes > maxTableRunes {
			return errors.New("knowledge table recovery cell text exceeds limit")
		}
	}
	if nonEmptyCells == 0 {
		return errors.New("knowledge table recovery has no non-empty cells")
	}
	if len(r.Warnings) > maxTableWarnings {
		return errors.New("knowledge table recovery warnings exceed limit")
	}
	for _, warning := range r.Warnings {
		trimmed := strings.TrimSpace(warning)
		if trimmed == "" || trimmed != warning || len(warning) > maxTableWarningSize || strings.ContainsRune(warning, 0) {
			return errors.New("knowledge table recovery warning is invalid")
		}
	}
	if r.Partial {
		reason := strings.TrimSpace(r.Reason)
		if reason == "" || reason != r.Reason || len(reason) > 256 || strings.ContainsRune(reason, 0) {
			return errors.New("knowledge table recovery partial reason is invalid")
		}
	} else if r.Reason != "" {
		return errors.New("knowledge table recovery complete result has a reason")
	}
	if r.Usage != nil {
		if err := r.Usage.Validate(); err != nil {
			return err
		}
	}
	return nil
}

func validIdentity(value string, maximum int) bool {
	trimmed := strings.TrimSpace(value)
	return trimmed != "" && trimmed == value && len(value) <= maximum && !strings.ContainsRune(value, 0)
}

type Processor interface {
	Recover(context.Context, Request) (Result, error)
}
