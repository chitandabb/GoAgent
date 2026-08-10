//go:build integration

package httptransport

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	mesagent "github.com/chitandabb/GoAgent/internal/agent"
	"github.com/chitandabb/GoAgent/internal/attachment"
	"github.com/chitandabb/GoAgent/internal/auth"
	"github.com/chitandabb/GoAgent/internal/conversation"
	"github.com/chitandabb/GoAgent/internal/knowledgeparser"
	"github.com/chitandabb/GoAgent/internal/platform/config"
	platformminio "github.com/chitandabb/GoAgent/internal/platform/minio"
	platformpostgres "github.com/chitandabb/GoAgent/internal/platform/postgres"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	minio "github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
	"go.uber.org/zap"
	gormpostgres "gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func TestAttachmentHTTPMinIOSmoke(t *testing.T) {
	dsn := os.Getenv("MESGUARD_TEST_POSTGRES_DSN")
	endpoint := os.Getenv("MESGUARD_TEST_MINIO_ENDPOINT")
	accessKey := os.Getenv("MESGUARD_TEST_MINIO_ACCESS_KEY")
	secretKey := os.Getenv("MESGUARD_TEST_MINIO_SECRET_KEY")
	if dsn == "" || endpoint == "" || accessKey == "" || secretKey == "" {
		t.Skip("PostgreSQL and MinIO integration settings are not configured")
	}

	db, err := gorm.Open(gormpostgres.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatalf("open test postgres: %v", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("get test postgres sql db: %v", err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	tx := db.WithContext(ctx).Begin()
	if tx.Error != nil {
		t.Fatalf("begin fixture transaction: %v", tx.Error)
	}
	t.Cleanup(func() { _ = tx.Rollback().Error })

	ownerID, otherID := uuid.New(), uuid.New()
	if err := tx.Exec(`
INSERT INTO users (id, username, display_name, password_hash, role, status, must_change_password)
VALUES (?, ?, 'Attachment HTTP Owner', 'integration-hash', 'analyst', 'active', false),
       (?, ?, 'Attachment HTTP Other', 'integration-hash', 'analyst', 'active', false)`,
		ownerID, "attachment_http_owner_"+uuid.NewString()[:8],
		otherID, "attachment_http_other_"+uuid.NewString()[:8]).Error; err != nil {
		t.Fatalf("insert users: %v", err)
	}

	suffix := strings.ReplaceAll(uuid.NewString(), "-", "")[:12]
	attachmentBucket := "mesguard-http-attachments-" + suffix
	knowledgeBucket := "mesguard-http-knowledge-" + suffix
	rawMinIO, err := minio.New(endpoint, &minio.Options{
		Creds: credentials.NewStaticV4(accessKey, secretKey, ""), Secure: false,
	})
	if err != nil {
		t.Fatalf("open MinIO cleanup client: %v", err)
	}
	removeAttachmentSmokeBucketsByPrefix(ctx, t, rawMinIO)
	store, err := platformminio.Open(ctx, config.MinIOConfig{
		Enabled: true, Endpoint: endpoint,
		AccessKeyEnv: "MESGUARD_TEST_MINIO_ACCESS_KEY", SecretKeyEnv: "MESGUARD_TEST_MINIO_SECRET_KEY",
		Region: "us-east-1", AttachmentBucket: attachmentBucket, KnowledgeSourceBucket: knowledgeBucket,
		AutoCreateBuckets: true, TimeoutMillis: 5_000, MaxObjectBytes: 1024 * 1024,
	})
	if err != nil {
		t.Fatalf("open MinIO store: %v", err)
	}

	attachmentRepository := platformpostgres.NewAttachmentRepository(tx)
	var uploaded attachment.Attachment
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cleanupCancel()
		if uploaded.ID != uuid.Nil {
			if err := store.Remove(cleanupCtx, uploaded.Ref); err != nil {
				t.Errorf("remove smoke object: %v", err)
			}
		}
		for _, bucket := range []string{attachmentBucket, knowledgeBucket} {
			removeAttachmentSmokeBucket(cleanupCtx, t, rawMinIO, bucket)
		}
	})

	parser, err := knowledgeparser.NewRouter(knowledgeparser.TextParser{})
	if err != nil {
		t.Fatalf("new attachment parser router: %v", err)
	}
	attachmentService, err := attachment.NewService(
		attachmentRepository, store, parser, 1024*1024,
	)
	if err != nil {
		t.Fatalf("new attachment service: %v", err)
	}
	conversationRepository := platformpostgres.NewConversationRepository(tx)
	conversationService, err := conversation.NewService(conversationRepository)
	if err != nil {
		t.Fatalf("new conversation service: %v", err)
	}
	authMiddleware := attachmentSmokeIdentity(ownerID)
	csrfMiddleware := func(c *gin.Context) { c.Next() }
	conversationRoutes, err := NewConversationRoutes(ctx, conversationService, authMiddleware, csrfMiddleware)
	if err != nil {
		t.Fatalf("new conversation routes: %v", err)
	}
	attachmentRoutes, err := NewAttachmentRoutes(attachmentService, authMiddleware, csrfMiddleware, 1024*1024)
	if err != nil {
		t.Fatalf("new attachment routes: %v", err)
	}
	router := NewRouter(zap.NewNop(), func(context.Context) error { return nil }, conversationRoutes, attachmentRoutes)

	conversationRecorder := httptest.NewRecorder()
	conversationRequest := httptest.NewRequest(
		http.MethodPost, "/api/v1/conversations", strings.NewReader(`{"title":"MinIO HTTP smoke"}`),
	)
	conversationRequest.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(conversationRecorder, conversationRequest)
	if conversationRecorder.Code != http.StatusCreated {
		t.Fatalf("create conversation status=%d body=%s", conversationRecorder.Code, conversationRecorder.Body.String())
	}
	var createdConversation conversationResponse
	decodeAttachmentSmokeData(t, conversationRecorder, &createdConversation)
	conversationID := uuid.MustParse(createdConversation.ID)

	content := []byte("MESGuard attachment smoke\nretry after 30 seconds\n")
	digest := sha256.Sum256(content)
	wantSHA256 := hex.EncodeToString(digest[:])
	idempotencyKey := uuid.New()
	upload := func() *httptest.ResponseRecorder {
		recorder := httptest.NewRecorder()
		request := attachmentSmokeUploadRequest(t, conversationID, idempotencyKey, "incident.txt", content)
		router.ServeHTTP(recorder, request)
		return recorder
	}
	uploadRecorder := upload()
	if uploadRecorder.Code != http.StatusCreated {
		t.Fatalf("upload status=%d body=%s", uploadRecorder.Code, uploadRecorder.Body.String())
	}
	var uploadedResponse attachmentResponse
	decodeAttachmentSmokeData(t, uploadRecorder, &uploadedResponse)
	attachmentID := uuid.MustParse(uploadedResponse.AttachmentID)
	if uploadedResponse.ConversationID != conversationID.String() || uploadedResponse.ContentSHA256 != wantSHA256 ||
		!strings.HasPrefix(uploadedResponse.MediaType, "text/plain") || uploadedResponse.Replayed {
		t.Fatalf("uploaded response=%+v", uploadedResponse)
	}

	uploaded, err = attachmentRepository.FindByIdempotency(ctx, ownerID, idempotencyKey)
	if err != nil {
		t.Fatalf("load uploaded attachment: %v", err)
	}
	replayRecorder := upload()
	if replayRecorder.Code != http.StatusOK {
		t.Fatalf("replay upload status=%d body=%s", replayRecorder.Code, replayRecorder.Body.String())
	}
	var replayedResponse attachmentResponse
	decodeAttachmentSmokeData(t, replayRecorder, &replayedResponse)
	if !replayedResponse.Replayed || replayedResponse.AttachmentID != attachmentID.String() {
		t.Fatalf("replayed response=%+v", replayedResponse)
	}

	readTool, err := mesagent.NewReadAttachmentTool(attachmentService)
	if err != nil {
		t.Fatalf("new read attachment Tool: %v", err)
	}
	unlinkedCtx := conversation.WithCommandContext(ctx, conversation.CommandContext{
		ConversationID: conversationID, UserMessageID: uuid.New(), Actor: conversation.Actor{UserID: ownerID},
	})
	if _, err := readTool.InvokableRun(unlinkedCtx, `{"attachmentId":"`+attachmentID.String()+`"}`); err == nil {
		t.Fatal("unlinked upload was readable by the Agent Tool")
	}

	messageRecorder := httptest.NewRecorder()
	messageRequest := httptest.NewRequest(
		http.MethodPost, "/api/v1/conversations/"+conversationID.String()+"/messages",
		strings.NewReader(`{"content":"请分析这个日志","attachments":[{"attachmentId":"`+attachmentID.String()+`","purpose":"incident_log"}]}`),
	)
	messageRequest.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(messageRecorder, messageRequest)
	if messageRecorder.Code != http.StatusCreated {
		t.Fatalf("append message status=%d body=%s", messageRecorder.Code, messageRecorder.Body.String())
	}
	var message conversationMessageResponse
	decodeAttachmentSmokeData(t, messageRecorder, &message)
	messageID := uuid.MustParse(message.ID)
	if len(message.Attachments) != 1 || message.Attachments[0].AttachmentID != attachmentID.String() ||
		message.Attachments[0].ContentSHA256 != wantSHA256 || message.Attachments[0].Purpose != "incident_log" {
		t.Fatalf("message attachments=%+v", message.Attachments)
	}

	linkedCtx := conversation.WithCommandContext(ctx, conversation.CommandContext{
		ConversationID: conversationID, UserMessageID: messageID, Actor: conversation.Actor{UserID: ownerID},
	})
	toolResult, err := readTool.InvokableRun(linkedCtx, `{"attachmentId":"`+attachmentID.String()+`"}`)
	if err != nil {
		t.Fatalf("read linked attachment Tool: %v", err)
	}
	if !strings.Contains(toolResult, "retry after 30 seconds") ||
		!strings.Contains(toolResult, `"sourceRef":"attachment:`+attachmentID.String()+`"`) ||
		!strings.Contains(toolResult, `"contentSha256":"`+wantSHA256+`"`) {
		t.Fatalf("unexpected Tool result=%s", toolResult)
	}

	previewRecorder := httptest.NewRecorder()
	previewRequest := httptest.NewRequest(
		http.MethodGet,
		"/api/v1/conversations/"+conversationID.String()+"/attachments/"+attachmentID.String()+"/preview",
		nil,
	)
	router.ServeHTTP(previewRecorder, previewRequest)
	if previewRecorder.Code != http.StatusOK {
		t.Fatalf("preview status=%d body=%s", previewRecorder.Code, previewRecorder.Body.String())
	}
	var preview attachmentPreviewResponse
	decodeAttachmentSmokeData(t, previewRecorder, &preview)
	if preview.SourceRef != "attachment:"+attachmentID.String() || preview.ContentSHA256 != wantSHA256 ||
		len(preview.Elements) != 1 || !strings.Contains(preview.Elements[0].ContentText, "retry after 30 seconds") {
		t.Fatalf("preview=%+v", preview)
	}

	for _, output := range []string{uploadRecorder.Body.String(), replayRecorder.Body.String(), messageRecorder.Body.String(), toolResult, previewRecorder.Body.String()} {
		if strings.Contains(output, attachmentBucket) || strings.Contains(output, uploaded.Ref.ObjectKey) ||
			strings.Contains(output, uploaded.Ref.ETag) || strings.Contains(output, secretKey) {
			t.Fatal("attachment response leaked object-store coordinates or credentials")
		}
	}

	otherRoutes, err := NewAttachmentRoutes(
		attachmentService, attachmentSmokeIdentity(otherID), csrfMiddleware, 1024*1024,
	)
	if err != nil {
		t.Fatalf("new cross-user attachment routes: %v", err)
	}
	otherRouter := NewRouter(zap.NewNop(), func(context.Context) error { return nil }, otherRoutes)
	forbiddenRecorder := httptest.NewRecorder()
	otherRouter.ServeHTTP(forbiddenRecorder, previewRequest.Clone(ctx))
	if forbiddenRecorder.Code != http.StatusNotFound ||
		strings.Contains(forbiddenRecorder.Body.String(), attachmentID.String()) ||
		strings.Contains(forbiddenRecorder.Body.String(), wantSHA256) {
		t.Fatalf("cross-user preview status=%d body=%s", forbiddenRecorder.Code, forbiddenRecorder.Body.String())
	}
}

