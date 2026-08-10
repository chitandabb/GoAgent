package bootstrap

import (
	"errors"

	"github.com/chitandabb/GoAgent/internal/attachment"
	"github.com/chitandabb/GoAgent/internal/knowledgeparser"
	"github.com/chitandabb/GoAgent/internal/objectstore"
	"github.com/chitandabb/GoAgent/internal/platform/config"
	platformpostgres "github.com/chitandabb/GoAgent/internal/platform/postgres"
	"gorm.io/gorm"
)

func buildAttachmentService(cfg config.Config, db *gorm.DB, store objectstore.Store, storeErr error) (*attachment.Service, error) {
	if db == nil {
		return nil, errors.New("attachment PostgreSQL dependency is unavailable")
	}
	if store == nil {
		store = objectstore.NewUnavailableStore(storeErr)
	}
	limits := knowledgeparser.Limits{
		MaxDocumentUnits:      cfg.Knowledge.ParserMaxDocumentUnits,
		MaxArchiveEntries:     cfg.Knowledge.ParserMaxArchiveEntries,
		MaxExpandedBytes:      cfg.Knowledge.ParserMaxExpandedBytes,
		MaxXMLBytes:           cfg.Knowledge.ParserMaxXMLBytes,
		MaxExtractedRunes:     cfg.Knowledge.ParserMaxExtractedRunes,
		MaxSpreadsheetRows:    cfg.Knowledge.ParserMaxSpreadsheetRows,
		MaxSpreadsheetColumns: cfg.Knowledge.ParserMaxSpreadsheetColumns,
		MaxVisualAssets:       cfg.Knowledge.ParserMaxVisualAssets,
		MaxVisualAssetBytes:   cfg.Knowledge.ParserMaxVisualAssetBytes,
		MaxTotalVisualBytes:   cfg.Knowledge.ParserMaxTotalVisualBytes,
	}
	pdfParser, err := knowledgeparser.NewPDFParser(limits)
	if err != nil {
		return nil, err
	}
	ooxmlParser, err := knowledgeparser.NewOOXMLParser(limits)
	if err != nil {
		return nil, err
	}
	imageParser, err := knowledgeparser.NewImageParser(limits)
	if err != nil {
		return nil, err
	}
	parser, err := knowledgeparser.NewRouter(knowledgeparser.TextParser{}, pdfParser, ooxmlParser, imageParser)
	if err != nil {
		return nil, err
	}
	return attachment.NewService(
		platformpostgres.NewAttachmentRepository(db), store, parser, cfg.Knowledge.MaxUploadBytes,
	)
}
