package handlers

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"github.com/itscwf/itscwf-fabrica-new/internal/middleware"
	"github.com/itscwf/itscwf-fabrica-new/internal/models"
)

// Codigos de erro devolvidos em {"error":{"code":...}}.
const (
	codeBadRequest   = "bad_request"
	codeValidation   = "validation_error"
	codeNotFound     = "not_found"
	codeConflict     = "conflict"
	codeUnauthorized = "unauthorized"
	codeForbidden    = "forbidden"
	codeInternal     = "internal_error"
	codeUpstream     = "hermes_unavailable"
)

// ok escreve {"data": ...}.
func ok(c *gin.Context, status int, data any) {
	c.JSON(status, gin.H{"data": data})
}

// listOK escreve a lista paginada.
func listOK(c *gin.Context, data any, total int64, p pageParams) {
	pages := 0
	if p.PerPage > 0 {
		pages = int((total + int64(p.PerPage) - 1) / int64(p.PerPage))
	}
	c.JSON(http.StatusOK, gin.H{
		"data":        data,
		"items":       data,
		"page":        p.Page,
		"per_page":    p.PerPage,
		"total":       total,
		"count":       total,
		"total_pages": pages,
	})
}

// fail escreve um erro estruturado.
func fail(c *gin.Context, status int, code, message string) {
	c.JSON(status, gin.H{"error": gin.H{"code": code, "message": message}})
}

// failDB converte um erro de banco em status HTTP.
func failDB(c *gin.Context, err error) {
	switch {
	case err == nil:
		return
	case errors.Is(err, gorm.ErrRecordNotFound):
		fail(c, http.StatusNotFound, codeNotFound, "registro nao encontrado")
	case isUniqueViolation(err):
		fail(c, http.StatusConflict, codeConflict, "registro ja existe")
	case isConstraintViolation(err):
		// FK/NOT NULL/CHECK: o payload aponta para uma linha inexistente ou
		// omite um campo obrigatorio do schema, o que e erro de validacao
		// (422) — nao um conflito de unicidade.
		slog.Warn("db_constraint_violation", "error", err, "path", c.Request.URL.Path)
		fail(c, http.StatusUnprocessableEntity, codeValidation, constraintMessage(err))
	default:
		slog.Error("api_error", "error", err, "path", c.Request.URL.Path)
		fail(c, http.StatusInternalServerError, codeInternal, "erro interno")
	}
}

// failValidation converte erro de validador: 409 para conflitos, 422 no resto.
func failValidation(c *gin.Context, err error) {
	var conflict conflictError
	if errors.As(err, &conflict) {
		fail(c, http.StatusConflict, codeConflict, err.Error())
		return
	}
	fail(c, http.StatusUnprocessableEntity, codeValidation, err.Error())
}

// isUniqueViolation reconhece violacoes de UNIQUE do SQLite (conflito real).
func isUniqueViolation(err error) bool {
	if err == nil {
		return false
	}
	message := err.Error()
	return strings.Contains(message, "UNIQUE constraint failed") ||
		strings.Contains(message, "duplicate key")
}

// isConstraintViolation reconhece as demais violacoes de integridade: FK
// (vinculo inexistente), NOT NULL (campo obrigatorio ausente) e CHECK (valor
// fora do dominio).
func isConstraintViolation(err error) bool {
	if err == nil {
		return false
	}
	message := err.Error()
	return strings.Contains(message, "FOREIGN KEY constraint failed") ||
		strings.Contains(message, "NOT NULL constraint failed") ||
		strings.Contains(message, "CHECK constraint failed")
}

// constraintMessage traduz o erro do SQLite numa mensagem util ao cliente.
func constraintMessage(err error) string {
	message := err.Error()
	switch {
	case strings.Contains(message, "FOREIGN KEY constraint failed"):
		return "referencia inexistente: o registro aponta para uma linha que nao existe"
	case strings.Contains(message, "NOT NULL constraint failed"):
		return "campo obrigatorio ausente: " + constraintColumn(message)
	case strings.Contains(message, "CHECK constraint failed"):
		return "valor invalido: " + constraintColumn(message)
	default:
		return message
	}
}

