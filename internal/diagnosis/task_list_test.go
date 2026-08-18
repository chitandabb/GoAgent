package diagnosis

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestDiagnosisTaskServiceListReturnsPage(t *testing.T) {
	ownerID := uuid.New()
	createdAt := time.Date(2026, 8, 10, 13, 0, 0, 0, time.UTC)
	repo := &taskRepositoryStub{listPage: TaskListPage{
		Items: []TaskListItem{{
			Task: DiagnosisTask{
				ID: uuid.New(), CreatedBy: ownerID, ExternalCaseID: uuid.New(),
				RequestText: "检查批次回冲", Status: TaskRunning, CreatedAt: createdAt, UpdatedAt: createdAt,
			},
			ExternalCaseKey:   "WO-2026-0810",
			ExternalCaseTitle: "报工数量与完工数量不一致",
		}},
		Total: 1, Page: 1, PageSize: 20,
	}}
	service, _ := NewDiagnosisTaskService(repo, &taskCaseReaderStub{}, mustTestPolicyBuilder(t))

	page, err := service.List(context.Background(), TaskListQuery{
		Actor: TaskActor{UserID: ownerID, IsAdmin: false}, Page: 1, PageSize: 20,
	})
	if err != nil {
		t.Fatalf("List(): %v", err)
	}
	if page.Total != 1 || len(page.Items) != 1 {
		t.Fatalf("page = %+v", page)
	}
	item := page.Items[0]
	if item.ExternalCaseKey != "WO-2026-0810" || item.ExternalCaseTitle != "报工数量与完工数量不一致" {
		t.Fatalf("list item = %+v", item)
	}
	if repo.listGotQuery.Actor.UserID != ownerID {
		t.Fatalf("query actor = %+v", repo.listGotQuery.Actor)
	}
}

func TestDiagnosisTaskServiceListScopesAnalystAndPassesFilter(t *testing.T) {
	ownerID := uuid.New()
	status := TaskFailed
	repo := &taskRepositoryStub{}
	service, _ := NewDiagnosisTaskService(repo, &taskCaseReaderStub{}, mustTestPolicyBuilder(t))

	_, err := service.List(context.Background(), TaskListQuery{
		Actor: TaskActor{UserID: ownerID, IsAdmin: false}, Status: &status, Page: 2, PageSize: 10,
	})
	if err != nil {
		t.Fatalf("List(): %v", err)
	}
	if repo.listGotQuery.Actor.UserID != ownerID || repo.listGotQuery.Actor.IsAdmin {
		t.Fatalf("analyst query must scope to owner, got %+v", repo.listGotQuery.Actor)
	}
	if repo.listGotQuery.Status == nil || *repo.listGotQuery.Status != status {
		t.Fatalf("status filter = %v", repo.listGotQuery.Status)
	}
	if repo.listGotQuery.Page != 2 || repo.listGotQuery.PageSize != 10 {
		t.Fatalf("pagination = %+v", repo.listGotQuery)
	}
}

func TestDiagnosisTaskServiceListPassesExternalCaseFilter(t *testing.T) {
	ownerID := uuid.New()
	caseID := uuid.New()
	repo := &taskRepositoryStub{}
	service, _ := NewDiagnosisTaskService(repo, &taskCaseReaderStub{}, mustTestPolicyBuilder(t))

	_, err := service.List(context.Background(), TaskListQuery{
		Actor: TaskActor{UserID: ownerID, IsAdmin: true}, ExternalCaseID: &caseID, Page: 1, PageSize: 20,
	})
	if err != nil {
		t.Fatalf("List(): %v", err)
	}
	if repo.listGotQuery.ExternalCaseID == nil || *repo.listGotQuery.ExternalCaseID != caseID {
		t.Fatalf("external case filter = %v, want %s", repo.listGotQuery.ExternalCaseID, caseID)
	}
}

func TestDiagnosisTaskServiceListNormalizesPagination(t *testing.T) {
	repo := &taskRepositoryStub{}
	service, _ := NewDiagnosisTaskService(repo, &taskCaseReaderStub{}, mustTestPolicyBuilder(t))

	_, err := service.List(context.Background(), TaskListQuery{
		Actor: TaskActor{UserID: uuid.New(), IsAdmin: true}, Page: 0, PageSize: 0,
	})
	if err != nil {
		t.Fatalf("List(): %v", err)
	}
	if repo.listGotQuery.Page != 1 || repo.listGotQuery.PageSize != DefaultTaskListPageSize {
		t.Fatalf("normalized pagination = page %d size %d, want 1/%d",
			repo.listGotQuery.Page, repo.listGotQuery.PageSize, DefaultTaskListPageSize)
	}
}

func TestDiagnosisTaskServiceListRejectsInvalidInput(t *testing.T) {
	repo := &taskRepositoryStub{}
	service, _ := NewDiagnosisTaskService(repo, &taskCaseReaderStub{}, mustTestPolicyBuilder(t))
	invalidStatus := TaskStatus("unknown_state")

	cases := []struct {
		name  string
		query TaskListQuery
	}{
		{"missing actor", TaskListQuery{Actor: TaskActor{UserID: uuid.Nil}, Page: 1, PageSize: 20}},
		{"invalid status", TaskListQuery{
			Actor: TaskActor{UserID: uuid.New(), IsAdmin: true}, Status: &invalidStatus, Page: 1, PageSize: 20,
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := service.List(context.Background(), tc.query)
			if err != ErrTaskForbidden && err != ErrInvalidTask {
				t.Fatalf("List() error = %v, want ErrTaskForbidden or ErrInvalidTask", err)
			}
			if repo.listCalls != 0 {
				t.Fatalf("repository calls = %d, want 0", repo.listCalls)
			}
		})
	}
}
