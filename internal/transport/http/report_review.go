package httptransport

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/chitandabb/GoAgent/internal/apperror"
	"github.com/chitandabb/GoAgent/internal/auth"
	"github.com/chitandabb/GoAgent/internal/diagnosis"
	"github.com/chitandabb/GoAgent/internal/repository"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

type reportReviewUseCase interface {
	List(ctx context.Context, actor diagnosis.ReviewActor, reportID uuid.UUID) ([]diagnosis.ReportReview, error)
	Submit(ctx context.Context, actor diagnosis.ReviewActor, reportID uuid.UUID, input diagnosis.SubmitReviewInput) (diagnosis.ReportReview, error)
}

type ReportReviewRoutes struct {
	useCase reportReviewUseCase
	auth    gin.HandlerFunc
	csrf    gin.HandlerFunc
}

func NewReportReviewRoutes(
	useCase reportReviewUseCase,
	authMiddleware gin.HandlerFunc,
	csrfMiddleware gin.HandlerFunc,
) (*ReportReviewRoutes, error) {
	if useCase == nil || authMiddleware == nil || csrfMiddleware == nil {
		return nil, errors.New("report review route dependencies are nil")
	}
	return &ReportReviewRoutes{useCase: useCase, auth: authMiddleware, csrf: csrfMiddleware}, nil
}

func (r *ReportReviewRoutes) Register(api *gin.RouterGroup) {
	routes := api.Group("/diagnosis-reports/:reportId/reviews")
	routes.Use(r.auth)
	routes.GET("", r.list)
	create := routes.Group("")
	create.Use(r.csrf)
	create.POST("", r.create)
}

func (r *ReportReviewRoutes) list(c *gin.Context) {
	reportID, ok := parseReportID(c)
	if !ok {
		return
	}
	identity, ok := identityFromContext(c)
	if !ok {
		AbortWithError(c, apperror.New(apperror.CodeUnauthorized))
		return
	}
	reviews, err := r.useCase.List(c.Request.Context(), reviewActorFrom(identity), reportID)
	if err != nil {
		AbortWithError(c, translateReportReviewError("list report reviews", err))
		return
	}
	response := reportReviewsResponseFrom(reportID, reviews)
	WriteSuccess(c, response)
}

type createReportReviewRequest struct {
	Verdict diagnosis.ReviewVerdict `json:"verdict" binding:"required,oneof=adopted partially_adopted rejected"`
	Comment string                  `json:"comment" binding:"max=2000"`
}

func (r *ReportReviewRoutes) create(c *gin.Context) {
	reportID, ok := parseReportID(c)
	if !ok {
		return
	}
	identity, ok := identityFromContext(c)
	if !ok {
		AbortWithError(c, apperror.New(apperror.CodeUnauthorized))
		return
	}
	request, ok := BindJSON[createReportReviewRequest](c)
	if !ok {
		return
	}
	review, err := r.useCase.Submit(c.Request.Context(), reviewActorFrom(identity), reportID, diagnosis.SubmitReviewInput{
		Verdict: request.Verdict, Comment: request.Comment,
	})
	if err != nil {
		AbortWithError(c, translateReportReviewError("submit report review", err))
		return
	}
	c.Header("Location", "/api/v1/diagnosis-reports/"+reportID.String()+"/reviews")
	WriteSuccessWithStatus(c, http.StatusCreated, reportReviewResponseFrom(review))
}

func parseReportID(c *gin.Context) (uuid.UUID, bool) {
	reportID, err := uuid.Parse(c.Param("reportId"))
	if err != nil {
		AbortWithError(c, apperror.NewWithFields(apperror.CodeInvalidArgument, []apperror.FieldError{{
			Field: "reportId", Reason: "必须是合法的 UUID",
		}}))
		return uuid.Nil, false
	}
	return reportID, true
}

type reportReviewsResponse struct {
	ReportID string                 `json:"reportId"`
	Current  *reportReviewResponse  `json:"current"`
	Items    []reportReviewResponse `json:"items"`
}

type reportReviewResponse struct {
	ID         string                  `json:"id"`
	ReportID   string                  `json:"reportId"`
	ReviewedBy string                  `json:"reviewedBy"`
	Verdict    diagnosis.ReviewVerdict `json:"verdict"`
	Comment    string                  `json:"comment"`
	CreatedAt  time.Time               `json:"createdAt"`
}

func reportReviewsResponseFrom(reportID uuid.UUID, reviews []diagnosis.ReportReview) reportReviewsResponse {
	items := make([]reportReviewResponse, 0, len(reviews))
	for _, review := range reviews {
		items = append(items, reportReviewResponseFrom(review))
	}
	var current *reportReviewResponse
	if len(items) > 0 {
		latest := items[0]
		current = &latest
	}
	return reportReviewsResponse{ReportID: reportID.String(), Current: current, Items: items}
}

func reportReviewResponseFrom(review diagnosis.ReportReview) reportReviewResponse {
	return reportReviewResponse{
		ID: review.ID.String(), ReportID: review.ReportID.String(), ReviewedBy: review.ReviewedBy.String(),
		Verdict: review.Verdict, Comment: review.Comment, CreatedAt: review.CreatedAt,
	}
}

func reviewActorFrom(identity auth.Identity) diagnosis.ReviewActor {
	return diagnosis.ReviewActor{UserID: identity.User.ID, IsAdmin: identity.User.IsAdmin()}
}

func translateReportReviewError(operation string, err error) error {
	switch {
	case errors.Is(err, repository.ErrNotFound):
		return apperror.New(apperror.CodeNotFound)
	case errors.Is(err, diagnosis.ErrReviewForbidden):
		return apperror.New(apperror.CodeForbidden)
	case errors.Is(err, diagnosis.ErrInvalidReview):
		return apperror.NewWithMessage(apperror.CodeValidationFailed, "反馈内容不合法")
	default:
		return apperror.Wrap(apperror.CodeInternal, fmt.Errorf("%s: %w", operation, err))
	}
}
