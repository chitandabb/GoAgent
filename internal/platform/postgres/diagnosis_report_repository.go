package postgres

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/chitandabb/GoAgent/internal/diagnosis"
	"github.com/chitandabb/GoAgent/internal/repository"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

// DiagnosisReportRepository 读取 Worker 已提交的正式报告和有序证据声明。
type DiagnosisReportRepository struct {
	db *gorm.DB
}

var _ diagnosis.DiagnosisReportRepository = (*DiagnosisReportRepository)(nil)

func NewDiagnosisReportRepository(db *gorm.DB) *DiagnosisReportRepository {
	return &DiagnosisReportRepository{db: db}
}

func (r *DiagnosisReportRepository) FindTaskReport(
	ctx context.Context,
	taskID uuid.UUID,
) (diagnosis.TaskReportLookup, error) {
	if r == nil || r.db == nil {
		return diagnosis.TaskReportLookup{}, errors.New("diagnosis report repository is unavailable")
	}
	if taskID == uuid.Nil {
		return diagnosis.TaskReportLookup{}, errors.New("task id is required")
	}

	var record diagnosisReportRecord
	if err := ResolveDB(ctx, r.db).Raw(`
SELECT task.id AS task_id, task.created_by AS task_creator, task.status AS task_status,
       report.id AS report_id, report.conclusion_status, report.business_summary,
       report.technical_summary, report.report_schema_version, report.risk_level,
       report.model_provider, report.model_id, report.prompt_version,
       report.generated_at, report.created_at, report.updated_at
FROM diagnosis_tasks AS task
LEFT JOIN diagnosis_reports AS report ON report.task_id = task.id
WHERE task.id = ?`, taskID).Scan(&record).Error; err != nil {
		return diagnosis.TaskReportLookup{}, TranslateError(err)
	}
	if record.TaskID == uuid.Nil {
		return diagnosis.TaskReportLookup{}, repository.ErrNotFound
	}
	lookup := diagnosis.TaskReportLookup{
		TaskID: record.TaskID, TaskCreator: record.TaskCreator,
		TaskStatus: diagnosis.TaskStatus(record.TaskStatus),
	}
	if record.ReportID == nil {
		return lookup, nil
	}

	report, err := record.toDomain()
	if err != nil {
		return diagnosis.TaskReportLookup{}, err
	}
	claims, err := r.findEvidenceClaims(ctx, *record.ReportID)
	if err != nil {
		return diagnosis.TaskReportLookup{}, err
	}
	report.Evidence = claims
	lookup.Report = &report
	return lookup, nil
}

func (r *DiagnosisReportRepository) findEvidenceClaims(
	ctx context.Context,
	reportID uuid.UUID,
) ([]diagnosis.ReportEvidenceClaim, error) {
	var records []diagnosisReportEvidenceRecord
	if err := ResolveDB(ctx, r.db).Raw(`
SELECT link.evidence_id, link.claim_key, link.claim_text, link.support_type,
       evidence.source_type, evidence.source_locator,
       evidence.source_locator_schema_version, evidence.content_hash,
       evidence.collected_at, evidence.redaction_status, evidence.truncated,
       evidence.validity_status
FROM report_evidence AS link
JOIN evidence_items AS evidence ON evidence.id = link.evidence_id
WHERE link.report_id = ?
ORDER BY link.claim_key, link.evidence_id`, reportID).Scan(&records).Error; err != nil {
		return nil, TranslateError(err)
	}
	result := make([]diagnosis.ReportEvidenceClaim, 0, len(records))
	for _, record := range records {
		claim, err := record.toDomain()
		if err != nil {
			return nil, err
		}
		result = append(result, claim)
	}
	return result, nil
}

type diagnosisReportRecord struct {
	TaskID              uuid.UUID  `gorm:"column:task_id"`
	TaskCreator         uuid.UUID  `gorm:"column:task_creator"`
	TaskStatus          string     `gorm:"column:task_status"`
	ReportID            *uuid.UUID `gorm:"column:report_id"`
	ConclusionStatus    string     `gorm:"column:conclusion_status"`
	BusinessSummary     []byte     `gorm:"column:business_summary"`
	TechnicalSummary    []byte     `gorm:"column:technical_summary"`
	ReportSchemaVersion int        `gorm:"column:report_schema_version"`
	RiskLevel           string     `gorm:"column:risk_level"`
	ModelProvider       string     `gorm:"column:model_provider"`
	ModelID             string     `gorm:"column:model_id"`
	PromptVersion       string     `gorm:"column:prompt_version"`
	GeneratedAt         time.Time  `gorm:"column:generated_at"`
	CreatedAt           time.Time  `gorm:"column:created_at"`
	UpdatedAt           time.Time  `gorm:"column:updated_at"`
}

type reportBusinessSummaryPayload struct {
	Conclusion string `json:"conclusion"`
	Summary    string `json:"summary"`
	Confidence string `json:"confidence"`
}

type reportTechnicalSummaryPayload struct {
	Summary         string                     `json:"summary"`
	Limitations     []string                   `json:"limitations"`
	Partial         bool                       `json:"partial"`
	MissingEvidence []string                   `json:"missingEvidence"`
	Usage           diagnosis.ReportModelUsage `json:"usage"`
	AgentRuns       int                        `json:"agentRuns"`
	SelectedSkill   string                     `json:"selectedSkill"`
	ExecutedSkills  []string                   `json:"executedSkills"`
	StopReason      string                     `json:"stopReason"`
}

