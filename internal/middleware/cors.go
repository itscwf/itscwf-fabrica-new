package middleware

import (
	"time"

	"github.com/gin-contrib/cors"
	"github.com/gin-gonic/gin"
)

// CORS monta a politica de CORS da API.
//
// origins vazio ou ["*"] libera qualquer origem (desenvolvimento); caso
// contrario apenas as origens listadas sao aceitas. Credenciais ficam
// desabilitadas porque o backend usa Bearer token, nao cookie de sessao.
func CORS(origins []string) gin.HandlerFunc {
	cfg := cors.Config{
		AllowMethods:     []string{"GET", "POST", "PUT", "PATCH", "DELETE", "OPTIONS"},
		AllowHeaders:     []string{"Origin", "Content-Type", "Accept", "Authorization", "X-Requested-With", "X-Tenant-Id"},
		ExposeHeaders:    []string{"Content-Length", "Content-Disposition"},
		AllowCredentials: false,
		MaxAge:           12 * time.Hour,
	}

	if isWildcard(origins) {
		cfg.AllowAllOrigins = true
	} else {
		cfg.AllowOrigins = origins
	}

	return cors.New(cfg)
}

func isWildcard(origins []string) bool {
	if len(origins) == 0 {
		return true
	}
	return len(origins) == 1 && origins[0] == "*"
}
