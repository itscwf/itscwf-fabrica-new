package handlers

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/itscwf/itscwf-fabrica-new/pkg/fabrica"
)

// hermesHandlers implements GET /api/v1/hermes/* via a KanbanService.
// This allows the Fabrica backend to run in Docker while Hermes lives on the host,
// bridged by the hermes-kanban-api REST server (localhost:8650).
type hermesHandlers struct {
	kanban       fabrica.KanbanService
	legacyClient *fabrica.HermesClient // agents + cron (local-only)
	cache        *fabrica.TTLCache
	defaultBoard string
}

func newHermesHandlers(kanban fabrica.KanbanService, legacyClient *fabrica.HermesClient, ttl time.Duration, defaultBoard string) *hermesHandlers {
	return &hermesHandlers{
		kanban:       kanban,
		legacyClient: legacyClient,
		cache:        fabrica.NewTTLCache(ttl),
		defaultBoard: defaultBoard,
	}
}

// agentsWithFallback tries `hermes status --json` via the legacy client
// (JSON-first with plain-text fallback) and uses the kanban bridge otherwise.
func (h *hermesHandlers) agentsWithFallback(ctx context.Context) (map[string]any, string, fabrica.CommandResult, bool, error) {
	if h.legacyClient != nil {
		return h.legacyClient.Agents(ctx)
	}
	return h.kanban.Agents(ctx)
}

// cronJobs prefers the local jobs.json registry (rich data: name, schedule,
// enabled, provider/model) and falls back to the kanban bridge TUI parser.
// The jobs.json file lives on the Hermes host only; in Docker containers the
// stat fails at startup and the bridge is the real source, so a missing file
// must NOT be a hard error — it silently defers to the bridge instead.
func (h *hermesHandlers) cronJobs() ([]map[string]any, string, error) {
	if h.legacyClient != nil {
		jobs, source, err := h.legacyClient.CronJobs()
		if err == nil {
			return jobs, source, nil
		}
		if h.kanban == nil {
			return nil, source, err
		}
		if _, isMissing := err.(*os.PathError); !isMissing && !strings.Contains(err.Error(), "no such file") {
			return nil, source, err
		}
	}
	if h.kanban == nil {
		return nil, "", fmt.Errorf("kanban service not configured")
	}
	return h.kanban.CronJobs()
}

// Tasks returns board tasks (bridged to hermes-kanban-api or CLI).
func (h *hermesHandlers) Tasks(c *gin.Context) {
	board := firstNonEmpty(c.Query("board"), h.defaultBoard)
	status := strings.TrimSpace(c.Query("status"))
	assignee := strings.TrimSpace(c.Query("assignee"))
	key := fmt.Sprintf("tasks|%s|%s|%s", board, status, assignee)

	if !queryBool(c, "refresh", false) {
		if cached, found := h.cache.Get(key); found {
			if prev, ok := cached.(*tasksResponse); ok {
				hit := *prev
				now := time.Now().UTC()
				hit.Cached = true
				hit.CachedAt = &now
				c.JSON(http.StatusOK, &hit)
				return
			}
		}
	}

	tasks, err := h.kanban.KanbanTasks(c.Request.Context(), board, status, assignee)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{
			"error": gin.H{"code": "hermes_unavailable", "message": err.Error()},
			"board": board,
			"command": fabrica.CommandResult{
				Command:  "hermes",
				Args:     []string{"kanban", "list", "--json"},
				ExitCode: 1,
				Stderr:   err.Error(),
			},
		})
		return
	}

	resp := &tasksResponse{
		Data:        tasks,
		Count:       len(tasks),
		Board:       board,
		Status:      status,
		Assignee:    assignee,
		ByStatus:    countByStatus(tasks),
		Command:     fabrica.CommandResult{Command: "hermes", Args: []string{"kanban", "list", "--json"}, ExitCode: 0},
		GeneratedAt: time.Now().UTC(),
	}
	if cached, found := h.cache.Get(key + "|meta"); found {
		if meta, ok := cached.(time.Time); ok {
			resp.Cached = true
			resp.CachedAt = &meta
		}
	}
	h.cache.Set(key, resp)
	h.cache.Set(key+"|meta", time.Now().UTC())
	c.JSON(http.StatusOK, resp)
}

