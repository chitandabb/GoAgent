package httptransport

import (
	"errors"
	"net/http"
	"time"

	"github.com/chitandabb/GoAgent/internal/diagnosis"

	"github.com/gin-gonic/gin"
)

type DiagnosisHandler struct {
	service *diagnosis.Service
}

func NewDiagnosisHandler(service *diagnosis.Service) *DiagnosisHandler {
	return &DiagnosisHandler{service: service}
}

func (h *DiagnosisHandler) Register(group *gin.RouterGroup) {
	runs := group.Group("/diagnostic-runs")
	runs.POST("", h.create)
	runs.GET("/:runID", h.get)
	runs.GET("/:runID/events", h.replayEvents)
}

type createRunRequest struct {
	SubjectType string `json:"subjectType" binding:"required"`
	SubjectID   string `json:"subjectId" binding:"required"`
	Question    string `json:"question" binding:"required"`
}

type runResponse struct {
	ID          string `json:"id"`
	SubjectType string `json:"subjectType"`
	SubjectID   string `json:"subjectId"`
	Question    string `json:"question"`
	Status      string `json:"status"`
	Summary     string `json:"summary,omitempty"`
	Error       string `json:"error,omitempty"`
	CreatedAt   string `json:"createdAt"`
	UpdatedAt   string `json:"updatedAt"`
}

type eventResponse struct {
	RunID     string         `json:"runId"`
	Sequence  int64          `json:"sequence"`
	Type      string         `json:"type"`
	Payload   map[string]any `json:"payload"`
	CreatedAt string         `json:"createdAt"`
}

func (h *DiagnosisHandler) create(c *gin.Context) {
	var request createRunRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request"})
		return
	}
	run, err := h.service.Start(c.Request.Context(), diagnosis.StartCommand{
		SubjectType: request.SubjectType,
		SubjectID:   request.SubjectID,
		Question:    request.Question,
	})
	if err != nil {
		writeDiagnosisError(c, err)
		return
	}
	c.Header("Location", "/api/v1/diagnostic-runs/"+run.ID)
	c.JSON(http.StatusAccepted, runToResponse(run))
}

func (h *DiagnosisHandler) get(c *gin.Context) {
	run, err := h.service.Get(c.Request.Context(), c.Param("runID"))
	if err != nil {
		writeDiagnosisError(c, err)
		return
	}
	c.JSON(http.StatusOK, runToResponse(run))
}

func (h *DiagnosisHandler) replayEvents(c *gin.Context) {
	events, err := h.service.Events(c.Request.Context(), c.Param("runID"))
	if err != nil {
		writeDiagnosisError(c, err)
		return
	}

	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	c.Header("X-Accel-Buffering", "no")
	for _, event := range events {
		c.SSEvent(string(event.Type), eventToResponse(event))
		c.Writer.Flush()
	}
}

func writeDiagnosisError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, diagnosis.ErrInvalidRun):
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
	case errors.Is(err, diagnosis.ErrRunNotFound):
		c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
	default:
		c.Error(err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal server error"})
	}
}

func runToResponse(run diagnosis.Run) runResponse {
	return runResponse{
		ID:          run.ID,
		SubjectType: run.SubjectType,
		SubjectID:   run.SubjectID,
		Question:    run.Request,
		Status:      string(run.Status),
		Summary:     run.Summary,
		Error:       run.Error,
		CreatedAt:   run.CreatedAt.Format(time.RFC3339Nano),
		UpdatedAt:   run.UpdatedAt.Format(time.RFC3339Nano),
	}
}

func eventToResponse(event diagnosis.Event) eventResponse {
	return eventResponse{
		RunID:     event.RunID,
		Sequence:  event.Sequence,
		Type:      string(event.Type),
		Payload:   event.Payload,
		CreatedAt: event.CreatedAt.Format(time.RFC3339Nano),
	}
}
