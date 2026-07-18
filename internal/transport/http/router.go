package httptransport

import (
	"context"
	"net/http"

	"GopherAI/internal/diagnosis"

	"github.com/gin-gonic/gin"
)

type HealthCheck func(context.Context) error

func NewRouter(diagnosisService *diagnosis.Service, health HealthCheck) *gin.Engine {
	router := gin.New()
	router.Use(gin.Logger(), gin.Recovery())
	router.GET("/healthz", func(c *gin.Context) {
		if err := health(c.Request.Context()); err != nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{"status": "unavailable", "error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"status": "ok"})
	})

	NewDiagnosisHandler(diagnosisService).Register(router.Group("/api/v1"))
	return router
}