// constraintColumn extrai "<tabela>.<coluna>" da mensagem do SQLite.
func constraintColumn(message string) string {
	if index := strings.LastIndex(message, "constraint failed:"); index >= 0 {
		return strings.TrimSpace(message[index+len("constraint failed:"):])
	}
	return strings.TrimSpace(message)
}

// pageParams e o estado de paginacao.
type pageParams struct {
	Page    int
	PerPage int
	Offset  int
}

const (
	defaultPerPage = 50
	maxPerPage     = 500
)

func parsePage(c *gin.Context) pageParams {
	page := queryInt(c, "page", 1)
	if page < 1 {
		page = 1
	}
	perPage := queryInt(c, "per_page", defaultPerPage)
	if perPage < 1 {
		perPage = defaultPerPage
	}
	if perPage > maxPerPage {
		perPage = maxPerPage
	}
	return pageParams{Page: page, PerPage: perPage, Offset: (page - 1) * perPage}
}

func queryInt(c *gin.Context, key string, fallback int) int {
	raw := strings.TrimSpace(c.Query(key))
	if raw == "" {
		return fallback
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value <= 0 {
		return fallback
	}
	return value
}

func queryBool(c *gin.Context, key string, fallback bool) bool {
	switch strings.ToLower(strings.TrimSpace(c.Query(key))) {
	case "":
		return fallback
	case "1", "true", "yes", "on":
		return true
	case "0", "false", "no", "off":
		return false
	default:
		return fallback
	}
}

// parseTimeParam aceita RFC3339, "2006-01-02" e "2006-01-02 15:04:05".
func parseTimeParam(raw string) (time.Time, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return time.Time{}, false
	}
	layouts := []string{time.RFC3339Nano, time.RFC3339, models.Layout, "2006-01-02T15:04:05", "2006-01-02 15:04:05", "2006-01-02"}
	for _, layout := range layouts {
		if parsed, err := time.Parse(layout, raw); err == nil {
			return parsed.UTC(), true
		}
	}
	return time.Time{}, false
}

// Recorder grava a trilha de auditoria em activity_log.
type Recorder struct {
	DB *gorm.DB
}

// NewRecorder constroi o recorder de auditoria.
func NewRecorder(db *gorm.DB) *Recorder { return &Recorder{DB: db} }

// Record registra uma acao do usuario autenticado.
func (r *Recorder) Record(c *gin.Context, action, target, details string, payload any) {
	r.RecordAudit(c, middleware.Subject(c), action, target, payload, details)
}

// RecordAudit registra uma acao com ator explicito (necessario no login, onde
// as claims ainda nao estao no contexto).
func (r *Recorder) RecordAudit(c *gin.Context, actor, action, target string, payload any, extra ...string) {
	if r == nil || r.DB == nil {
		return
	}
	if strings.TrimSpace(actor) == "" {
		actor = middleware.Subject(c)
	}
	if strings.TrimSpace(actor) == "" {
		actor = "anonymous"
	}

	message := ""
	if payload != nil {
		if raw, err := json.Marshal(payload); err == nil {
			message = string(raw)
		}
	}
	if message == "" && len(extra) > 0 {
		message = extra[0]
	}
	message = truncate(message, 4000)

	projectID, projectSlug := r.resolveProject(target)
	entry := models.ActivityLog{
		Timestamp:   models.Now(),
		Kind:        action,
		Actor:       actor,
		Ref:         target,
		Message:     message,
		ProjectID:   projectID,
		ProjectSlug: projectSlug,
	}
	entry.TouchNew(models.Now())
	if err := r.DB.Create(&entry).Error; err != nil {
		// A tabela tem UNIQUE(ts, kind, ref, message): repeticao e ignorada.
		slog.Debug("activity_log_skipped", "error", err, "action", action, "target", target)
	}
}

