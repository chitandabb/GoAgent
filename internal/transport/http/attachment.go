package httptransport

import (
	"context"
	"errors"
	"io"
	"mime"
	"net/http"
	"strings"

	"github.com/chitandabb/GoAgent/internal/apperror"
	"github.com/chitandabb/GoAgent/internal/attachment"
	"github.com/chitandabb/GoAgent/internal/knowledgeparser"
	"github.com/chitandabb/GoAgent/internal/repository"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

const attachmentPreviewRunes = 2_000

type attachmentUseCase interface {
	Upload(context.Context, attachment.UploadInput) (attachment.UploadResult, error)
	Read(context.Context, uuid.UUID, uuid.UUID, uuid.UUID, int) (attachment.ReadResult, error)
}

type AttachmentRoutes struct {
	useCase        attachmentUseCase
	auth           gin.HandlerFunc
	csrf           gin.HandlerFunc
	maxObjectBytes int64
}

func NewAttachmentRoutes(useCase attachmentUseCase, authMiddleware, csrfMiddleware gin.HandlerFunc, maxObjectBytes int64) (*AttachmentRoutes, error) {
	if useCase == nil || authMiddleware == nil || csrfMiddleware == nil || maxObjectBytes < 1 {
		return nil, errors.New("attachment route dependencies are invalid")
	}
	return &AttachmentRoutes{
		useCase: useCase, auth: authMiddleware, csrf: csrfMiddleware, maxObjectBytes: maxObjectBytes,
	}, nil
}

func (r *AttachmentRoutes) Register(api *gin.RouterGroup) {
	routes := api.Group("/conversations/:conversationId/attachments")
	routes.Use(r.auth)
	routes.GET("/:attachmentId/preview", r.preview)
	commands := routes.Group("")
	commands.Use(r.csrf)
	commands.POST("", r.upload)
}

type attachmentResponse struct {
	AttachmentID   string            `json:"attachmentId"`
	ConversationID string            `json:"conversationId"`
	Scope          attachment.Scope  `json:"scope"`
	Status         attachment.Status `json:"status"`
	OriginalName   string            `json:"originalName"`
	MediaType      string            `json:"mediaType"`
	SizeBytes      int64             `json:"sizeBytes"`
	ContentSHA256  string            `json:"contentSha256"`
	Replayed       bool              `json:"replayed"`
	UploadedAt     string            `json:"uploadedAt"`
}

type attachmentPreviewElement struct {
	Index       int      `json:"index"`
	PageNumber  *int     `json:"pageNumber,omitempty"`
	ElementType string   `json:"elementType"`
	SectionPath []string `json:"sectionPath,omitempty"`
	ContentText string   `json:"contentText"`
}

type attachmentPreviewResponse struct {
	SourceType       string                     `json:"sourceType"`
	SourceRef        string                     `json:"sourceRef"`
	AttachmentID     string                     `json:"attachmentId"`
	OriginalName     string                     `json:"originalName"`
	MediaType        string                     `json:"mediaType"`
	SizeBytes        int64                      `json:"sizeBytes"`
	ContentSHA256    string                     `json:"contentSha256"`
	ParserVersion    string                     `json:"parserVersion"`
	Elements         []attachmentPreviewElement `json:"elements"`
	VisualAssetCount int                        `json:"visualAssetCount"`
	Truncated        bool                       `json:"truncated"`
}

func (r *AttachmentRoutes) upload(c *gin.Context) {
	conversationID, ok := parseConversationID(c)
	if !ok {
		return
	}
	identity, ok := identityFromContext(c)
	if !ok {
		AbortWithError(c, apperror.New(apperror.CodeUnauthorized))
		return
	}
	idempotencyKey, err := uuid.Parse(strings.TrimSpace(c.GetHeader("Idempotency-Key")))
	if err != nil {
		AbortWithError(c, apperror.NewWithFields(apperror.CodeInvalidArgument, []apperror.FieldError{{
			Field: "Idempotency-Key", Reason: "必须是合法的 UUID",
		}}))
		return
	}
	staged, err := stageAttachmentMultipart(c, r.maxObjectBytes)
	if err != nil {
		AbortWithError(c, err)
		return
	}
	defer staged.Close()
	content, err := staged.Open()
	if err != nil {
		AbortWithError(c, apperror.Wrap(apperror.CodeInternal, err))
		return
	}
	defer content.Close()
	fingerprint := attachment.UploadFingerprint(
		attachment.ScopeSession, &conversationID, staged.originalName, staged.mediaType,
		staged.sizeBytes, staged.sha256,
	)
	result, err := r.useCase.Upload(c.Request.Context(), attachment.UploadInput{
		OwnerUserID: identity.User.ID, Scope: attachment.ScopeSession, ConversationID: &conversationID,
		IdempotencyKey: idempotencyKey, RequestFingerprint: fingerprint,
		OriginalName: staged.originalName, MediaType: staged.mediaType, SizeBytes: staged.sizeBytes,
		ContentSHA256: staged.sha256, Content: content,
	})
	if err != nil {
		AbortWithError(c, translateAttachmentError("upload attachment", err))
		return
	}
	status := http.StatusCreated
	if result.Replayed {
		status = http.StatusOK
	}
	WriteSuccessWithStatus(c, status, attachmentResponseFrom(result.Attachment, result.Replayed))
}

func (r *AttachmentRoutes) preview(c *gin.Context) {
	conversationID, ok := parseConversationID(c)
	if !ok {
		return
	}
	attachmentID, err := uuid.Parse(strings.TrimSpace(c.Param("attachmentId")))
	if err != nil {
		AbortWithError(c, apperror.NewWithFields(apperror.CodeInvalidArgument, []apperror.FieldError{{
			Field: "attachmentId", Reason: "必须是合法的 UUID",
		}}))
		return
	}
	identity, ok := identityFromContext(c)
	if !ok {
		AbortWithError(c, apperror.New(apperror.CodeUnauthorized))
		return
	}
	result, err := r.useCase.Read(
		c.Request.Context(), identity.User.ID, conversationID, attachmentID, attachmentPreviewRunes,
	)
	if err != nil {
		AbortWithError(c, translateAttachmentError("preview attachment", err))
		return
	}
	elements := make([]attachmentPreviewElement, 0, len(result.Elements))
	for _, element := range result.Elements {
		elements = append(elements, attachmentPreviewElement{
			Index: element.Index, PageNumber: element.PageNumber, ElementType: element.ElementType,
			SectionPath: element.SectionPath, ContentText: element.ContentText,
		})
	}
	WriteSuccess(c, attachmentPreviewResponse{
		SourceType: "attachment", SourceRef: "attachment:" + result.Attachment.ID.String(),
		AttachmentID: result.Attachment.ID.String(), OriginalName: result.Attachment.Ref.OriginalName,
		MediaType: result.Attachment.Ref.MediaType, SizeBytes: result.Attachment.Ref.SizeBytes,
		ContentSHA256: result.Attachment.Ref.SHA256, ParserVersion: result.ParserVersion,
		Elements: elements, VisualAssetCount: result.VisualAssetCount, Truncated: result.Truncated,
	})
}

func stageAttachmentMultipart(c *gin.Context, maxObjectBytes int64) (stagedKnowledgeSource, error) {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxObjectBytes+multipartMetadataAllowance)
	mediaType, params, err := mime.ParseMediaType(c.GetHeader("Content-Type"))
	if err != nil || !strings.EqualFold(mediaType, "multipart/form-data") || strings.TrimSpace(params["boundary"]) == "" {
		return stagedKnowledgeSource{}, invalidUploadField("Content-Type", "必须是 multipart/form-data")
	}
	reader, err := c.Request.MultipartReader()
	if err != nil {
		return stagedKnowledgeSource{}, apperror.Wrap(apperror.CodeInvalidArgument, err)
	}
	var staged stagedKnowledgeSource
	cleanup := true
	defer func() {
		if cleanup {
			_ = staged.Close()
		}
	}()
	for {
		part, nextErr := reader.NextPart()
		if errors.Is(nextErr, io.EOF) {
			break
		}
		if nextErr != nil {
			return stagedKnowledgeSource{}, multipartReadError(nextErr)
		}
		if part.FormName() != "file" || staged.path != "" || strings.TrimSpace(part.FileName()) == "" {
			_ = part.Close()
			return stagedKnowledgeSource{}, invalidUploadField("file", "必须且只能上传一个 file 字段")
		}
		staged, err = stageKnowledgeFile(part, maxObjectBytes)
		_ = part.Close()
		if err != nil {
			return stagedKnowledgeSource{}, err
		}
	}
	if staged.path == "" {
		return stagedKnowledgeSource{}, invalidUploadField("file", "必须上传文件")
	}
	cleanup = false
	return staged, nil
}

