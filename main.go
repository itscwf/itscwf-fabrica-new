package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/gin-contrib/cors"
	"github.com/gin-gonic/gin"
	"github.com/spf13/viper"
	_ "github.com/mattn/go-sqlite3"

	"github.com/itscwf/itscwf-fabrica-new/internal/models"
	"github.com/itscwf/itscwf-fabrica-new/internal/handlers"
)

// --- Hermes CLI wrapper (from hermes-kanban-api) ---

type HermesResult struct {
	Command    string   `json:"command"`
	Args       []string `json:"args"`
	ExitCode   int      `json:"exit_code"`
	DurationMs int64    `json:"duration_ms"`
	Stdout     string   `json:"stdout"`
	Stderr     string   `json:"stderr"`
	Error      string   `json:"error,omitempty"`
	OK         bool     `json:"ok"`
}

var blockedEnv = []string{
	"HERMES_DELEGATED_CHILD_CONTEXT",
	"HERMES_KANBAN_TASK",
	"HERMES_KANBAN_DB",
	"HERMES_KANBAN_WORKSPACE",
	"HERMES_KANBAN_WORKSPACES_ROOT",
	"HERMES_KANBAN_BRANCH",
	"HERMES_KANBAN_RUN_ID",
	"HERMES_CRON_SESSION",
	"HERMES_SINGLE_QUERY_SESSION",
	"HERMES_INTERACTIVE",
	"HERMES_EXEC_ASK",
	"HERMES_QUIET",
	"HERMES_AGENT",
	"AI_AGENT",
}

func buildEnv(extra map[string]string) []string {
	skip := make(map[string]bool, len(blockedEnv)+len(extra))
	for _, k := range blockedEnv {
		skip[k] = true
	}
	for k := range extra {
		skip[k] = true
	}
	var env []string
	for _, kv := range os.Environ() {
		if idx := strings.IndexByte(kv, '='); idx > 0 {
			if skip[kv[:idx]] {
				continue
			}
		}
		env = append(env, kv)
	}
	for k, v := range extra {
		env = append(env, k+"="+v)
	}
	return env
}

func RunHermesCLI(ctx context.Context, args []string, timeout time.Duration, extraEnv map[string]string) HermesResult {
	bin := viper.GetString("HERMES_BIN")
	if bin == "" {
		bin = "hermes"
	}

	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Env = buildEnv(extraEnv)

	start := time.Now()
	out, err := cmd.CombinedOutput()

	duration := time.Since(start).Milliseconds()

	result := HermesResult{
		Command:    bin,
		Args:       args,
		DurationMs: duration,
		Stdout:     string(out),
	}

	if exitErr, ok := err.(*exec.ExitError); ok {
		result.ExitCode = exitErr.ExitCode()
		result.Stderr = string(exitErr.Stderr)
		result.OK = result.ExitCode == 0
	} else if err != nil {
		result.Error = err.Error()
		result.ExitCode = -1
	} else {
		result.ExitCode = 0
		result.OK = true
	}

	return result
}

// --- Main ---