func attachmentSmokeIdentity(userID uuid.UUID) gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Set(identityKey, auth.Identity{User: auth.User{
			ID: userID, Role: auth.RoleAnalyst, Status: auth.UserStatusActive,
		}})
		c.Next()
	}
}

func attachmentSmokeUploadRequest(
	t *testing.T,
	conversationID, idempotencyKey uuid.UUID,
	name string,
	content []byte,
) *http.Request {
	t.Helper()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile("file", name)
	if err != nil {
		t.Fatalf("create multipart file: %v", err)
	}
	if _, err := part.Write(content); err != nil {
		t.Fatalf("write multipart file: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close multipart writer: %v", err)
	}
	request := httptest.NewRequest(
		http.MethodPost, "/api/v1/conversations/"+conversationID.String()+"/attachments", &body,
	)
	request.Header.Set("Content-Type", writer.FormDataContentType())
	request.Header.Set("Idempotency-Key", idempotencyKey.String())
	return request
}

func decodeAttachmentSmokeData(t *testing.T, recorder *httptest.ResponseRecorder, target any) {
	t.Helper()
	var envelope struct {
		Code int             `json:"code"`
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode response envelope: %v body=%s", err, recorder.Body.String())
	}
	if envelope.Code != 0 || len(envelope.Data) == 0 {
		t.Fatalf("unexpected response envelope code=%d body=%s", envelope.Code, recorder.Body.String())
	}
	if err := json.Unmarshal(envelope.Data, target); err != nil {
		t.Fatalf("decode response data: %v data=%s", err, envelope.Data)
	}
}

func removeAttachmentSmokeBucketsByPrefix(ctx context.Context, t *testing.T, client *minio.Client) {
	t.Helper()
	buckets, err := client.ListBuckets(ctx)
	if err != nil {
		t.Fatalf("list smoke buckets: %v", err)
	}
	for _, bucket := range buckets {
		if strings.HasPrefix(bucket.Name, "mesguard-http-attachments-") ||
			strings.HasPrefix(bucket.Name, "mesguard-http-knowledge-") {
			removeAttachmentSmokeBucket(ctx, t, client, bucket.Name)
		}
	}
}

func removeAttachmentSmokeBucket(ctx context.Context, t *testing.T, client *minio.Client, bucket string) {
	t.Helper()
	for object := range client.ListObjects(ctx, bucket, minio.ListObjectsOptions{Recursive: true}) {
		if object.Err != nil {
			t.Errorf("list smoke bucket objects: %v", object.Err)
			continue
		}
		if err := client.RemoveObject(ctx, bucket, object.Key, minio.RemoveObjectOptions{}); err != nil {
			t.Errorf("remove smoke bucket object: %v", err)
		}
	}
	if err := client.RemoveBucket(ctx, bucket); err != nil {
		t.Errorf("remove smoke bucket: %v", err)
	}
}