func (r diagnosisReportRecord) toDomain() (diagnosis.DiagnosisReport, error) {
	if r.ReportID == nil || r.ReportSchemaVersion != 1 {
		return diagnosis.DiagnosisReport{}, fmt.Errorf("unsupported diagnosis report schema version %d", r.ReportSchemaVersion)
	}
	var business reportBusinessSummaryPayload
	if err := decodeStrictStoredJSON(r.BusinessSummary, &business); err != nil {
		return diagnosis.DiagnosisReport{}, fmt.Errorf("decode diagnosis report business summary: %w", err)
	}
	var technical reportTechnicalSummaryPayload
	if err := decodeStrictStoredJSON(r.TechnicalSummary, &technical); err != nil {
		return diagnosis.DiagnosisReport{}, fmt.Errorf("decode diagnosis report technical summary: %w", err)
	}
	if strings.TrimSpace(business.Conclusion) == "" || strings.TrimSpace(business.Summary) == "" ||
		strings.TrimSpace(business.Confidence) == "" || strings.TrimSpace(technical.Summary) == "" ||
		strings.TrimSpace(r.ModelProvider) == "" || strings.TrimSpace(r.ModelID) == "" ||
		strings.TrimSpace(r.PromptVersion) == "" || technical.AgentRuns < 0 || !validReportUsage(technical.Usage) {
		return diagnosis.DiagnosisReport{}, errors.New("diagnosis report payload is invalid")
	}
	if technical.Limitations == nil {
		technical.Limitations = []string{}
	}
	if technical.MissingEvidence == nil {
		technical.MissingEvidence = []string{}
	}
	if technical.ExecutedSkills == nil {
		technical.ExecutedSkills = []string{}
	}
	return diagnosis.DiagnosisReport{
		ID: *r.ReportID, TaskID: r.TaskID, ConclusionStatus: r.ConclusionStatus,
		RiskLevel: r.RiskLevel, Conclusion: business.Conclusion,
		BusinessSummary: business.Summary, TechnicalSummary: technical.Summary,
		Confidence: business.Confidence, Limitations: technical.Limitations,
		Partial: technical.Partial, MissingEvidence: technical.MissingEvidence,
		Usage: technical.Usage, AgentRuns: technical.AgentRuns,
		SelectedSkill: technical.SelectedSkill, ExecutedSkills: technical.ExecutedSkills,
		StopReason: technical.StopReason, ReportSchemaVersion: r.ReportSchemaVersion,
		ModelProvider: r.ModelProvider, ModelID: r.ModelID, PromptVersion: r.PromptVersion,
		Evidence:    []diagnosis.ReportEvidenceClaim{},
		GeneratedAt: r.GeneratedAt.UTC(), CreatedAt: r.CreatedAt.UTC(), UpdatedAt: r.UpdatedAt.UTC(),
	}, nil
}

type diagnosisReportEvidenceRecord struct {
	EvidenceID                 uuid.UUID `gorm:"column:evidence_id"`
	ClaimKey                   string    `gorm:"column:claim_key"`
	ClaimText                  string    `gorm:"column:claim_text"`
	SupportType                string    `gorm:"column:support_type"`
	SourceType                 string    `gorm:"column:source_type"`
	SourceLocator              []byte    `gorm:"column:source_locator"`
	SourceLocatorSchemaVersion int       `gorm:"column:source_locator_schema_version"`
	ContentHash                string    `gorm:"column:content_hash"`
	CollectedAt                time.Time `gorm:"column:collected_at"`
	RedactionStatus            string    `gorm:"column:redaction_status"`
	Truncated                  bool      `gorm:"column:truncated"`
	ValidityStatus             string    `gorm:"column:validity_status"`
}

type reportEvidenceLocatorPayload struct {
	SourceRef  string `json:"sourceRef"`
	SourceTool string `json:"sourceTool"`
	Location   string `json:"location"`
}

func (r diagnosisReportEvidenceRecord) toDomain() (diagnosis.ReportEvidenceClaim, error) {
	if r.SourceLocatorSchemaVersion != 1 {
		return diagnosis.ReportEvidenceClaim{}, fmt.Errorf(
			"unsupported evidence locator schema version %d", r.SourceLocatorSchemaVersion,
		)
	}
	var locator reportEvidenceLocatorPayload
	if err := decodeStrictStoredJSON(r.SourceLocator, &locator); err != nil {
		return diagnosis.ReportEvidenceClaim{}, fmt.Errorf("decode report evidence locator: %w", err)
	}
	if r.EvidenceID == uuid.Nil || strings.TrimSpace(r.ClaimKey) == "" || strings.TrimSpace(r.ClaimText) == "" ||
		strings.TrimSpace(locator.SourceRef) == "" || strings.TrimSpace(locator.SourceTool) == "" ||
		strings.TrimSpace(r.ContentHash) == "" {
		return diagnosis.ReportEvidenceClaim{}, errors.New("report evidence payload is invalid")
	}
	return diagnosis.ReportEvidenceClaim{
		EvidenceID: r.EvidenceID, ClaimKey: r.ClaimKey, Claim: r.ClaimText,
		SupportType: r.SupportType, SourceType: r.SourceType,
		SourceRef: locator.SourceRef, SourceTool: locator.SourceTool, Location: locator.Location,
		ContentHash: r.ContentHash, CollectedAt: r.CollectedAt.UTC(),
		RedactionStatus: r.RedactionStatus, Truncated: r.Truncated, ValidityStatus: r.ValidityStatus,
	}, nil
}

func decodeStrictStoredJSON(data []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("stored JSON contains multiple values")
		}
		return err
	}
	return nil
}

func validReportUsage(usage diagnosis.ReportModelUsage) bool {
	return usage.ModelCalls >= 0 && usage.PromptTokens >= 0 && usage.CompletionTokens >= 0 &&
		usage.TotalTokens >= 0 && usage.CachedTokens >= 0 && usage.ReasoningTokens >= 0
}
