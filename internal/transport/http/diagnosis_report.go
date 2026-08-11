package httptransport

import (
	"context"
	"errors"
	"net/http"

	"github.com/chitandabb/GoAgent/internal/apperror"
	"github.com/chitandabb/GoAgent/internal/diagnosis"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

type diagnosisReportUseCase interface {
	Get(ctx context.Context, actor diagnosis.TaskActor, taskID uuid.UUID) (diagnosis.DiagnosisReport, error)
}

type DiagnosisReportRoutes struct {
	useCase diagnosisReportUseCase
	auth    gin.HandlerFunc
}

func NewDiagnosisReportRoutes(
	useCase diagnosisReportUseCase,
	authMiddleware gin.HandlerFunc,
) (*DiagnosisReportRoutes, error) {
	if useCase == nil || authMiddleware == nil {
		return nil, errors.New("diagnosis report route dependencies are nil")
	}
	return &DiagnosisReportRoutes{useCase: useCase, auth: authMiddleware}, nil
}

func (r *DiagnosisReportRoutes) Register(api *gin.RouterGroup) {
	protected := api.Group("/diagnosis-tasks")
	protected.Use(r.auth)
	protected.GET("/:taskId/report", r.get)
}

func (r *DiagnosisReportRoutes) get(c *gin.Context) {
	taskID, err := uuid.Parse(c.Param("taskId"))
	if err != nil {
		AbortWithError(c, apperror.NewWithFields(apperror.CodeInvalidArgument, []apperror.FieldError{{
			Field: "taskId", Reason: "必须是合法的 UUID",
		}}))
		return
	}
	identity, ok := identityFromContext(c)
	if !ok {
		AbortWithError(c, apperror.New(apperror.CodeUnauthorized))
		return
	}
	report, err := r.useCase.Get(c.Request.Context(), diagnosis.TaskActor{
		UserID: identity.User.ID, IsAdmin: identity.User.IsAdmin(),
	}, taskID)
	if err != nil {
		AbortWithError(c, translateDiagnosisTaskError("get diagnosis report", err))
		return
	}
	WriteSuccessWithStatus(c, http.StatusOK, diagnosisReportResponseFrom(report))
}

type diagnosisReportUsageResponse struct {
	ModelCalls       int `json:"modelCalls"`
	PromptTokens     int `json:"promptTokens"`
	CompletionTokens int `json:"completionTokens"`
	TotalTokens      int `json:"totalTokens"`
	CachedTokens     int `json:"cachedTokens"`
	ReasoningTokens  int `json:"reasoningTokens"`
}

type diagnosisReportEvidenceResponse struct {
	EvidenceID      string `json:"evidenceId"`
	ClaimKey        string `json:"claimKey"`
	Claim           string `json:"claim"`
	SupportType     string `json:"supportType"`
	SourceType      string `json:"sourceType"`
	SourceRef       string `json:"sourceRef"`
	SourceTool      string `json:"sourceTool"`
	Location        string `json:"location,omitempty"`
	ContentHash     string `json:"contentHash"`
	CollectedAt     string `json:"collectedAt"`
	RedactionStatus string `json:"redactionStatus"`
	Truncated       bool   `json:"truncated"`
	ValidityStatus  string `json:"validityStatus"`
}

type diagnosisReportResponse struct {
	ReportID                      string                             `json:"reportId"`
	TaskID                        string                             `json:"taskId"`
	ConclusionStatus              string                             `json:"conclusionStatus"`
	RiskLevel                     string                             `json:"riskLevel"`
	Conclusion                    string                             `json:"conclusion"`
	BusinessSummary               string                             `json:"businessSummary"`
	TechnicalSummary              string                             `json:"technicalSummary"`
	Confidence                    string                             `json:"confidence"`
	Limitations                   []string                           `json:"limitations"`
	Partial                       bool                               `json:"partial"`
	MissingEvidence               []string                           `json:"missingEvidence"`
	Usage                         diagnosisReportUsageResponse       `json:"usage"`
	ContextObservation            diagnosis.ReportContextObservation `json:"contextObservation"`
	AgentRuns                     int                                `json:"agentRuns"`
	SelectedSkill                 string                             `json:"selectedSkill"`
	ExecutedSkills                []string                           `json:"executedSkills"`
	StopReason                    string                             `json:"stopReason,omitempty"`
	AgenticRetrievalAttempted     bool                               `json:"agenticRetrievalAttempted"`
	AgenticRetrievalAddedEvidence bool                               `json:"agenticRetrievalAddedEvidence"`
	AgenticRetrievalStopReason    string                             `json:"agenticRetrievalStopReason"`
	ReportSchemaVersion           int                                `json:"reportSchemaVersion"`
	ModelProvider                 string                             `json:"modelProvider"`
	ModelID                       string                             `json:"modelId"`
	PromptVersion                 string                             `json:"promptVersion"`
	Evidence                      []diagnosisReportEvidenceResponse  `json:"evidence"`
	GeneratedAt                   string                             `json:"generatedAt"`
	CreatedAt                     string                             `json:"createdAt"`
	UpdatedAt                     string                             `json:"updatedAt"`
}

func diagnosisReportResponseFrom(report diagnosis.DiagnosisReport) diagnosisReportResponse {
	evidence := make([]diagnosisReportEvidenceResponse, 0, len(report.Evidence))
	for _, item := range report.Evidence {
		evidence = append(evidence, diagnosisReportEvidenceResponse{
			EvidenceID: item.EvidenceID.String(), ClaimKey: item.ClaimKey,
			Claim: item.Claim, SupportType: item.SupportType, SourceType: item.SourceType,
			SourceRef: item.SourceRef, SourceTool: item.SourceTool, Location: item.Location,
			ContentHash: item.ContentHash, CollectedAt: item.CollectedAt.UTC().Format(timeRFC3339Nano),
			RedactionStatus: item.RedactionStatus, Truncated: item.Truncated,
			ValidityStatus: item.ValidityStatus,
		})
	}
	return diagnosisReportResponse{
		ReportID: report.ID.String(), TaskID: report.TaskID.String(),
		ConclusionStatus: report.ConclusionStatus, RiskLevel: report.RiskLevel,
		Conclusion: report.Conclusion, BusinessSummary: report.BusinessSummary,
		TechnicalSummary: report.TechnicalSummary, Confidence: report.Confidence,
		Limitations: report.Limitations, Partial: report.Partial,
		MissingEvidence: report.MissingEvidence,
		Usage: diagnosisReportUsageResponse{
			ModelCalls: report.Usage.ModelCalls, PromptTokens: report.Usage.PromptTokens,
			CompletionTokens: report.Usage.CompletionTokens, TotalTokens: report.Usage.TotalTokens,
			CachedTokens: report.Usage.CachedTokens, ReasoningTokens: report.Usage.ReasoningTokens,
		},
		ContextObservation: report.ContextObservation,
		AgentRuns:          report.AgentRuns, SelectedSkill: report.SelectedSkill,
		ExecutedSkills: report.ExecutedSkills, StopReason: report.StopReason,
		AgenticRetrievalAttempted:     report.AgenticRetrievalAttempted,
		AgenticRetrievalAddedEvidence: report.AgenticRetrievalAddedEvidence,
		AgenticRetrievalStopReason:    report.AgenticRetrievalStopReason,
		ReportSchemaVersion:           report.ReportSchemaVersion,
		ModelProvider:                 report.ModelProvider, ModelID: report.ModelID, PromptVersion: report.PromptVersion,
		Evidence: evidence, GeneratedAt: report.GeneratedAt.UTC().Format(timeRFC3339Nano),
		CreatedAt: report.CreatedAt.UTC().Format(timeRFC3339Nano),
		UpdatedAt: report.UpdatedAt.UTC().Format(timeRFC3339Nano),
	}
}

var _ diagnosisReportUseCase = (*diagnosis.DiagnosisReportService)(nil)
