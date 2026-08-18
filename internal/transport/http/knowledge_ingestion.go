package httptransport

import (
	"archive/zip"
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/chitandabb/GoAgent/internal/apperror"
	"github.com/chitandabb/GoAgent/internal/knowledge"
	"github.com/chitandabb/GoAgent/internal/repository"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

const multipartMetadataAllowance int64 = 1 << 20

type knowledgeIngestionUseCase interface {
	QueueSource(context.Context, knowledge.QueueSourceInput) (knowledge.QueueVersionResult, error)
}

type KnowledgeIngestionRoutes struct {
	useCase         knowledgeIngestionUseCase
	auth            gin.HandlerFunc
	csrf            gin.HandlerFunc
	maxObjectBytes  int64
	pipelineVersion string
	maxAttempts     int
}

func NewKnowledgeIngestionRoutes(
	useCase knowledgeIngestionUseCase,
	authMiddleware gin.HandlerFunc,
	csrfMiddleware gin.HandlerFunc,
	maxObjectBytes int64,
	pipelineVersion string,
	maxAttempts int,
) (*KnowledgeIngestionRoutes, error) {
	if useCase == nil || authMiddleware == nil || csrfMiddleware == nil {
		return nil, errors.New("knowledge ingestion route dependencies are nil")
	}
	if maxObjectBytes < 1 || strings.TrimSpace(pipelineVersion) == "" || maxAttempts < 1 || maxAttempts > 10 {
		return nil, errors.New("knowledge ingestion route configuration is invalid")
	}
	return &KnowledgeIngestionRoutes{
		useCase: useCase, auth: authMiddleware, csrf: csrfMiddleware,
		maxObjectBytes: maxObjectBytes, pipelineVersion: strings.TrimSpace(pipelineVersion),
		maxAttempts: maxAttempts,
	}, nil
}

func (r *KnowledgeIngestionRoutes) Register(api *gin.RouterGroup) {
	routes := api.Group("/admin/knowledge-documents")
	routes.Use(r.auth, r.csrf)
	routes.POST("", r.createDocument)
	routes.POST("/:documentId/versions", r.createVersion)
}

type knowledgeIngestionResponse struct {
	DocumentID        string                        `json:"documentId"`
	DocumentVersionID string                        `json:"documentVersionId"`
	Version           int                           `json:"version"`
	TaskID            string                        `json:"taskId"`
	Status            knowledge.IngestionTaskStatus `json:"status"`
	Stage             knowledge.IngestionStage      `json:"stage"`
	Replayed          bool                          `json:"replayed"`
	CreatedAt         string                        `json:"createdAt"`
}

func (r *KnowledgeIngestionRoutes) createDocument(c *gin.Context) {
	r.queue(c, uuid.Nil, true)
}

func (r *KnowledgeIngestionRoutes) createVersion(c *gin.Context) {
	documentID, err := uuid.Parse(c.Param("documentId"))
	if err != nil {
		AbortWithError(c, apperror.NewWithFields(apperror.CodeInvalidArgument, []apperror.FieldError{{
			Field: "documentId", Reason: "必须是合法的 UUID",
		}}))
		return
	}
	r.queue(c, documentID, false)
}

func (r *KnowledgeIngestionRoutes) queue(c *gin.Context, documentID uuid.UUID, createDocument bool) {
	identity, ok := identityFromContext(c)
	if !ok {
		AbortWithError(c, apperror.New(apperror.CodeUnauthorized))
		return
	}
	if !identity.User.IsAdmin() {
		AbortWithError(c, apperror.New(apperror.CodeForbidden))
		return
	}
	idempotencyKey := strings.TrimSpace(c.GetHeader("Idempotency-Key"))
	if _, err := uuid.Parse(idempotencyKey); err != nil {
		AbortWithError(c, apperror.NewWithFields(apperror.CodeInvalidArgument, []apperror.FieldError{{
			Field: "Idempotency-Key", Reason: "必须是合法的 UUID",
		}}))
		return
	}

	staged, fields, err := stageKnowledgeMultipart(c, r.maxObjectBytes)
	if err != nil {
		AbortWithError(c, err)
		return
	}
	defer staged.Close()

	title := strings.TrimSpace(fields["title"])
	if createDocument {
		if title == "" {
			// 前端承诺「标题可选，留空则由服务端处理」：回落为原始文件名。
			title = staged.originalName
		}
		if title == "" || len([]rune(title)) > 512 {
			AbortWithError(c, apperror.NewWithFields(apperror.CodeInvalidArgument, []apperror.FieldError{{
				Field: "title", Reason: "不能为空且不能超过 512 个字符",
			}}))
			return
		}
		documentID = uuid.New()
	} else if title != "" {
		AbortWithError(c, apperror.NewWithFields(apperror.CodeInvalidArgument, []apperror.FieldError{{
			Field: "title", Reason: "上传新版本时不能修改标题",
		}}))
		return
	}

	fingerprint, err := knowledgeUploadFingerprint(knowledgeUploadFingerprintInput{
		Operation: func() string {
			if createDocument {
				return "create_document"
			}
			return "create_version"
		}(),
		DocumentID: func() string {
			if createDocument {
				return ""
			}
			return documentID.String()
		}(),
		Title: title, OriginalName: staged.originalName, MediaType: staged.mediaType,
		SizeBytes: staged.sizeBytes, SourceSHA256: staged.sha256,
		PipelineVersion: r.pipelineVersion, MaxAttempts: r.maxAttempts,
	})
	if err != nil {
		AbortWithError(c, apperror.Wrap(apperror.CodeInternal, err))
		return
	}

	content, err := staged.Open()
	if err != nil {
		AbortWithError(c, apperror.Wrap(apperror.CodeInternal, fmt.Errorf("open staged knowledge source: %w", err)))
		return
	}
	defer content.Close()
	correlationID := uuid.New()
	if requestID := RequestIDFromContext(c); requestID != "" {
		if parsed, parseErr := uuid.Parse(requestID); parseErr == nil {
			correlationID = parsed
		}
	}
	input := knowledge.QueueSourceInput{
		DocumentID: documentID, CreatedBy: identity.User.ID, CorrelationID: correlationID,
		Content: content, SizeBytes: staged.sizeBytes, MediaType: staged.mediaType,
		OriginalName: staged.originalName, PipelineVersion: r.pipelineVersion,
		MaxAttempts: r.maxAttempts, IdempotencyKey: idempotencyKey,
		RequestFingerprint: fingerprint, ExpectedSourceSHA256: staged.sha256,
	}
	if createDocument {
		input.NewDocument = &knowledge.CreateDocumentInput{
			ID: documentID, Scope: knowledge.ScopeGlobal, Title: title, CreatedBy: identity.User.ID,
		}
	}
	result, err := r.useCase.QueueSource(c.Request.Context(), input)
	if err != nil {
		AbortWithError(c, translateKnowledgeIngestionError(err))
		return
	}
	status := http.StatusAccepted
	if result.Replayed {
		status = http.StatusOK
	}
	WriteSuccessWithStatus(c, status, knowledgeIngestionResponse{
		DocumentID: result.Version.DocumentID.String(), DocumentVersionID: result.Version.ID.String(),
		Version: result.Version.Version, TaskID: result.Task.ID.String(), Status: result.Task.Status,
		Stage: result.Task.Stage, Replayed: result.Replayed,
		CreatedAt: result.Task.CreatedAt.UTC().Format(timeRFC3339Nano),
	})
}

type stagedKnowledgeSource struct {
	path         string
	originalName string
	mediaType    string
	sizeBytes    int64
	sha256       string
}

func (s stagedKnowledgeSource) Open() (*os.File, error) { return os.Open(s.path) }

func (s stagedKnowledgeSource) Close() error {
	if s.path == "" {
		return nil
	}
	return os.Remove(s.path)
}

func stageKnowledgeMultipart(c *gin.Context, maxObjectBytes int64) (stagedKnowledgeSource, map[string]string, error) {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxObjectBytes+multipartMetadataAllowance)
	mediaType, params, err := mime.ParseMediaType(c.GetHeader("Content-Type"))
	if err != nil || !strings.EqualFold(mediaType, "multipart/form-data") || strings.TrimSpace(params["boundary"]) == "" {
		return stagedKnowledgeSource{}, nil, apperror.NewWithFields(apperror.CodeInvalidArgument, []apperror.FieldError{{
			Field: "Content-Type", Reason: "必须是 multipart/form-data",
		}})
	}
	reader, err := c.Request.MultipartReader()
	if err != nil {
		return stagedKnowledgeSource{}, nil, apperror.Wrap(apperror.CodeInvalidArgument, err)
	}
	fields := make(map[string]string)
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
			return stagedKnowledgeSource{}, nil, multipartReadError(nextErr)
		}
		name := part.FormName()
		if name == "file" {
			if staged.path != "" || strings.TrimSpace(part.FileName()) == "" {
				_ = part.Close()
				return stagedKnowledgeSource{}, nil, invalidUploadField("file", "必须且只能上传一个文件")
			}
			staged, err = stageKnowledgeFile(part, maxObjectBytes)
			_ = part.Close()
			if err != nil {
				return stagedKnowledgeSource{}, nil, err
			}
			continue
		}
		if name != "title" || part.FileName() != "" {
			_ = part.Close()
			return stagedKnowledgeSource{}, nil, invalidUploadField(name, "不支持该 multipart 字段")
		}
		if _, exists := fields[name]; exists {
			_ = part.Close()
			return stagedKnowledgeSource{}, nil, invalidUploadField(name, "字段不能重复")
		}
		value, readErr := io.ReadAll(io.LimitReader(part, 2049))
		_ = part.Close()
		if readErr != nil {
			return stagedKnowledgeSource{}, nil, multipartReadError(readErr)
		}
		if len(value) > 2048 {
			return stagedKnowledgeSource{}, nil, invalidUploadField(name, "字段内容过长")
		}
		fields[name] = string(value)
	}
	if staged.path == "" {
		return stagedKnowledgeSource{}, nil, invalidUploadField("file", "必须上传文件")
	}
	cleanup = false
	return staged, fields, nil
}

