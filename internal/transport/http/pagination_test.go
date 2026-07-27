package httptransport

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

// newPaginationTestRouter 创建仅供 HTTP 绑定测试使用的最小路由。
func newPaginationTestRouter() *gin.Engine {
	router := newTestRouter(func(context.Context) error { return nil })
	router.GET("/test-pagination", func(c *gin.Context) {
		req, ok := BindQuery[PageQuery](c)
		if !ok {
			return
		}
		req.Normalize()
		WriteSuccess(c, gin.H{
			"page":     req.Page,
			"pageSize": req.PageSize,
		})
	})
	return router
}

func TestPageQuery_Normalize(t *testing.T) {
	// Normalize 是普通 Go 方法，使用表驱动单元测试覆盖边界值。
	tests := []struct {
		name  string
		input PageQuery
		want  PageQuery
	}{
		{
			"全缺省",
			PageQuery{},
			PageQuery{Page: 1, PageSize: 20}},
		{
			"正常值",
			PageQuery{
				Page:     2,
				PageSize: 25},
			PageQuery{
				Page:     2,
				PageSize: 25,
			}},
		{
			"page0,pagesize999",
			PageQuery{
				Page:     0,
				PageSize: 999,
			},
			PageQuery{
				Page:     1,
				PageSize: 100,
			}},
		{
			"负数",
			PageQuery{
				Page:     -1,
				PageSize: -20,
			},
			PageQuery{
				Page:     1,
				PageSize: 20,
			}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.input
			got.Normalize()
			if got != tt.want {
				t.Fatalf("%s: got %+v, want %+v", tt.name, got, tt.want)
			}
		})
	}
}

func TestBindQueryNormalizesPagination(t *testing.T) {
	router := newPaginationTestRouter()
	request := httptest.NewRequest(
		http.MethodGet,
		"/test-pagination?page=0&pageSize=999",
		nil,
	)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	var got struct {
		Code int `json:"code"`
		Data struct {
			Page     int `json:"page"`
			PageSize int `json:"pageSize"`
		} `json:"data"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode response: %v, body = %s", err, response.Body.String())
	}
	if response.Code != http.StatusOK {
		t.Fatalf("status code = %d, want %d", response.Code, http.StatusOK)
	}
	if got.Code != 0 {
		t.Fatalf("code = %d, want %d", got.Code, 0)
	}
	// pageSize=999 超过上限，归一化后应该等于 100。
	actual := PageQuery{Page: got.Data.Page, PageSize: got.Data.PageSize}
	want := PageQuery{Page: 1, PageSize: 100}
	if actual != want {
		t.Fatalf("data = %+v, want %+v", actual, want)
	}
}
