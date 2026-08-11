package diagnosis

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
)

var ErrTaskReportUnavailable = errors.New("diagnosis task report is unavailable")

// ReportModelUsage 是报告读取契约中的供应商 Token 用量快照。
type ReportModelUsage struct {
	ModelCalls       int `json:"modelCalls"`
	PromptTokens     int `json:"promptTokens"`
	CompletionTokens int `json:"completionTokens"`
	TotalTokens      int `json:"totalTokens"`
	CachedTokens     int `json:"cachedTokens"`
	ReasoningTokens  int `json:"reasoningTokens"`
}

// ReportContextObservation is the persisted provider-neutral Diagnosis prompt
// budget observation. It contains no prompt or evidence content.
type ReportContextObservation struct {
	PreflightCalls                int     `json:"preflightCalls"`
	PreflightFailureCount         int     `json:"preflightFailureCount"`
	HighWaterTokens               int     `json:"highWaterTokens"`
	AvailableInputTokens          int     `json:"availableInputTokens"`
	HighWaterRatio                float64 `json:"highWaterRatio"`
	ToolResultTruncatedCount      int     `json:"toolResultTruncatedCount"`
	HardWindowBlockedCount        int     `json:"hardWindowBlockedCount"`
	LastEstimatedUpperBoundTokens int     `json:"lastEstimatedUpperBoundTokens"`
	ReportOutputReserveTokens     int     `json:"reportOutputReserveTokens"`
	ToolGrowthReserveTokens       int     `json:"toolGrowthReserveTokens"`
	EstimationMethod              string  `json:"estimationMethod,omitempty"`
}

// ReportEvidenceClaim 只暴露报告引用和证据定位元数据，不包含完整证据内容。
type ReportEvidenceClaim struct {
	EvidenceID      uuid.UUID
	ClaimKey        string
	Claim           string
	SupportType     string
	SourceType      string
	SourceRef       string
	SourceTool      string
	Location        string
	ContentHash     string
	CollectedAt     time.Time
	RedactionStatus string
	Truncated       bool
	ValidityStatus  string
}

// DiagnosisReport 是 Worker 已提交的不可变正式报告读取模型。
type DiagnosisReport struct {
	ID                            uuid.UUID
	TaskID                        uuid.UUID
	ConclusionStatus              string
	RiskLevel                     string
	Conclusion                    string
	BusinessSummary               string
	TechnicalSummary              string
	Confidence                    string
	Limitations                   []string
	Partial                       bool
	MissingEvidence               []string
	Usage                         ReportModelUsage
	ContextObservation            ReportContextObservation
	AgentRuns                     int
	SelectedSkill                 string
	ExecutedSkills                []string
	StopReason                    string
	AgenticRetrievalAttempted     bool
	AgenticRetrievalAddedEvidence bool
	AgenticRetrievalStopReason    string
	ReportSchemaVersion           int
	ModelProvider                 string
	ModelID                       string
	PromptVersion                 string
	Evidence                      []ReportEvidenceClaim
	GeneratedAt                   time.Time
	CreatedAt                     time.Time
	UpdatedAt                     time.Time
}

// TaskReportLookup 把任务归属、状态和可选报告作为一次授权查询事实返回。
type TaskReportLookup struct {
	TaskID      uuid.UUID
	TaskCreator uuid.UUID
	TaskStatus  TaskStatus
	Report      *DiagnosisReport
}

type DiagnosisReportRepository interface {
	FindTaskReport(ctx context.Context, taskID uuid.UUID) (TaskReportLookup, error)
}

type DiagnosisReportService struct {
	repository DiagnosisReportRepository
}

func NewDiagnosisReportService(repository DiagnosisReportRepository) (*DiagnosisReportService, error) {
	if repository == nil {
		return nil, errors.New("diagnosis report repository is required")
	}
	return &DiagnosisReportService{repository: repository}, nil
}

func (s *DiagnosisReportService) Get(
	ctx context.Context,
	actor TaskActor,
	taskID uuid.UUID,
) (DiagnosisReport, error) {
	if s == nil || s.repository == nil {
		return DiagnosisReport{}, errors.New("diagnosis report service is unavailable")
	}
	if actor.UserID == uuid.Nil || taskID == uuid.Nil {
		return DiagnosisReport{}, ErrTaskForbidden
	}
	lookup, err := s.repository.FindTaskReport(ctx, taskID)
	if err != nil {
		return DiagnosisReport{}, err
	}
	if !actor.IsAdmin && actor.UserID != lookup.TaskCreator {
		return DiagnosisReport{}, ErrTaskForbidden
	}
	if lookup.Report == nil {
		return DiagnosisReport{}, ErrTaskReportUnavailable
	}
	return *lookup.Report, nil
}