func stageKnowledgeFile(part io.Reader, maxObjectBytes int64) (stagedKnowledgeSource, error) {
	temp, err := os.CreateTemp("", "mesguard-knowledge-*")
	if err != nil {
		return stagedKnowledgeSource{}, apperror.Wrap(apperror.CodeInternal, err)
	}
	path := temp.Name()
	ok := false
	defer func() {
		_ = temp.Close()
		if !ok {
			_ = os.Remove(path)
		}
	}()
	hasher := sha256.New()
	written, err := io.Copy(io.MultiWriter(temp, hasher), io.LimitReader(part, maxObjectBytes+1))
	if err != nil {
		return stagedKnowledgeSource{}, multipartReadError(err)
	}
	if written < 1 || written > maxObjectBytes {
		return stagedKnowledgeSource{}, invalidUploadField("file", "文件不能为空且不能超过配置上限")
	}
	if err := temp.Sync(); err != nil {
		return stagedKnowledgeSource{}, apperror.Wrap(apperror.CodeInternal, err)
	}
	if err := temp.Close(); err != nil {
		return stagedKnowledgeSource{}, apperror.Wrap(apperror.CodeInternal, err)
	}
	fileName := knowledgeFileName(part)
	canonicalName, canonicalMediaType, err := validateKnowledgeFile(path, fileName)
	if err != nil {
		return stagedKnowledgeSource{}, err
	}
	ok = true
	return stagedKnowledgeSource{
		path: path, originalName: canonicalName, mediaType: canonicalMediaType,
		sizeBytes: written, sha256: hex.EncodeToString(hasher.Sum(nil)),
	}, nil
}