func main() {
	viper.SetDefault("PORT", "8643")
	viper.SetDefault("DB_PATH", "./itscwf_fabrica.db")
	viper.SetDefault("HERMES_BIN", "hermes")
	viper.SetDefault("TIMEOUT_SECONDS", 30)
	viper.SetDefault("CORS_ORIGINS", "*")
	viper.AutomaticEnv()

	port := viper.GetString("PORT")
	dbPath := viper.GetString("DB_PATH")
	corsOrigins := viper.GetString("CORS_ORIGINS")

	// Open database
	db, err := sql.Open("sqlite3", dbPath+"?_foreign_keys=on&_journal_mode=WAL")
	if err != nil {
		log.Fatalf("failed to open DB: %v", err)
	}
	defer db.Close()

	// Configure WAL mode for better concurrency
	if _, err := db.Exec("PRAGMA journal_mode=WAL"); err != nil {
		log.Printf("WAL pragma failed (non-fatal): %v", err)
	}

	// Run migrations
	if err := models.Migrate(db); err != nil {
		log.Fatalf("migration failed: %v", err)
	}

	// Set GORM DB
	models.SetDB(db)

	gin.SetMode(gin.ReleaseMode)
	r := gin.New()
	r.Use(gin.Recovery())
	r.Use(gin.Logger())

	// CORS
	config := cors.Config{
		AllowOrigins:     strings.Split(corsOrigins, ","),
		AllowMethods:     []string{"GET", "POST", "PUT", "PATCH", "DELETE", "OPTIONS"},
		AllowHeaders:     []string{"Authorization", "Content-Type", "X-API-Key"},
		AllowCredentials: true,
	}
	r.Use(cors.New(config))

	// Health
	r.GET("/health", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{
			"service": "itscwf-fabrica-new",
			"status":  "ok",
			"version": "1.0.0",
			"time":    time.Now().UTC().Format(time.RFC3339),
		})
	})

	// ===== Hermes Kanban routes (from hermes-kanban-api) =====
	hermes := r.Group("/api/v1/hermes")
	{
		hermes.GET("/agents", hermesAgentsHandler)
		hermes.GET("/crons", hermesCronsHandler)
		hermes.GET("/boards", hermesBoardsHandler)
		hermes.POST("/boards", hermesCreateBoardHandler)

		hermes.GET("/tasks", hermesListTasksHandler)
		hermes.POST("/tasks", hermesCreateTaskHandler)
		hermes.GET("/tasks/:id", hermesGetTaskHandler)
		hermes.PATCH("/tasks/:id", hermesUpdateTaskHandler)
		hermes.DELETE("/tasks/:id", hermesDeleteTaskHandler)

		hermes.POST("/tasks/:id/assign", hermesAssignTaskHandler)
		hermes.POST("/tasks/:id/start", hermesStartTaskHandler)
		hermes.POST("/tasks/:id/complete", hermesCompleteTaskHandler)
		hermes.POST("/tasks/:id/block", hermesBlockTaskHandler)
		hermes.POST("/tasks/:id/unblock", hermesUnblockTaskHandler)
		hermes.GET("/tasks/:id/runs", hermesTaskRunsHandler)
	}

	// ===== Fabrica routes (from itscwf-fabrica) =====
	authed := r.Group("/api/v1")
	authed.Use(handlers.AuthMiddleware())

	handlers.RegisterProjectsRoutes(authed, db)
	handlers.RegisterECARoutes(authed, db)
	handlers.RegisterQARoutes(authed, db)
	handlers.RegisterCronRoutes(authed, db)
	handlers.RegisterAgentRoutes(authed, db)
	handlers.RegisterProviderRoutes(authed, db)
	handlers.RegisterServerRoutes(authed, db)
	handlers.RegisterActivityLogRoutes(authed, db)
	handlers.RegisterBugRoutes(authed, db)
	handlers.RegisterHermesProxyRoutes(authed)

	log.Printf("itscwf-fabrica-new starting on :%s (DB: %s, Hermes CLI: %s)",
		port, dbPath, viper.GetString("HERMES_BIN"))

	if err := r.Run(":" + port); err != nil {
		log.Fatal(err)
	}
}

// --- Hermes handlers (merged from hermes-kanban-api) ---

func withTimeout(seconds int, fn func(*gin.Context)) gin.HandlerFunc {
	return func(c *gin.Context) {
		ctx, cancel := context.WithTimeout(c.Request.Context(), time.Duration(seconds)*time.Second)
		defer cancel()
		c.Request = c.Request.WithContext(ctx)
		fn(c)
	}
}

