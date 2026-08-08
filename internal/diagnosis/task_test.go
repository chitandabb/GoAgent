package diagnosis

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/chitandabb/GoAgent/internal/externalcase"
	"github.com/google/uuid"
)

func TestDiagnosisTaskServiceCreateBuildsRedactedSnapshotAndFingerprint(t *testing.T) {
	caseID := uuid.New()
	firstSourceID := uuid.New()
	secondSourceID := uuid.New()
	ownerID := uuid.New()
	readAt := time.Date(2026, 8, 2, 4, 0, 0, 0, time.UTC)
	repo := &taskRepositoryStub{}
	reader := &taskCaseReaderStub{item: &externalcase.ExternalCase{
		ID: caseID, DataSourceID: uuid.New(), ExternalCaseKey: "TKT-1001", CaseType: "incident",
		Title: "库存未更新", Description: "数据库状态异常", SourceFingerprint: "sha256:source",
		ReportedAt: readAt, SourceUpdatedAt: readAt,
		Attributes: map[string]any{"line": "A"},
		Attachments: []externalcase.ExternalAttachment{{
			ExternalAttachmentKey: "ATT-1", FileName: "error.png", MediaType: "image/png",
			SizeBytes: 42, ObjectKey: "private/object-key", ContentHash: "sha256:file", SourceUpdatedAt: readAt,
		}},
	}}
	service, err := NewDiagnosisTaskService(repo, reader)
	if err != nil {
		t.Fatalf("NewDiagnosisTaskService(): %v", err)
	}
	service.clock = func() time.Time { return readAt }

	result, err := service.Create(context.Background(), TaskActor{UserID: ownerID}, CreateTaskInput{
		ExternalCaseID: caseID, ExpectedSourceFingerprint: "sha256:source",
		EvidenceDataSourceIDs: []uuid.UUID{secondSourceID, firstSourceID, firstSourceID},
		RequestText:           "  请检查数据库状态  ", RequestScope: map[string]any{"timeRange": map[string]any{"from": "today"}},
		IdempotencyKey: uuid.NewString(), CorrelationID: uuid.New(),
	})
	if err != nil {
		t.Fatalf("Create(): %v", err)
	}
	if result.Replayed || result.Task.Status != TaskPending {
		t.Fatalf("create result = %+v", result)
	}
	if repo.createInput.CreatedBy != ownerID || repo.createInput.RequestText != "请检查数据库状态" {
		t.Fatalf("repository input = %+v", repo.createInput)
	}
	wantSourceIDs := []uuid.UUID{firstSourceID, secondSourceID}
	sort.Slice(wantSourceIDs, func(i, j int) bool { return wantSourceIDs[i].String() < wantSourceIDs[j].String() })
	if len(repo.createInput.EvidenceDataSourceIDs) != 2 || repo.createInput.EvidenceDataSourceIDs[0] != wantSourceIDs[0] || repo.createInput.EvidenceDataSourceIDs[1] != wantSourceIDs[1] {
		t.Fatalf("normalized data source IDs = %v", repo.createInput.EvidenceDataSourceIDs)
	}
	payload := string(repo.createInput.Snapshot.Payload)
	if strings.Contains(payload, "private/object-key") || strings.Contains(payload, "objectKey") {
		t.Fatalf("snapshot leaked object storage key: %s", payload)
	}
	if repo.createInput.Snapshot.RedactionStatus != "redacted" || repo.createInput.Snapshot.ContentHash == "" {
		t.Fatalf("snapshot metadata = %+v", repo.createInput.Snapshot)
	}
	if !strings.HasPrefix(repo.createInput.RequestFingerprint, "sha256:") {
		t.Fatalf("request fingerprint = %q", repo.createInput.RequestFingerprint)
	}
	var persistedScope map[string]any
	if err := json.Unmarshal(repo.createInput.RequestScope, &persistedScope); err != nil {
		t.Fatalf("decode persisted request scope: %v", err)
	}
	capabilities, err := TaskCapabilitiesFromRequestScope(persistedScope)
	if err != nil || len(capabilities) != 3 || capabilities[0] != TaskCapabilityCase ||
		capabilities[1] != TaskCapabilityKnowledge || capabilities[2] != TaskCapabilityWebSearch {
		t.Fatalf("default capabilities = %v, err=%v", capabilities, err)
	}
}

