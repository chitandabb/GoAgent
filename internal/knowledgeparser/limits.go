package knowledgeparser

import (
	"errors"
	"fmt"
	"unicode/utf8"
)

var ErrResourceLimit = errors.New("knowledge parser resource limit exceeded")

const maxParserElements = 10000

type Limits struct {
	MaxDocumentUnits      int
	MaxArchiveEntries     int
	MaxExpandedBytes      int64
	MaxXMLBytes           int64
	MaxExtractedRunes     int
	MaxSpreadsheetRows    int
	MaxSpreadsheetColumns int
	MaxVisualAssets       int
	MaxVisualAssetBytes   int64
	MaxTotalVisualBytes   int64
}

func (l Limits) Validate() error {
	if l.MaxDocumentUnits < 1 || l.MaxDocumentUnits > 5000 ||
		l.MaxArchiveEntries < 1 || l.MaxArchiveEntries > 20000 ||
		l.MaxExpandedBytes < 1024*1024 || l.MaxExpandedBytes > 1024*1024*1024 ||
		l.MaxXMLBytes < 64*1024 || l.MaxXMLBytes > l.MaxExpandedBytes ||
		l.MaxExtractedRunes < 1000 || l.MaxExtractedRunes > 10_000_000 ||
		l.MaxSpreadsheetRows < 1 || l.MaxSpreadsheetRows > 1_000_000 ||
		l.MaxSpreadsheetColumns < 1 || l.MaxSpreadsheetColumns > 16384 ||
		l.MaxVisualAssets < 1 || l.MaxVisualAssets > 10_000 ||
		l.MaxVisualAssetBytes < 1024 || l.MaxVisualAssetBytes > l.MaxExpandedBytes ||
		l.MaxTotalVisualBytes < l.MaxVisualAssetBytes || l.MaxTotalVisualBytes > l.MaxExpandedBytes {
		return errors.New("knowledge parser limits are invalid")
	}
	return nil
}

type runeBudget struct {
	remaining int
}

func newRuneBudget(limit int) *runeBudget {
	return &runeBudget{remaining: limit}
}

func (b *runeBudget) consume(value string) error {
	if b == nil {
		return errors.New("knowledge parser rune budget is unavailable")
	}
	count := utf8.RuneCountInString(value)
	if count > b.remaining {
		return fmt.Errorf("%w: extracted text exceeds configured rune limit", ErrResourceLimit)
	}
	b.remaining -= count
	return nil
}