// Boards returns all kanban boards.
func (h *hermesHandlers) Boards(c *gin.Context) {
	boards, err := h.kanban.KanbanBoards(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{
			"error": gin.H{"code": "hermes_unavailable", "message": err.Error()},
		})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"data":  boards,
		"count": len(boards),
	})
}

// Task returns a single task by ID.
func (h *hermesHandlers) Task(c *gin.Context) {
	taskID := c.Param("id")
	board := firstNonEmpty(c.Query("board"), h.defaultBoard)
	task, err := h.kanban.KanbanTask(c.Request.Context(), taskID, board)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{
			"error": gin.H{"code": "hermes_unavailable", "message": err.Error()},
		})
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": task})
}

// TaskRuns returns run history for a task.
func (h *hermesHandlers) TaskRuns(c *gin.Context) {
	taskID := c.Param("id")
	runs, err := h.kanban.KanbanRuns(c.Request.Context(), taskID)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{
			"error": gin.H{"code": "hermes_unavailable", "message": err.Error()},
		})
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": runs, "count": len(runs)})
}

// Agents returns Hermes agent status via KanbanService, falling back to the
// legacy HermesClient (plain `hermes status` with --json retry semantics).
func (h *hermesHandlers) Agents(c *gin.Context) {
	if h.kanban == nil && h.legacyClient == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{
			"error": gin.H{"code": "unavailable", "message": "kanban service not configured"},
		})
		return
	}
	key := "agents|status"
	if !queryBool(c, "refresh", false) {
		if cached, found := h.cache.Get(key); found {
			if prev, ok := cached.(*agentsResponse); ok {
				c.JSON(http.StatusOK, prev)
				return
			}
		}
	}

	parsed, raw, result, fallback, err := h.agentsWithFallback(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{
			"error":   gin.H{"code": "hermes_unavailable", "message": err.Error()},
			"command": result,
		})
		return
	}

	resp := &agentsResponse{
		Data:         parsed,
		Raw:          raw,
		Parsed:       parsed != nil,
		FallbackUsed: fallback,
		Summary:      parseStatusText(raw),
		Command:      result,
		GeneratedAt:  time.Now().UTC(),
	}
	h.cache.Set(key, resp)
	c.JSON(http.StatusOK, resp)
}

// Crons returns Hermes cron job registry (local only).
func (h *hermesHandlers) Crons(c *gin.Context) {
	if h.kanban == nil && h.legacyClient == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{
			"error": gin.H{"code": "unavailable", "message": "kanban service not configured"},
		})
		return
	}
	jobs, source, err := h.cronJobs()
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{
			"error":  gin.H{"code": "hermes_unavailable", "message": err.Error()},
			"source": source,
		})
		return
	}

	filtered := filterJobs(jobs, c)
	sort.SliceStable(filtered, func(i, j int) bool {
		return asString(filtered[i]["name"]) < asString(filtered[j]["name"])
	})
	total := len(filtered)
	limit := queryInt(c, "limit", 0)
	if limit > 0 && len(filtered) > limit {
		filtered = filtered[:limit]
	}

	enabledCount, failingCount := 0, 0
	for _, job := range jobs {
		if asBool(job["enabled"]) {
			enabledCount++
		}
		lastStatus := strings.ToLower(strings.TrimSpace(asString(job["last_status"])))
		if lastStatus == "error" || lastStatus == "failed" || lastStatus == "fail" {
			failingCount++
		}
	}

	c.JSON(http.StatusOK, gin.H{
		"data":          filtered,
		"count":         len(filtered),
		"total":         total,
		"enabled_count": enabledCount,
		"failing_count": failingCount,
		"source":        source,
		"generated_at":  time.Now().UTC(),
	})
}