func TestNormalizeTaskRequestScopeValidatesCapabilitiesAndSkill(t *testing.T) {
	tests := []struct {
		name    string
		scope   map[string]any
		want    []TaskCapability
		wantErr bool
	}{
		{name: "defaults to backend managed capabilities", scope: nil, want: []TaskCapability{TaskCapabilityCase, TaskCapabilityKnowledge, TaskCapabilityWebSearch}},
		{
			name:  "legacy code skill infers capability",
			scope: map[string]any{RequestScopeKeyRequestedSkill: RequestedSkillCodeInvestigation},
			want:  []TaskCapability{TaskCapabilityCase, TaskCapabilityCode, TaskCapabilityKnowledge, TaskCapabilityWebSearch},
		},
		{
			name: "sql investigation", scope: map[string]any{
				RequestScopeKeyRequestedSkill:      RequestedSkillSQLInvestigation,
				RequestScopeKeyAllowedCapabilities: []string{"sql", "case"},
			},
			want: []TaskCapability{TaskCapabilityCase, TaskCapabilityKnowledge, TaskCapabilitySQL, TaskCapabilityWebSearch},
		},
		{
			name: "broad ticket diagnosis", scope: map[string]any{
				RequestScopeKeyRequestedSkill:      RequestedSkillTicketDiagnosis,
				RequestScopeKeyAllowedCapabilities: []string{"sql", "code", "case"},
			},
			want: []TaskCapability{TaskCapabilityCase, TaskCapabilityCode, TaskCapabilityKnowledge, TaskCapabilitySQL, TaskCapabilityWebSearch},
		},
		{name: "missing case", scope: map[string]any{RequestScopeKeyAllowedCapabilities: []string{"sql"}}, wantErr: true},
		{name: "duplicate", scope: map[string]any{RequestScopeKeyAllowedCapabilities: []string{"case", "case"}}, wantErr: true},
		{name: "unknown", scope: map[string]any{RequestScopeKeyAllowedCapabilities: []string{"case", "shell"}}, wantErr: true},
		{name: "knowledge is backend managed", scope: map[string]any{RequestScopeKeyAllowedCapabilities: []string{"case", "knowledge"}}, wantErr: true},
		{name: "web search is backend managed", scope: map[string]any{RequestScopeKeyAllowedCapabilities: []string{"case", "web_search"}}, wantErr: true},
		{
			name: "code skill without capability", scope: map[string]any{
				RequestScopeKeyRequestedSkill:      RequestedSkillCodeInvestigation,
				RequestScopeKeyAllowedCapabilities: []string{"case"},
			},
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			normalized, err := NormalizeTaskRequestScope(tt.scope)
			if tt.wantErr {
				if err == nil {
					t.Fatal("NormalizeTaskRequestScope accepted invalid scope")
				}
				return
			}
			if err != nil {
				t.Fatalf("NormalizeTaskRequestScope: %v", err)
			}
			got, err := TaskCapabilitiesFromRequestScope(normalized)
			if err != nil {
				t.Fatalf("TaskCapabilitiesFromRequestScope: %v", err)
			}
			if len(got) != len(tt.want) {
				t.Fatalf("capabilities = %v, want %v", got, tt.want)
			}
			for index := range got {
				if got[index] != tt.want[index] {
					t.Fatalf("capabilities = %v, want %v", got, tt.want)
				}
			}
		})
	}
}

func TestDiagnosisTaskServiceRejectsChangedSourceBeforePersistence(t *testing.T) {
	caseID := uuid.New()
	repo := &taskRepositoryStub{}
	reader := &taskCaseReaderStub{item: &externalcase.ExternalCase{
		ID: caseID, SourceFingerprint: "sha256:actual", ReportedAt: time.Now().UTC(), SourceUpdatedAt: time.Now().UTC(),
	}}
	service, _ := NewDiagnosisTaskService(repo, reader)
	_, err := service.Create(context.Background(), TaskActor{UserID: uuid.New()}, CreateTaskInput{
		ExternalCaseID: caseID, ExpectedSourceFingerprint: "sha256:old", RequestText: "检查", IdempotencyKey: uuid.NewString(),
	})
	if !errors.Is(err, ErrSourceChanged) {
		t.Fatalf("Create() error = %v, want ErrSourceChanged", err)
	}
	if repo.createCalls != 0 {
		t.Fatalf("repository create calls = %d, want 0", repo.createCalls)
	}
}