// resolveProject descobre o projeto dono do alvo ("qa/cycles/12") para o
// dashboard agrupar atividade por projeto.
func (r *Recorder) resolveProject(target string) (int64, string) {
	parts := strings.Split(strings.Trim(target, "/"), "/")
	if len(parts) < 2 {
		return 0, ""
	}
	id, err := strconv.ParseUint(parts[len(parts)-1], 10, 64)
	if err != nil {
		return 0, ""
	}
	resource := strings.Join(parts[:len(parts)-1], "/")

	var project models.Project
	switch resource {
	case "projects":
		if err := r.DB.Select("id", "slug").Where("id = ?", id).First(&project).Error; err == nil {
			return project.ID, project.Slug
		}
	case "repos":
		var repo models.Repo
		if err := r.DB.Select("project_id").Where("id = ?", id).First(&repo).Error; err == nil {
			return r.projectByID(repo.ProjectID)
		}
	case "worktrees":
		var worktree models.Worktree
		if err := r.DB.Select("project_id").Where("id = ?", id).First(&worktree).Error; err == nil {
			return r.projectByID(worktree.ProjectID)
		}
	case "qa/cycles":
		var cycle models.QACycle
		if err := r.DB.Select("project_id", "project_slug").Where("id = ?", id).First(&cycle).Error; err == nil {
			return int64(cycle.ProjectID), cycle.ProjectSlug
		}
	case "qa/testcases":
		var tc models.QATestCase
		if err := r.DB.Select("cycle_id").Where("id = ?", id).First(&tc).Error; err == nil {
			var cycle models.QACycle
			if err := r.DB.Select("project_id", "project_slug").Where("id = ?", tc.CycleID).First(&cycle).Error; err == nil {
				return int64(cycle.ProjectID), cycle.ProjectSlug
			}
		}
	case "qa/executions":
		var exec models.QAExecution
		if err := r.DB.Select("cycle_id").Where("id = ?", id).First(&exec).Error; err == nil {
			var cycle models.QACycle
			if err := r.DB.Select("project_id", "project_slug").Where("id = ?", exec.CycleID).First(&cycle).Error; err == nil {
				return int64(cycle.ProjectID), cycle.ProjectSlug
			}
		}
	case "cron/jobs":
		var job models.CronJob
		if err := r.DB.Select("project_slug").Where("id = ?", id).First(&job).Error; err == nil && job.ProjectSlug != "" {
			if projectID, slug, ok := r.projectBySlug(job.ProjectSlug); ok {
				return projectID, slug
			}
			return 0, job.ProjectSlug
		}
	}
	return 0, ""
}

func (r *Recorder) projectByID(projectID int64) (int64, string) {
	if projectID == 0 {
		return 0, ""
	}
	var project models.Project
	if err := r.DB.Select("id", "slug").Where("id = ?", projectID).First(&project).Error; err != nil {
		return projectID, ""
	}
	return project.ID, project.Slug
}

func (r *Recorder) projectBySlug(slug string) (int64, string, bool) {
	var project models.Project
	if slug == "" {
		return 0, "", false
	}
	if err := r.DB.Select("id", "slug").Where("slug = ?", slug).First(&project).Error; err != nil {
		return 0, slug, false
	}
	return project.ID, project.Slug, true
}

// truncate limita o tamanho de um texto gravado no log.
func truncate(raw string, limit int) string {
	if limit <= 0 || len(raw) <= limit {
		return raw
	}
	return raw[:limit] + "...(truncado)"
}

// jsonMarshal/jsonUnmarshal isolam o encoding/json dos handlers.
func jsonMarshal(value any) ([]byte, error) { return json.Marshal(value) }

func jsonUnmarshal(raw []byte, target any) error { return json.Unmarshal(raw, target) }

// normalizeECAStatus traduz status livres para os valores aceitos pelo CHECK
// de eca_registry (created|in_progress|blocked|done|cancelled|paused).
func normalizeECAStatus(raw string) (string, bool) {
	normalized := strings.ToLower(strings.TrimSpace(raw))
	normalized = strings.ReplaceAll(normalized, " ", "_")
	normalized = strings.ReplaceAll(normalized, "-", "_")
	switch normalized {
	case "created", "nova", "new", "pending", "open", "planejada":
		return "created", true
	case "in_progress", "em_andamento", "active", "ativa", "doing", "wip":
		return "in_progress", true
	case "blocked", "bloqueada", "bloqueado":
		return "blocked", true
	case "done", "concluida", "concluido", "complete", "completed", "finished":
		return "done", true
	case "cancelled", "canceled", "cancelada", "aborted":
		return "cancelled", true
	case "paused", "pausada", "on_hold":
		return "paused", true
	default:
		return normalized, false
	}
}

// requireText e reexportado por resources.go; mantido aqui para os handlers
// especificos (ecas/qa) usarem a mesma mensagem.
func requireTextErr(label string) error { return fmt.Errorf("%s e obrigatorio", label) }
