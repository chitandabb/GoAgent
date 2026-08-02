package diagnosis

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/chitandabb/GoAgent/internal/repository"
	"github.com/google/uuid"
)

func TestReportReviewServiceAppendsOwnerReviewAndNormalizesComment(t *testing.T) {
	reportID := uuid.New()
	ownerID := uuid.New()
	createdAt := time.Date(2026, 8, 2, 3, 4, 5, 0, time.UTC)
	repo := &reviewRepositoryStub{access: ReportAccess{ReportID: reportID, TaskID: uuid.New(), TaskCreator: ownerID}}
	service := &ReportReviewService{repository: repo, clock: func() time.Time { return createdAt }}

	review, err := service.Submit(context.Background(), ReviewActor{UserID: ownerID}, reportID, SubmitReviewInput{
		Verdict: ReviewAdopted,
		Comment: "  结论和证据一致  ",
	})
	if err != nil {
		t.Fatalf("Submit(): %v", err)
	}
	if review.ID == uuid.Nil || review.ReportID != reportID || review.ReviewedBy != ownerID ||
		review.Verdict != ReviewAdopted || review.Comment != "结论和证据一致" || !review.CreatedAt.Equal(createdAt) {
		t.Fatalf("unexpected review: %+v", review)
	}
	if len(repo.created) != 1 || repo.created[0].ID != review.ID {
		t.Fatalf("created reviews = %+v", repo.created)
	}
}

func TestReportReviewServiceAdminCanViewButCannotSubmit(t *testing.T) {
	reportID := uuid.New()
	ownerID := uuid.New()
	adminID := uuid.New()
	repo := &reviewRepositoryStub{access: ReportAccess{ReportID: reportID, TaskID: uuid.New(), TaskCreator: ownerID}}
	service := &ReportReviewService{repository: repo, clock: time.Now}

	if _, err := service.Submit(context.Background(), ReviewActor{UserID: adminID, IsAdmin: true}, reportID, SubmitReviewInput{Verdict: ReviewRejected}); !errors.Is(err, ErrReviewForbidden) {
		t.Fatalf("admin Submit() error = %v, want forbidden", err)
	}
	if _, err := service.List(context.Background(), ReviewActor{UserID: adminID, IsAdmin: true}, reportID); err != nil {
		t.Fatalf("admin List() error = %v", err)
	}
	if len(repo.created) != 0 {
		t.Fatalf("admin created reviews = %+v", repo.created)
	}
}

func TestReportReviewServiceRejectsInvalidReviewAndUnauthorizedViewer(t *testing.T) {
	reportID := uuid.New()
	ownerID := uuid.New()
	repo := &reviewRepositoryStub{access: ReportAccess{ReportID: reportID, TaskID: uuid.New(), TaskCreator: ownerID}}
	service := &ReportReviewService{repository: repo, clock: time.Now}

	if _, err := service.Submit(context.Background(), ReviewActor{UserID: ownerID}, reportID, SubmitReviewInput{Verdict: ReviewVerdict("unknown")}); !errors.Is(err, ErrInvalidReview) {
		t.Fatalf("invalid verdict error = %v, want invalid review", err)
	}
	if _, err := service.List(context.Background(), ReviewActor{UserID: uuid.New()}, reportID); !errors.Is(err, ErrReviewForbidden) {
		t.Fatalf("unauthorized List() error = %v, want forbidden", err)
	}
	if _, err := service.List(context.Background(), ReviewActor{UserID: ownerID}, uuid.New()); !errors.Is(err, repository.ErrNotFound) {
		t.Fatalf("missing report error = %v, want repository not found", err)
	}
}

type reviewRepositoryStub struct {
	access  ReportAccess
	reviews []ReportReview
	created []ReportReview
}

func (s *reviewRepositoryStub) FindReportAccess(_ context.Context, reportID uuid.UUID) (ReportAccess, error) {
	if s.access.ReportID == uuid.Nil || s.access.ReportID != reportID {
		return ReportAccess{}, repository.ErrNotFound
	}
	return s.access, nil
}

func (s *reviewRepositoryStub) ListReportReviews(context.Context, uuid.UUID) ([]ReportReview, error) {
	return append([]ReportReview(nil), s.reviews...), nil
}

func (s *reviewRepositoryStub) CreateReportReview(_ context.Context, review ReportReview) error {
	s.created = append(s.created, review)
	return nil
}