func TestDiagnosisTaskServiceRejectsAttachmentsInsteadOfDroppingThem(t *testing.T) {
	repo := &taskRepositoryStub{}
	reader := &taskCaseReaderStub{}
	service, _ := NewDiagnosisTaskService(repo, reader)
	_, err := service.Create(context.Background(), TaskActor{UserID: uuid.New()}, CreateTaskInput{
		ExternalCaseID: uuid.New(), ExpectedSourceFingerprint: "sha256:source", RequestText: "检查",
		Attachments: []TaskAttachment{{AttachmentID: uuid.New(), Purpose: "problem_image"}}, IdempotencyKey: uuid.NewString(),
	})
	if !errors.Is(err, ErrAttachmentsUnsupported) {
		t.Fatalf("Create() error = %v, want ErrAttachmentsUnsupported", err)
	}
	if reader.calls != 0 || repo.createCalls != 0 {
		t.Fatalf("reader/repository calls = %d/%d, want 0/0", reader.calls, repo.createCalls)
	}
}

func TestDiagnosisTaskServiceGetEnforcesOwnerOrAdmin(t *testing.T) {
	ownerID := uuid.New()
	taskID := uuid.New()
	repo := &taskRepositoryStub{getTask: DiagnosisTask{ID: taskID, CreatedBy: ownerID, Status: TaskPending}}
	service, _ := NewDiagnosisTaskService(repo, &taskCaseReaderStub{})
	if _, err := service.Get(context.Background(), TaskActor{UserID: uuid.New()}, taskID); !errors.Is(err, ErrTaskForbidden) {
		t.Fatalf("non-owner Get() error = %v, want ErrTaskForbidden", err)
	}
	if _, err := service.Get(context.Background(), TaskActor{UserID: ownerID}, taskID); err != nil {
		t.Fatalf("owner Get(): %v", err)
	}
	if _, err := service.Get(context.Background(), TaskActor{UserID: uuid.New(), IsAdmin: true}, taskID); err != nil {
		t.Fatalf("admin Get(): %v", err)
	}
}

func TestDiagnosisTaskServiceListEventsNormalizesLimitAndEnforcesOwner(t *testing.T) {
	ownerID := uuid.New()
	taskID := uuid.New()
	repo := &taskRepositoryStub{
		getTask:   DiagnosisTask{ID: taskID, CreatedBy: ownerID, Status: TaskPending},
		eventPage: TaskEventPage{Items: []TaskEvent{{TaskID: taskID, Seq: 1, EventType: "task_created"}}},
	}
	service, _ := NewDiagnosisTaskService(repo, &taskCaseReaderStub{})

	if _, err := service.ListEvents(context.Background(), TaskActor{UserID: uuid.New()}, taskID, 0, 0); !errors.Is(err, ErrTaskForbidden) {
		t.Fatalf("non-owner ListEvents() error = %v, want ErrTaskForbidden", err)
	}
	page, err := service.ListEvents(context.Background(), TaskActor{UserID: ownerID}, taskID, 7, 0)
	if err != nil {
		t.Fatalf("ListEvents(): %v", err)
	}
	if len(page.Items) != 1 || repo.gotAfterSeq != 7 || repo.gotEventLimit != DefaultTaskEventLimit {
		t.Fatalf("page=%+v afterSeq=%d limit=%d", page, repo.gotAfterSeq, repo.gotEventLimit)
	}
	if _, err := service.ListEvents(context.Background(), TaskActor{UserID: ownerID}, taskID, -1, 1); !errors.Is(err, ErrInvalidTask) {
		t.Fatalf("negative afterSeq error = %v, want ErrInvalidTask", err)
	}
}