type fileNamer interface{ FileName() string }

func knowledgeFileName(part io.Reader) string {
	if named, ok := part.(fileNamer); ok {
		return named.FileName()
	}
	return ""
}

func validateKnowledgeFile(path, originalName string) (string, string, error) {
	name := strings.TrimSpace(filepath.Base(originalName))
	if name == "" || name == "." || len([]rune(name)) > 512 || strings.ContainsAny(name, "\r\n\x00") {
		return "", "", invalidUploadField("file", "文件名不合法")
	}
	ext := strings.ToLower(filepath.Ext(name))
	mediaTypes := map[string]string{
		".txt": "text/plain; charset=utf-8", ".md": "text/markdown; charset=utf-8",
		".log": "text/plain; charset=utf-8", ".json": "text/plain; charset=utf-8",
		".csv": "text/plain; charset=utf-8", ".sql": "text/plain; charset=utf-8",
		".xml": "text/plain; charset=utf-8", ".yaml": "text/plain; charset=utf-8",
		".yml": "text/plain; charset=utf-8",
		".pdf": "application/pdf", ".docx": "application/vnd.openxmlformats-officedocument.wordprocessingml.document",
		".xlsx": "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet",
		".pptx": "application/vnd.openxmlformats-officedocument.presentationml.presentation",
		".png":  "image/png", ".jpg": "image/jpeg", ".jpeg": "image/jpeg",
	}
	canonicalMediaType, ok := mediaTypes[ext]
	if !ok {
		return "", "", invalidUploadField("file", "文件格式不受支持")
	}
	switch ext {
	case ".txt", ".md", ".log", ".json", ".csv", ".sql", ".xml", ".yaml", ".yml":
		if err := validateUTF8File(path); err != nil {
			return "", "", invalidUploadField("file", "文本文件必须是 UTF-8 且不能包含 NUL 字符")
		}
	case ".pdf":
		if !fileHasPrefix(path, []byte("%PDF-")) {
			return "", "", invalidUploadField("file", "PDF 文件签名与扩展名不一致")
		}
	case ".png":
		if !fileHasPrefix(path, []byte("\x89PNG\r\n\x1a\n")) {
			return "", "", invalidUploadField("file", "PNG 文件签名与扩展名不一致")
		}
	case ".jpg", ".jpeg":
		if !fileHasPrefix(path, []byte{0xff, 0xd8, 0xff}) {
			return "", "", invalidUploadField("file", "JPEG 文件签名与扩展名不一致")
		}
	case ".docx", ".xlsx", ".pptx":
		if err := validateOfficePackage(path, ext); err != nil {
			return "", "", invalidUploadField("file", err.Error())
		}
	}
	return name, canonicalMediaType, nil
}

