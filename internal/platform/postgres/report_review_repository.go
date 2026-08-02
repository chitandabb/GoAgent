package postgres

import (
	"context"
	"errors"
	"time"

	"github.com/chitandabb/GoAgent/internal/diagnosis"
	"github.com/chitandabb/GoAgent/internal/repository"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

// DiagnosisReportReviewRepository 读取报告归属并追加人工反馈历史。
type DiagnosisReportReviewRepository struct {
	db *gorm.DB
}

var _ diagnosis.ReportReviewRepository = (*DiagnosisReportReviewRepository)(nil)

func NewDiagnosisReportReviewRepository(db *gorm.DB) *DiagnosisReportReviewRepository {
	return &DiagnosisReportReviewRepository{db: db}
}

func (r *DiagnosisReportReviewRepository) FindReportAccess(
	ctx context.Context,
	reportID uuid.UUID,
) (diagnosis.ReportAccess, error) {
	if r == nil || r.db == nil {
		return diagnosis.ReportAccess{}, errors.New("report review repository is unavailable")
	}
	if reportID == uuid.Nil {
		return diagnosis.ReportAccess{}, errors.New("report id is required")
	}
	var record reportAccessRecord
	err := ResolveDB(ctx, r.db).Raw(`
SELECT r.id AS report_id, r.task_id, t.created_by AS task_creator
FROM diagnosis_reports AS r
JOIN diagnosis_tasks AS t ON t.id = r.task_id
WHERE r.id = ?`, reportID).Scan(&record).Error
	if err != nil {
		return diagnosis.ReportAccess{}, TranslateError(err)
	}
	if record.ReportID == uuid.Nil {
		return diagnosis.ReportAccess{}, repository.ErrNotFound
	}
	return diagnosis.ReportAccess{
		ReportID: record.ReportID, TaskID: record.TaskID, TaskCreator: record.TaskCreator,
	}, nil
}

func (r *DiagnosisReportReviewRepository) ListReportReviews(
	ctx context.Context,
	reportID uuid.UUID,
) ([]diagnosis.ReportReview, error) {
	if r == nil || r.db == nil {
		return nil, errors.New("report review repository is unavailable")
	}
	var records []reportReviewRecord
	if err := ResolveDB(ctx, r.db).
		Where("report_id = ?", reportID).
		Order("created_at DESC, id DESC").
		Find(&records).Error; err != nil {
		return nil, TranslateError(err)
	}
	result := make([]diagnosis.ReportReview, 0, len(records))
	for _, record := range records {
		result = append(result, record.toDomain())
	}
	return result, nil
}

func (r *DiagnosisReportReviewRepository) CreateReportReview(
	ctx context.Context,
	review diagnosis.ReportReview,
) error {
	if r == nil || r.db == nil {
		return errors.New("report review repository is unavailable")
	}
	if err := ResolveDB(ctx, r.db).Create(reportReviewRecordFromDomain(review)).Error; err != nil {
		return TranslateError(err)
	}
	return nil
}

type reportAccessRecord struct {
	ReportID    uuid.UUID `gorm:"column:report_id"`
	TaskID      uuid.UUID `gorm:"column:task_id"`
	TaskCreator uuid.UUID `gorm:"column:task_creator"`
}

type reportReviewRecord struct {
	ID         uuid.UUID `gorm:"column:id"`
	ReportID   uuid.UUID `gorm:"column:report_id"`
	ReviewedBy uuid.UUID `gorm:"column:reviewed_by"`
	Verdict    string    `gorm:"column:verdict"`
	Comment    *string   `gorm:"column:comment"`
	CreatedAt  time.Time `gorm:"column:created_at"`
}

func (reportReviewRecord) TableName() string { return "report_reviews" }

func reportReviewRecordFromDomain(review diagnosis.ReportReview) *reportReviewRecord {
	comment := review.Comment
	return &reportReviewRecord{
		ID: review.ID, ReportID: review.ReportID, ReviewedBy: review.ReviewedBy,
		Verdict: string(review.Verdict), Comment: &comment, CreatedAt: review.CreatedAt,
	}
}

func (r reportReviewRecord) toDomain() diagnosis.ReportReview {
	comment := ""
	if r.Comment != nil {
		comment = *r.Comment
	}
	return diagnosis.ReportReview{
		ID: r.ID, ReportID: r.ReportID, ReviewedBy: r.ReviewedBy,
		Verdict: diagnosis.ReviewVerdict(r.Verdict), Comment: comment, CreatedAt: r.CreatedAt,
	}
}