func respondHermes(c *gin.Context, result HermesResult) {
	c.Header("X-Command", "hermes "+strings.Join(result.Args, " "))
	c.Header("X-Duration-Ms", strconv.FormatInt(result.DurationMs, 10))

	if result.Error != "" && result.ExitCode == -1 {
		c.JSON(http.StatusGatewayTimeout, gin.H{
			"error": gin.H{"code": "timeout", "message": result.Error},
			"meta":  result,
		})
		return
	}
	if !result.OK {
		c.JSON(http.StatusBadGateway, gin.H{
			"error": gin.H{"code": "hermes_error", "message": strings.TrimSpace(result.Stderr)},
			"meta":  result,
		})
		return
	}

	var data any
	if err := json.Unmarshal([]byte(strings.TrimSpace(result.Stdout)), &data); err == nil {
		c.JSON(http.StatusOK, gin.H{"data": data, "meta": result})
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": result.Stdout, "meta": result})
}

func hermesAgentsHandler(c *gin.Context)    { respondHermes(c, RunHermesCLI(c.Request.Context(), []string{"status"}, 30*time.Second, nil)) }
func hermesCronsHandler(c *gin.Context)     { respondHermes(c, RunHermesCLI(c.Request.Context(), []string{"cron", "list"}, 30*time.Second, nil)) }
func hermesBoardsHandler(c *gin.Context)    { respondHermes(c, RunHermesCLI(c.Request.Context(), []string{"kanban", "boards", "list", "--json"}, 30*time.Second, nil)) }

func hermesCreateBoardHandler(c *gin.Context) {
	var body struct {
		Slug string `json:"slug" binding:"required"`
		Name string `json:"name"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	args := []string{"kanban", "boards", "create", body.Slug}
	if body.Name != "" {
		args = append(args, "--name", body.Name)
	}
	respondHermes(c, RunHermesCLI(c.Request.Context(), args, 30*time.Second, nil))
}

func hermesListTasksHandler(c *gin.Context) {
	args := []string{"kanban", "list", "--json"}
	extraEnv := map[string]string{}
	if q := c.Query("board"); q != "" {
		extraEnv["HERMES_KANBAN_BOARD"] = q
	}
	if q := c.Query("status"); q != "" {
		args = append(args, "--status", q)
	}
	if q := c.Query("assignee"); q != "" {
		args = append(args, "--assignee", q)
	}
	if q := c.Query("limit"); q != "" {
		args = append(args, "--limit", q)
	}
	respondHermes(c, RunHermesCLI(c.Request.Context(), args, 30*time.Second, extraEnv))
}

func hermesGetTaskHandler(c *gin.Context) {
	id := c.Param("id")
	args := []string{"kanban", "show", "--json", id}
	extraEnv := map[string]string{}
	if q := c.Query("board"); q != "" {
		extraEnv["HERMES_KANBAN_BOARD"] = q
	}
	respondHermes(c, RunHermesCLI(c.Request.Context(), args, 30*time.Second, extraEnv))
}

func hermesCreateTaskHandler(c *gin.Context) {
	var body struct {
		Title     string `json:"title" binding:"required"`
		Assignee  string `json:"assignee"`
		Priority  string `json:"priority"`
		Estimate  string `json:"estimate"`
		BlockedBy string `json:"blocked_by"`
		Board     string `json:"board"`
		Goal      bool   `json:"goal"`
		Body      string `json:"body"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	args := []string{"kanban", "create", body.Title, "--json"}
	extraEnv := map[string]string{}
	if body.Assignee != "" {
		args = append(args, "--assignee", body.Assignee)
	}
	if body.Priority != "" {
		args = append(args, "--priority", body.Priority)
	}
	if body.Estimate != "" {
		args = append(args, "--estimate", body.Estimate)
	}
	if body.Goal {
		args = append(args, "--goal")
	}
	if body.Body != "" {
		args = append(args, "--body", body.Body)
	}
	if body.Board != "" {
		extraEnv["HERMES_KANBAN_BOARD"] = body.Board
	}
	respondHermes(c, RunHermesCLI(c.Request.Context(), args, 30*time.Second, extraEnv))
}

func hermesUpdateTaskHandler(c *gin.Context) {
	id := c.Param("id")
	var body struct {
		Result  string `json:"result"`
		Summary string `json:"summary"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	args := []string{"kanban", "edit", id}
	if body.Result != "" {
		args = append(args, "--result", body.Result)
	}
	if body.Summary != "" {
		args = append(args, "--summary", body.Summary)
	}
	respondHermes(c, RunHermesCLI(c.Request.Context(), args, 30*time.Second, nil))
}

func hermesDeleteTaskHandler(c *gin.Context) {
	respondHermes(c, RunHermesCLI(c.Request.Context(), []string{"kanban", "archive", c.Param("id")}, 30*time.Second, nil))
}

func hermesAssignTaskHandler(c *gin.Context) {
	assignee := c.Query("assignee")
	if assignee == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "query param 'assignee' is required"})
		return
	}
	respondHermes(c, RunHermesCLI(c.Request.Context(), []string{"kanban", "assign", c.Param("id"), assignee}, 30*time.Second, nil))
}

func hermesStartTaskHandler(c *gin.Context) {
	respondHermes(c, RunHermesCLI(c.Request.Context(), []string{"kanban", "claim", c.Param("id")}, 30*time.Second, nil))
}

func hermesCompleteTaskHandler(c *gin.Context) {
	var body struct {
		Result  string `json:"result"`
		Summary string `json:"summary"`
	}
	c.ShouldBindJSON(&body)
	args := []string{"kanban", "complete", c.Param("id")}
	if body.Result != "" {
		args = append(args, "--result", body.Result)
	}
	if body.Summary != "" {
		args = append(args, "--summary", body.Summary)
	}
	respondHermes(c, RunHermesCLI(c.Request.Context(), args, 30*time.Second, nil))
}

func hermesBlockTaskHandler(c *gin.Context) {
	reason := c.Query("reason")
	args := []string{"kanban", "block", "--ids", c.Param("id")}
	if reason != "" {
		args = append(args, "--reason", reason)
	}
	respondHermes(c, RunHermesCLI(c.Request.Context(), args, 30*time.Second, nil))
}

func hermesUnblockTaskHandler(c *gin.Context) {
	respondHermes(c, RunHermesCLI(c.Request.Context(), []string{"kanban", "unblock", c.Param("id")}, 30*time.Second, nil))
}

func hermesTaskRunsHandler(c *gin.Context) {
	respondHermes(c, RunHermesCLI(c.Request.Context(), []string{"kanban", "runs", c.Param("id"), "--json"}, 30*time.Second, nil))
}