// CreateTask creates a new kanban task from a bug report.
func (h *hermesHandlers) CreateTask(c *gin.Context) {
	var req struct {
		Title     string `json:"title" binding:"required"`
		Body      string `json:"body"`
		Severity  string `json:"severity"`
		URL       string `json:"url"`
		Steps     string `json:"steps"`
		Expected  string `json:"expected"`
		Actual    string `json:"actual"`
		Tags      string `json:"tags"`
		Assignee  string `json:"assignee"`
		Project   string `json:"project"`
		Workspace string `json:"workspace"`
	}

	if err := c.ShouldBindJSON(&req); err != nil {
		failValidation(c, err)
		return
	}

	if req.Assignee == "" {
		req.Assignee = "coder"
	}
	if req.Project == "" {
		req.Project = "itscwf-fabrica"
	}
	if req.Workspace == "" {
		req.Workspace = "worktree:itscwf-fabrica"
	}

	task, err := h.kanban.CreateTask(c.Request.Context(), fabrica.KanbanCreateTaskRequest{
		Title:     req.Title,
		Body:      req.Body,
		Assignee:  req.Assignee,
		Project:   req.Project,
		Workspace: req.Workspace,
	})
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{
			"error": gin.H{"code": "hermes_unavailable", "message": err.Error()},
		})
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": task})
}

// --- Response types ---

type tasksResponse struct {
	Data        []map[string]any      `json:"data"`
	Count       int                   `json:"count"`
	Board       string                `json:"board"`
	Status      string                `json:"status,omitempty"`
	Assignee    string                `json:"assignee,omitempty"`
	ByStatus    map[string]int        `json:"by_status"`
	Command     fabrica.CommandResult `json:"command"`
	GeneratedAt time.Time             `json:"generated_at"`
	Cached      bool                  `json:"cached"`
	CachedAt    *time.Time            `json:"cached_at,omitempty"`
}

type agentsResponse struct {
	Data         map[string]any        `json:"data"`
	Raw          string                `json:"raw,omitempty"`
	Parsed       bool                  `json:"parsed"`
	FallbackUsed bool                  `json:"fallback_used"`
	Summary      map[string]string     `json:"summary"`
	Command      fabrica.CommandResult `json:"command"`
	GeneratedAt  time.Time             `json:"generated_at"`
}

// --- Helpers ---

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

func countByStatus(tasks []map[string]any) map[string]int {
	counts := map[string]int{}
	for _, task := range tasks {
		counts[strings.ToLower(asString(task["status"]))]++
	}
	return counts
}

func filterJobs(jobs []map[string]any, c *gin.Context) []map[string]any {
	enabled := strings.TrimSpace(c.Query("enabled"))
	status := strings.TrimSpace(c.Query("status"))
	term := strings.ToLower(strings.TrimSpace(c.Query("q")))

	filtered := make([]map[string]any, 0, len(jobs))
	for _, job := range jobs {
		if enabled != "" && strconv.FormatBool(asBool(job["enabled"])) != enabled {
			continue
		}
		if status != "" && strings.ToLower(asString(job["last_status"])) != strings.ToLower(status) {
			continue
		}
		if term != "" && !strings.Contains(strings.ToLower(asString(job["name"])), term) {
			continue
		}
		filtered = append(filtered, job)
	}
	return filtered
}

func parseStatusText(raw string) map[string]string {
	summary := map[string]string{}
	for _, line := range strings.Split(raw, "\n") {
		parts := strings.SplitN(strings.TrimSpace(line), ":", 2)
		if len(parts) != 2 {
			continue
		}
		key := strings.ToLower(strings.TrimSpace(parts[0]))
		if _, known := map[string]bool{"model": true, "provider": true, "profile": true, "project": true}[key]; known && summary[key] == "" {
			summary[key] = strings.TrimSpace(parts[1])
		}
	}
	return summary
}

func asString(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		return t
	case bool:
		return strconv.FormatBool(t)
	case float64:
		return strconv.FormatFloat(t, 'f', -1, 64)
	default:
		return fmt.Sprintf("%v", t)
	}
}

func asBool(v any) bool {
	if v == nil {
		return false
	}
	switch t := v.(type) {
	case bool:
		return t
	case string:
		return t == "true" || t == "1"
	case float64:
		return t != 0
	default:
		return false
	}
}
