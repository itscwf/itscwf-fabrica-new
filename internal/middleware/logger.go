// Package middleware reune os middlewares HTTP transversais do backend.
package middleware

import (
	"log/slog"
	"time"

	"github.com/gin-gonic/gin"
)

// Logger emite uma linha de log estruturada por requisicao, com nivel derivado
// do status HTTP (5xx -> error, 4xx -> warn, resto -> info).
func Logger(logger *slog.Logger) gin.HandlerFunc {
	if logger == nil {
		logger = slog.Default()
	}

	return func(c *gin.Context) {
		start := time.Now()
		path := c.Request.URL.Path
		query := c.Request.URL.RawQuery

		c.Next()

		attrs := []any{
			"method", c.Request.Method,
			"path", path,
			"status", c.Writer.Status(),
			"duration_ms", time.Since(start).Milliseconds(),
			"client_ip", c.ClientIP(),
		}
		if query != "" {
			attrs = append(attrs, "query", query)
		}
		if len(c.Errors) > 0 {
			attrs = append(attrs, "errors", c.Errors.String())
		}

		switch {
		case c.Writer.Status() >= 500:
			logger.Error("http_request", attrs...)
		case c.Writer.Status() >= 400:
			logger.Warn("http_request", attrs...)
		default:
			logger.Info("http_request", attrs...)
		}
	}
}