func validateUTF8File(path string) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	reader := bufio.NewReader(file)
	for {
		r, size, readErr := reader.ReadRune()
		if errors.Is(readErr, io.EOF) {
			return nil
		}
		if readErr != nil || r == 0 || (r == utf8.RuneError && size == 1) {
			return errors.New("invalid UTF-8 text")
		}
	}
}

func validateOfficePackage(path, ext string) error {
	archive, err := zip.OpenReader(path)
	if err != nil {
		return errors.New("Office 文件不是有效的 ZIP 包")
	}
	defer archive.Close()
	want := map[string]string{
		".docx": "word/document.xml", ".xlsx": "xl/workbook.xml", ".pptx": "ppt/presentation.xml",
	}[ext]
	foundContentTypes, foundMain := false, false
	for _, file := range archive.File {
		if file.Flags&0x1 != 0 {
			return errors.New("不支持加密的 Office 文件")
		}
		switch file.Name {
		case "[Content_Types].xml":
			foundContentTypes = true
		case want:
			foundMain = true
		}
	}
	if !foundContentTypes || !foundMain {
		return errors.New("Office 文件结构与扩展名不一致")
	}
	return nil
}

func fileHasPrefix(path string, prefix []byte) bool {
	file, err := os.Open(path)
	if err != nil {
		return false
	}
	defer file.Close()
	actual := make([]byte, len(prefix))
	_, err = io.ReadFull(file, actual)
	return err == nil && string(actual) == string(prefix)
}

type knowledgeUploadFingerprintInput struct {
	Operation       string `json:"operation"`
	DocumentID      string `json:"documentId,omitempty"`
	Title           string `json:"title,omitempty"`
	OriginalName    string `json:"originalName"`
	MediaType       string `json:"mediaType"`
	SizeBytes       int64  `json:"sizeBytes"`
	SourceSHA256    string `json:"sourceSha256"`
	PipelineVersion string `json:"pipelineVersion"`
	MaxAttempts     int    `json:"maxAttempts"`
}

func knowledgeUploadFingerprint(input knowledgeUploadFingerprintInput) (string, error) {
	encoded, err := json.Marshal(input)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), nil
}

func multipartReadError(err error) error {
	var maxBytesError *http.MaxBytesError
	if errors.As(err, &maxBytesError) {
		return invalidUploadField("file", "请求体超过配置上限")
	}
	return apperror.Wrap(apperror.CodeInvalidArgument, err)
}

func invalidUploadField(field, reason string) error {
	if strings.TrimSpace(field) == "" {
		field = "multipart"
	}
	return apperror.NewWithFields(apperror.CodeInvalidArgument, []apperror.FieldError{{Field: field, Reason: reason}})
}

func translateKnowledgeIngestionError(err error) error {
	switch {
	case errors.Is(err, knowledge.ErrIdempotencyConflict):
		return apperror.New(apperror.CodeIdempotencyConflict)
	case errors.Is(err, repository.ErrNotFound):
		return apperror.New(apperror.CodeNotFound)
	case errors.Is(err, knowledge.ErrObjectStoreUnavailable):
		return apperror.New(apperror.CodeDependencyUnavailable)
	case errors.Is(err, knowledge.ErrSourceChanged):
		return apperror.New(apperror.CodeConflict)
	default:
		return apperror.Wrap(apperror.CodeInternal, fmt.Errorf("queue knowledge ingestion: %w", err))
	}
}

var _ knowledgeIngestionUseCase = (*knowledge.IngestionService)(nil)
