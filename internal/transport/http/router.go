package httptransport

import (
	"context"

	"github.com/chitandabb/GoAgent/internal/apperror"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

// HealthCheck 定义健康检查函数，Router 不需要知道数据库和 Redis 的具体类型。
type HealthCheck func(context.Context) error

// NewRouter 创建并配置 Gin Engine。
// 所有全局中间件和基础路由都集中在这里注册。
func NewRouter(log *zap.Logger, health HealthCheck, authRoutes ...*AuthRoutes) *gin.Engine {
	router := gin.New()
	router.HandleMethodNotAllowed = true

	// 中间件按照注册顺序进入，按照相反顺序退出。
	router.Use(RequestID())
	router.Use(RequestLogger(log))
	router.Use(Recovery(log))
	router.Use(ErrorHandler(log))

	router.GET("/healthz", func(c *gin.Context) {
		if err := health(c.Request.Context()); err != nil {
			AbortWithError(c, apperror.Wrap(apperror.CodeDependencyUnavailable, err))
			return
		}
		WriteSuccess(c, gin.H{"status": "ok"})
	})

	if len(authRoutes) > 0 && authRoutes[0] != nil {
		authRoutes[0].Register(router.Group("/api/v1"))
	}

	// 未匹配的路由和请求方法也必须使用统一响应格式。
	router.NoRoute(func(c *gin.Context) {
		WriteError(c, apperror.New(apperror.CodeNotFound))
	})
	router.NoMethod(func(c *gin.Context) {
		WriteError(c, apperror.New(apperror.CodeMethodNotAllowed))
	})

	return router
}