func attachmentResponseFrom(item attachment.Attachment, replayed bool) attachmentResponse {
	conversationID := ""
	if item.ConversationID != nil {
		conversationID = item.ConversationID.String()
	}
	return attachmentResponse{
		AttachmentID: item.ID.String(), ConversationID: conversationID, Scope: item.Scope, Status: item.Status,
		OriginalName: item.Ref.OriginalName, MediaType: item.Ref.MediaType, SizeBytes: item.Ref.SizeBytes,
		ContentSHA256: item.Ref.SHA256, Replayed: replayed, UploadedAt: item.UploadedAt.UTC().Format(timeRFC3339Nano),
	}
}

func translateAttachmentError(operation string, err error) error {
	switch {
	case errors.Is(err, attachment.ErrInvalidAttachment):
		return apperror.Wrap(apperror.CodeValidationFailed, err)
	case errors.Is(err, attachment.ErrIdempotencyConflict):
		return apperror.New(apperror.CodeIdempotencyConflict)
	case errors.Is(err, attachment.ErrAttachmentForbidden), errors.Is(err, repository.ErrNotFound):
		return apperror.New(apperror.CodeNotFound)
	case errors.Is(err, attachment.ErrObjectStoreUnavailable):
		return apperror.Wrap(apperror.CodeDependencyUnavailable, err)
	case errors.Is(err, knowledgeparser.ErrUnsupportedMediaType),
		errors.Is(err, knowledgeparser.ErrInvalidContent), errors.Is(err, knowledgeparser.ErrResourceLimit):
		return apperror.Wrap(apperror.CodeValidationFailed, err)
	default:
		return apperror.Wrap(apperror.CodeInternal, errors.New(operation+" failed"))
	}
}

var _ attachmentUseCase = (*attachment.Service)(nil)
