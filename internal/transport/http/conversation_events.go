package httptransport

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/chitandabb/GoAgent/internal/apperror"
	"github.com/chitandabb/GoAgent/internal/conversation"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

func (r *ConversationRoutes) streamTurnEvents(
	c *gin.Context,
	actor conversation.Actor,
	conversationID, turnID uuid.UUID,
	afterSeq int64,
	batchLimit int,
) {
	stream, err := r.useCase.OpenTurnEventStream(c.Request.Context(), actor, conversationID, turnID)
	if err != nil {
		AbortWithError(c, translateConversationError("open conversation turn event stream", err))
		return
	}
	if stream == nil {
		AbortWithError(c, apperror.Wrap(apperror.CodeInternal, errors.New("conversation turn event stream is nil")))
		return
	}
	flusher, ok := c.Writer.(http.Flusher)
	if !ok {
		AbortWithError(c, apperror.Wrap(apperror.CodeInternal, errors.New("streaming is not supported")))
		return
	}
	page, err := stream.Next(c.Request.Context(), afterSeq, batchLimit)
	if err != nil {
		AbortWithError(c, translateConversationError("read conversation turn event stream", err))
		return
	}

	c.Header("Content-Type", "text/event-stream; charset=utf-8")
	c.Header("Cache-Control", "no-cache, no-transform")
	c.Header("X-Accel-Buffering", "no")
	c.Status(http.StatusOK)
	if _, err := fmt.Fprintf(c.Writer, "retry: %d\n\n", taskEventSSERetryMillis); err != nil {
		return
	}
	flusher.Flush()

	pollInterval := r.ssePollInterval
	if pollInterval <= 0 {
		pollInterval = defaultSSEPollInterval
	}
	heartbeatInterval := r.sseHeartbeatInterval
	if heartbeatInterval <= 0 {
		heartbeatInterval = defaultSSEHeartbeatInterval
	}
	poll := time.NewTicker(pollInterval)
	heartbeat := time.NewTicker(heartbeatInterval)
	defer poll.Stop()
	defer heartbeat.Stop()

	var sessionTimer *time.Timer
	var sessionExpired <-chan time.Time
	if expiresAt := identityAbsoluteExpiry(c); !expiresAt.IsZero() {
		remaining := time.Until(expiresAt)
		if remaining <= 0 {
			return
		}
		sessionTimer = time.NewTimer(remaining)
		sessionExpired = sessionTimer.C
		defer sessionTimer.Stop()
	}

	cursor := afterSeq
	initialTerminal := stream.InitialStatus().IsTerminal()
	for {
		terminalSent, writeErr := writeConversationTurnEventPage(c.Writer, page)
		if writeErr != nil {
			return
		}
		if page.NextAfterSeq > cursor {
			cursor = page.NextAfterSeq
		}
		if len(page.Items) > 0 {
			flusher.Flush()
		}
		if terminalSent || (initialTerminal && !page.HasMore) {
			return
		}
		if page.HasMore {
			page, err = stream.Next(c.Request.Context(), cursor, batchLimit)
			if err != nil {
				r.writeConversationTurnEventStreamError(c, err)
				flusher.Flush()
				return
			}
			continue
		}

		select {
		case <-c.Request.Context().Done():
			return
		case <-r.lifecycle.Done():
			return
		case <-sessionExpired:
			return
		case <-heartbeat.C:
			if _, err := io.WriteString(c.Writer, ": heartbeat\n\n"); err != nil {
				return
			}
			flusher.Flush()
		case <-poll.C:
			page, err = stream.Next(c.Request.Context(), cursor, batchLimit)
			if err != nil {
				r.writeConversationTurnEventStreamError(c, err)
				flusher.Flush()
				return
			}
		}
	}
}

func (r *ConversationRoutes) writeConversationTurnEventStreamError(c *gin.Context, cause error) {
	_ = c.Error(apperror.Wrap(apperror.CodeInternal, fmt.Errorf("read conversation turn event stream: %w", cause)))
	_ = writeSSEFrame(c.Writer, "", "error", map[string]any{
		"code": int(apperror.CodeInternal), "message": apperror.CodeInternal.Message(),
		"requestId": RequestIDFromContext(c),
	})
}

func writeConversationTurnEventPage(writer io.Writer, page conversation.TurnEventPage) (bool, error) {
	terminalSent := false
	for _, item := range page.Items {
		if item.Seq < 1 || !item.EventType.Valid() || strings.ContainsAny(string(item.EventType), "\r\n") {
			return false, errors.New("conversation turn event stream payload is invalid")
		}
		if err := writeSSEFrame(writer, strconv.FormatInt(item.Seq, 10), string(item.EventType), conversationTurnEventResponseFrom(item)); err != nil {
			return false, err
		}
		terminalSent = terminalSent || item.EventType.IsTerminal()
	}
	return terminalSent, nil
}
