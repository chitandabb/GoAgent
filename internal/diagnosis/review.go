package diagnosis

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
)

// ReviewVerdict 是对已生成报告的业务反馈，不是任务审批状态。
type ReviewVerdict string

const (
	ReviewAdopted          ReviewVerdict = "adopted"
	ReviewPartiallyAdopted ReviewVerdict = "partially_adopted"
	ReviewRejected         ReviewVerdict = "rejected"
)

func (v ReviewVerdict) Valid() bool {
	switch v {
	case ReviewAdopted, ReviewPartiallyAdopted, ReviewRejected:
		return true
	default:
		return false
	}
}

var (
	ErrReviewForbidden = errors.New("report review is forbidden")
	ErrInvalidReview   = errors.New("report review is invalid")
)

// ReportAccess 是报告与任务创建者之间的最小授权事实。
type ReportAccess struct {
	ReportID    uuid.UUID
	TaskID      uuid.UUID
	TaskCreator uuid.UUID
}

// ReportReview 是追加保存的人工反馈事实。
type ReportReview struct {
	ID         uuid.UUID
	ReportID   uuid.UUID
	ReviewedBy uuid.UUID
	Verdict    ReviewVerdict
	Comment    string
	CreatedAt  time.Time
}

type ReviewActor struct {
	UserID  uuid.UUID
	IsAdmin bool
}

type SubmitReviewInput struct {
	Verdict ReviewVerdict
	Comment string
}

// ReportReviewRepository 由 PostgreSQL 适配器实现；诊断 Service 不感知 GORM。
type ReportReviewRepository interface {
	FindReportAccess(ctx context.Context, reportID uuid.UUID) (ReportAccess, error)
	ListReportReviews(ctx context.Context, reportID uuid.UUID) ([]ReportReview, error)
	CreateReportReview(ctx context.Context, review ReportReview) error
}

type ReportReviewService struct {
	repository ReportReviewRepository
	clock      func() time.Time
}

func NewReportReviewService(repository ReportReviewRepository) (*ReportReviewService, error) {
	if repository == nil {
		return nil, errors.New("report review repository is required")
	}
	return &ReportReviewService{repository: repository, clock: func() time.Time { return time.Now().UTC() }}, nil
}

func (s *ReportReviewService) List(
	ctx context.Context,
	actor ReviewActor,
	reportID uuid.UUID,
) ([]ReportReview, error) {
	if s == nil || s.repository == nil {
		return nil, errors.New("report review service is unavailable")
	}
	access, err := s.repository.FindReportAccess(ctx, reportID)
	if err != nil {
		return nil, err
	}
	if err := authorizeReviewView(actor, access); err != nil {
		return nil, err
	}
	return s.repository.ListReportReviews(ctx, reportID)
}

func (s *ReportReviewService) Submit(
	ctx context.Context,
	actor ReviewActor,
	reportID uuid.UUID,
	input SubmitReviewInput,
) (ReportReview, error) {
	if s == nil || s.repository == nil {
		return ReportReview{}, errors.New("report review service is unavailable")
	}
	if actor.UserID == uuid.Nil {
		return ReportReview{}, ErrReviewForbidden
	}
	access, err := s.repository.FindReportAccess(ctx, reportID)
	if err != nil {
		return ReportReview{}, err
	}
	// 管理员可以查看反馈，但不能代替任务创建者追加业务反馈。
	if actor.UserID != access.TaskCreator {
		return ReportReview{}, ErrReviewForbidden
	}

	verdict := ReviewVerdict(strings.TrimSpace(string(input.Verdict)))
	comment := strings.TrimSpace(input.Comment)
	if !verdict.Valid() || len([]rune(comment)) > 2000 {
		return ReportReview{}, ErrInvalidReview
	}
	review := ReportReview{
		ID:         uuid.New(),
		ReportID:   reportID,
		ReviewedBy: actor.UserID,
		Verdict:    verdict,
		Comment:    comment,
		CreatedAt:  s.clock().UTC(),
	}
	if err := s.repository.CreateReportReview(ctx, review); err != nil {
		return ReportReview{}, err
	}
	return review, nil
}

func authorizeReviewView(actor ReviewActor, access ReportAccess) error {
	if actor.UserID == uuid.Nil || (!actor.IsAdmin && actor.UserID != access.TaskCreator) {
		return ErrReviewForbidden
	}
	return nil
}