func TestDiagnosisTaskServiceOpensAuthorizedEventStreamOnce(t *testing.T) {
	ownerID := uuid.New()
	taskID := uuid.New()
	repo := &taskRepositoryStub{
		getTask: DiagnosisTask{ID: taskID, CreatedBy: ownerID, Status: TaskRunning},
		eventPage: TaskEventPage{
			Items:    []TaskEvent{{TaskID: taskID, Seq: 2, EventType: "task_started"}},
			AfterSeq: 1, NextAfterSeq: 2,
		},
	}
	service, _ := NewDiagnosisTaskService(repo, &taskCaseReaderStub{})

	if _, err := service.OpenEventStream(context.Background(), TaskActor{UserID: uuid.New()}, taskID); !errors.Is(err, ErrTaskForbidden) {
		t.Fatalf("non-owner OpenEventStream() error = %v, want ErrTaskForbidden", err)
	}
	stream, err := service.OpenEventStream(context.Background(), TaskActor{UserID: ownerID}, taskID)
	if err != nil {
		t.Fatalf("OpenEventStream(): %v", err)
	}
	if stream.InitialStatus() != TaskRunning {
		t.Fatalf("initial status = %q, want running", stream.InitialStatus())
	}
	page, err := stream.Next(context.Background(), 1, MaxTaskEventLimit)
	if err != nil {
		t.Fatalf("stream.Next(): %v", err)
	}
	if len(page.Items) != 1 || repo.gotAfterSeq != 1 || repo.gotEventLimit != MaxTaskEventLimit {
		t.Fatalf("page=%+v afterSeq=%d limit=%d", page, repo.gotAfterSeq, repo.gotEventLimit)
	}
}

func TestDiagnosisTaskServiceCancelUsesAuthorizedActorAndClock(t *testing.T) {
	ownerID := uuid.New()
	taskID := uuid.New()
	now := time.Date(2026, 8, 2, 8, 0, 0, 0, time.UTC)
	repo := &taskRepositoryStub{
		getTask:      DiagnosisTask{ID: taskID, CreatedBy: ownerID, Status: TaskRunning},
		cancelResult: TaskCancelResult{Task: DiagnosisTask{ID: taskID, Status: TaskCancelRequested}, Changed: true},
	}
	service, _ := NewDiagnosisTaskService(repo, &taskCaseReaderStub{})
	service.clock = func() time.Time { return now }

	result, err := service.Cancel(context.Background(), TaskActor{UserID: ownerID}, taskID)
	if err != nil {
		t.Fatalf("Cancel(): %v", err)
	}
	if !result.Changed || repo.gotCancelTaskID != taskID || repo.gotRequestedBy != ownerID || !repo.gotRequestedAt.Equal(now) {
		t.Fatalf("result=%+v taskID=%s requestedBy=%s requestedAt=%s", result, repo.gotCancelTaskID, repo.gotRequestedBy, repo.gotRequestedAt)
	}
}

type taskRepositoryStub struct {
	createInput     diagnosisCreateTaskRecordAlias
	createResult    TaskCreateResult
	createErr       error
	createCalls     int
	getTask         DiagnosisTask
	getErr          error
	eventPage       TaskEventPage
	eventErr        error
	gotAfterSeq     int64
	gotEventLimit   int
	cancelResult    TaskCancelResult
	cancelErr       error
	gotCancelTaskID uuid.UUID
	gotRequestedBy  uuid.UUID
	gotRequestedAt  time.Time
}

// alias keeps the test readable while retaining the production interface type.
type diagnosisCreateTaskRecordAlias = CreateTaskRecord

func (s *taskRepositoryStub) CreateTask(_ context.Context, input CreateTaskRecord) (TaskCreateResult, error) {
	s.createInput = input
	s.createCalls++
	if s.createErr != nil {
		return TaskCreateResult{}, s.createErr
	}
	if s.createResult.Task.ID == uuid.Nil {
		s.createResult.Task = DiagnosisTask{ID: uuid.New(), CreatedBy: input.CreatedBy, Status: TaskPending, CreatedAt: input.CreatedAt}
	}
	return s.createResult, nil
}

func (s *taskRepositoryStub) GetTask(context.Context, uuid.UUID) (DiagnosisTask, error) {
	return s.getTask, s.getErr
}

func (s *taskRepositoryStub) ListTaskEvents(_ context.Context, _ uuid.UUID, afterSeq int64, limit int) (TaskEventPage, error) {
	s.gotAfterSeq, s.gotEventLimit = afterSeq, limit
	return s.eventPage, s.eventErr
}

func (s *taskRepositoryStub) CancelTask(_ context.Context, taskID, requestedBy uuid.UUID, requestedAt time.Time) (TaskCancelResult, error) {
	s.gotCancelTaskID, s.gotRequestedBy, s.gotRequestedAt = taskID, requestedBy, requestedAt
	return s.cancelResult, s.cancelErr
}

type taskCaseReaderStub struct {
	item  *externalcase.ExternalCase
	err   error
	calls int
}

func (s *taskCaseReaderStub) Get(context.Context, uuid.UUID) (*externalcase.ExternalCase, error) {
	s.calls++
	return s.item, s.err
}
