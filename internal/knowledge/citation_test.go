package knowledge

import (
	"context"
	"testing"

	"github.com/google/uuid"
)

func TestCitationServiceValidatesPersistentContentHash(t *testing.T) {
	content := "连接池超时应检查活动连接与等待队列。"
	item := CitationPreview{
		DocumentID: uuid.New(), DocumentVersionID: uuid.New(), ChunkID: uuid.New(),
		Title: "连接池排障", Scope: ScopeGlobal, Version: 2, Ordinal: 3,
		ElementType: ElementText, SectionPath: []string{"故障处理"},
		ContentText: content, ContentSHA256: SHA256Hex(content),
	}
	repository := citationRepositoryStub{item: item}
	service, err := NewCitationService(repository)
	if err != nil {
		t.Fatal(err)
	}
	result, err := service.Get(context.Background(), uuid.New(), item.ChunkID)
	if err != nil || result.ChunkID != item.ChunkID {
		t.Fatalf("Get()=%+v err=%v", result, err)
	}
	repository.item.ContentSHA256 = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	service, _ = NewCitationService(repository)
	if _, err := service.Get(context.Background(), uuid.New(), item.ChunkID); err == nil {
		t.Fatal("Get() accepted a stale citation content hash")
	}
}

type citationRepositoryStub struct {
	item CitationPreview
	err  error
}

func (s citationRepositoryStub) GetCitation(context.Context, uuid.UUID, uuid.UUID) (CitationPreview, error) {
	if s.err != nil {
		return CitationPreview{}, s.err
	}
	return s.item, nil
}
