package httptransport

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

// createDemoRequest 是仅用于测试绑定层的示例 DTO。
// binding tag 覆盖了当前翻译表支持的典型规则。
type createDemoRequest struct {
	Title string `json:"title" binding:"required,min=2"`
	Level int    `json:"level" binding:"omitempty,oneof=1 2 3"`
}

func newBindingTestRouter() *gin.Engine {
	router := newTestRouter(func(context.Context) error { return nil })
	router.POST("/test-bind", func(c *gin.Context) {
		req, ok := BindJSON[createDemoRequest](c)
		if !ok {
			return
		}
		WriteSuccess(c, gin.H{"title": req.Title})
	})
	return router
}

func TestBindJSON(t *testing.T) {
	// 表驱动测试：每行一个场景，结构相同、数据不同。
	// wantContains 逐条断言响应体片段，方便失败时直接看出缺了什么。
	tests := []struct {
		name         string
		body         string
		wantStatus   int
		wantContains []string
	}{
		{
			name:         "合法请求返回成功信封",
			body:         `{"title":"演示工单","level":2}`,
			wantStatus:   http.StatusOK,
			wantContains: []string{`"code":0`, `"title":"演示工单"`},
		},
		{
			name:         "JSON 语法错误返回通用参数错误",
			body:         `{"title":`,
			wantStatus:   http.StatusBadRequest,
			wantContains: []string{`"code":40001`, `"message":"请求参数错误"`},
		},
		{
			name:         "类型不匹配返回通用参数错误不泄露内部结构",
			body:         `{"title":"演示","level":"high"}`,
			wantStatus:   http.StatusBadRequest,
			wantContains: []string{`"code":40001`},
		},
		{
			name:         "缺少必填字段返回字段级提示",
			body:         `{"level":1}`,
			wantStatus:   http.StatusBadRequest,
			wantContains: []string{`"code":40001`, `"field":"title"`, `"reason":"必填"`},
		},
		{
			name:         "取值超出枚举返回字段级提示",
			body:         `{"title":"演示工单","level":9}`,
			wantStatus:   http.StatusBadRequest,
			wantContains: []string{`"code":40001`, `"field":"level"`},
		},
	}

	router := newBindingTestRouter()
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodPost, "/test-bind", strings.NewReader(tt.body))
			request.Header.Set("Content-Type", "application/json")
			response := httptest.NewRecorder()

			router.ServeHTTP(response, request)

			if response.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d, body = %s", response.Code, tt.wantStatus, response.Body.String())
			}
			for _, want := range tt.wantContains {
				if !strings.Contains(response.Body.String(), want) {
					t.Fatalf("body = %s, want contains %s", response.Body.String(), want)
				}
			}
		})
	}
}

// 42201 属于业务校验失败，由 Service 层产生；这里只固定它的 HTTP 映射。
func TestValidationFailedCodeMapsToUnprocessableEntity(t *testing.T) {
	if got := httpStatus(42201); got != http.StatusUnprocessableEntity {
		t.Fatalf("httpStatus(42201) = %d, want %d", got, http.StatusUnprocessableEntity)
	}
}
