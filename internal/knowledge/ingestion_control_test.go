package knowledge

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
)

type ingestionControlRepositoryStub struct {
	page      DocumentListPage
	gotPage   int
	gotSize   int
	listCalls int
}

func (s *ingestionControlRepositoryStub) FindIngestionTask(context.Context, uuid.UUID) (IngestionTaskDetail, error) {
	return IngestionTaskDetail{}, errors.New("not implemented in stub")
}

func (s *ingestionControlRepositoryStub) RequestIngestionCancellation(context.Context, uuid.UUID, uuid.UUID, time.Time) (IngestionCancelResult, error) {
	return IngestionCancelResult{}, errors.New("not implemented in stub")
}

func (s *ingestionControlRepositoryStub) ListDocuments(_ context.Context, page, pageSize int) (DocumentListPage, error) {
	s.listCalls++
	s.gotPage, s.gotSize = page, pageSize
	return s.page, nil
}

func TestIngestionTaskControlServiceListsDocuments(t *testing.T) {
	now := time.Date(2026, 8, 10, 15, 0, 0, 0, time.UTC)
	documentID := uuid.New()
	repo := &ingestionControlRepositoryStub{page: DocumentListPage{
		Items: []DocumentListItem{{
			DocumentID: documentID, Title: "回冲科目对照表", Scope: ScopeGlobal,
			Version: 3, Status: IngestionSucceeded, Stage: IngestionStageCompleted,
			ProgressPercent: 100, AttemptCount: 1, MaxAttempts: 3, CreatedAt: now,
		}},
		Total: 1, Page: 1, PageSize: 20,
	}}
	service, err := NewIngestionTaskControlService(repo)
	if err != nil {
		t.Fatalf("NewIngestionTaskControlService(): %v", err)
	}

	page, err := service.ListDocuments(context.Background(), 1, 20)
	if err != nil {
		t.Fatalf("ListDocuments(): %v", err)
	}
	if page.Total != 1 || len(page.Items) != 1 {
		t.Fatalf("page = %+v", page)
	}
	item := page.Items[0]
	if item.DocumentID != documentID || item.Title != "回冲科目对照表" || item.Version != 3 ||
		item.Status != IngestionSucceeded || item.ProgressPercent != 100 {
		t.Fatalf("list item = %+v", item)
	}
	if repo.gotPage != 1 || repo.gotSize != 20 {
		t.Fatalf("repository received page=%d size=%d", repo.gotPage, repo.gotSize)
	}
}

func TestIngestionTaskControlServiceListDocumentsNormalizesPagination(t *testing.T) {
	repo := &ingestionControlRepositoryStub{}
	service, err := NewIngestionTaskControlService(repo)
	if err != nil {
		t.Fatalf("NewIngestionTaskControlService(): %v", err)
	}
	if _, err := service.ListDocuments(context.Background(), 0, 0); err != nil {
		t.Fatalf("ListDocuments(): %v", err)
	}
	if repo.gotPage != 1 || repo.gotSize != DefaultDocumentListPageSize {
		t.Fatalf("normalized pagination = page %d size %d", repo.gotPage, repo.gotSize)
	}
}

func TestIngestionTaskControlServiceListDocumentsRejectsOversizePageSize(t *testing.T) {
	repo := &ingestionControlRepositoryStub{}
	service, err := NewIngestionTaskControlService(repo)
	if err != nil {
		t.Fatalf("NewIngestionTaskControlService(): %v", err)
	}
	if _, err := service.ListDocuments(context.Background(), 5, 9999); err != nil {
		t.Fatalf("ListDocuments(): %v", err)
	}
	if repo.gotPage != 5 || repo.gotSize != MaxDocumentListPageSize {
		t.Fatalf("normalized pagination = page %d size %d", repo.gotPage, repo.gotSize)
	}
}
