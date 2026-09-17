// Package handlers monta o roteador HTTP e implementa a API REST da Fabrica
// ITSCWF (card T3).
package handlers

import (
	"log/slog"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"github.com/itscwf/itscwf-fabrica-new/internal/config"
	"github.com/itscwf/itscwf-fabrica-new/internal/middleware"
	"github.com/itscwf/itscwf-fabrica-new/pkg/fabrica"
)

// Deps agrupa as dependencias compartilhadas pelos handlers.
type Deps struct {
	Config   *config.Config
	DB       *gorm.DB
	Logger   *slog.Logger
	Hermes      *fabrica.HermesClient
	Kanban      fabrica.KanbanService
	Auth        *middleware.JWTAuth
	Recorder *Recorder
}

// NewRouter constroi o *gin.Engine com middlewares, probes e a API v1.
func NewRouter(deps Deps) *gin.Engine {
	if deps.Config != nil && deps.Config.IsProduction() {
		gin.SetMode(gin.ReleaseMode)
	}

	logger := deps.Logger
	if logger == nil {
		logger = slog.Default()
	}

	router := gin.New()
	router.Use(gin.Recovery(), middleware.Logger(logger))
	if deps.Config != nil {
		router.Use(middleware.CORS(deps.Config.CORSOrigins))
	}

	health := &healthHandler{db: deps.DB, started: time.Now()}

	// Probes publicos (sem autenticacao): usados pelo HEALTHCHECK do container,
	// pelo nginx e por monitores externos.
	router.GET("/health", health.Health)
	router.GET("/healthz", health.Health)
	router.GET("/health/live", health.Live)
	router.GET("/health/ready", health.Ready)

	api := router.Group(fabrica.APIPrefix)
	api.GET("/health", health.Health)
	api.GET("/health/live", health.Live)
	api.GET("/health/ready", health.Ready)
	registerAPIRoutes(api, deps)

	router.NoRoute(func(c *gin.Context) {
		fail(c, http.StatusNotFound, codeNotFound, "rota nao encontrada: "+c.Request.Method+" "+c.Request.URL.Path)
	})
	router.NoMethod(func(c *gin.Context) {
		fail(c, http.StatusMethodNotAllowed, codeBadRequest, "metodo nao permitido: "+c.Request.Method)
	})

	return router
}

// registerAPIRoutes registra autenticacao, recursos CRUD e integracoes Hermes.
func registerAPIRoutes(api *gin.RouterGroup, deps Deps) {
	if deps.Config == nil || deps.DB == nil || deps.Auth == nil {
		return
	}

	if deps.Recorder == nil {
		deps.Recorder = NewRecorder(deps.DB)
	}

	auth := newAuthHandlers(deps.Config, deps.Auth.Manager(), deps.Recorder)
	api.POST("/auth/login", auth.Login)

	authed := api.Group("", deps.Auth.Handler())
	authed.GET("/auth/me", auth.Me)
	authed.POST("/auth/refresh", auth.Refresh)

	RegisterResources(authed, &deps)

	hermes := newHermesHandlers(deps.Kanban, deps.Hermes, time.Duration(deps.Config.Hermes.CacheTTLSecs)*time.Second, deps.Config.Hermes.KanbanBoard)
	authed.GET("/hermes/tasks", hermes.Tasks)
	authed.GET("/hermes/boards", hermes.Boards)
	authed.GET("/hermes/tasks/:id", hermes.Task)
	authed.GET("/hermes/tasks/:id/runs", hermes.TaskRuns)
	authed.POST("/hermes/tasks", hermes.CreateTask)
	authed.GET("/hermes/agents", hermes.Agents)
	authed.GET("/hermes/agents/profiles", hermes.AgentsProfilesHandler)
	authed.GET("/hermes/providers", hermes.ProvidersHandler)
	authed.GET("/hermes/crons", hermes.Crons)

	stats := &statsHandlers{deps: &deps}
	authed.GET("/stats", stats.Stats)
	authed.GET("/dashboard", stats.Stats)
}

// healthHandler responde aos probes de liveness/readiness e ao resumo.
type healthHandler struct {
	db      *gorm.DB
	started time.Time
}

// Live responde 200 sempre que o processo esta de pe (usado pelo HEALTHCHECK).
func (h *healthHandler) Live(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{"status": "ok", "service": fabrica.Name, "version": fabrica.Version})
}

// Health devolve metadados do servico e o estado do banco. Sempre 200: um
// banco indisponivel aparece como "degraded" no payload.
func (h *healthHandler) Health(c *gin.Context) {
	payload := gin.H{
		"status":        "ok",
		"service":       fabrica.Name,
		"version":       fabrica.Version,
		"commit":        fabrica.Commit,
		"uptime_s":      int64(time.Since(h.started).Seconds()),
		"time":          time.Now().UTC().Format(time.RFC3339),
		"api":           fabrica.APIPrefix,
		"journal_mode":  "",
		"database_path": "",
	}
	if h.db == nil {
		c.JSON(http.StatusOK, payload)
		return
	}
	if mode, err := journalModeOf(h.db); err == nil {
		payload["journal_mode"] = mode
	}
	var path string
	if err := h.db.Raw("PRAGMA database_list;").Scan(&[]struct {
		Seq  int
		Name string
		File string
	}{}); err == nil {
		_ = err
	}
	_ = path
	var total int64
	start := time.Now()
	if err := h.db.Raw("SELECT count(*) FROM sqlite_master WHERE type = 'table'").Scan(&total).Error; err != nil {
		payload["status"] = "degraded"
		payload["database"] = "error: " + err.Error()
	} else {
		payload["database"] = "ok"
		payload["tables"] = total
		payload["db_latency_ms"] = time.Since(start).Milliseconds()
	}
	c.JSON(http.StatusOK, payload)
}

// Ready responde 503 quando o banco nao esta acessivel.
func (h *healthHandler) Ready(c *gin.Context) {
	if h.db == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"status": "unavailable", "database": "sem conexao"})
		return
	}
	sqlDB, err := h.db.DB()
	if err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"status": "unavailable", "database": err.Error()})
		return
	}
	if err := sqlDB.PingContext(c.Request.Context()); err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"status": "unavailable", "database": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "ready"})
}
