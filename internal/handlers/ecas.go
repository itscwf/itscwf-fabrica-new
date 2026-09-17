package handlers

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/itscwf/itscwf-fabrica-new/internal/models"
	fab "github.com/itscwf/itscwf-fabrica-new/pkg/fabrica"
)

// ecaHandlers implementa o CRUD de ECAs.
//
// Diferente dos outros recursos, a ECA tem dados em tabelas filhas
// (eca_steps/eca_dependencies, criadas pela migracao T5). Por isso os handlers
// sao proprios: ao criar/atualizar aceitam "steps": [...] e
// "dependencies": ["ECA-007"] e mantem as tabelas filhas em sincronia.
type ecaHandlers struct {
	db     *gorm.DB
	rec    *Recorder
	kanban fab.KanbanService
}

// stringList aceita ["a","b"], "a, b" e "a\nb" — o frontend e os JSONs legados
// usam as tres formas.
type stringList []string

// UnmarshalJSON normaliza string unica, lista ou null.
func (l *stringList) UnmarshalJSON(raw []byte) error {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" || trimmed == "null" {
		*l = nil
		return nil
	}
	if strings.HasPrefix(trimmed, "[") {
		items := []string{}
		if err := json.Unmarshal(raw, &items); err != nil {
			return err
		}
		*l = items
		return nil
	}
	text := strings.Trim(trimmed, `"`)
	parts := strings.FieldsFunc(text, func(r rune) bool {
		return r == ',' || r == '\n' || r == ';'
	})
	items := make([]string, 0, len(parts))
	for _, part := range parts {
		if value := strings.TrimSpace(part); value != "" {
			items = append(items, value)
		}
	}
	*l = items
	return nil
}

// ecaPayload e o corpo aceito em POST/PUT /api/v1/ecas.
type ecaPayload struct {
	ECAID        string `json:"eca_id"`
	Title        string `json:"title"`
	Summary      string `json:"summary"`
	Status       string `json:"status"`
	Responsible  string `json:"responsible"`
	Repo         string `json:"repo"`
	ProjectID    int64  `json:"project_id"`
	HermesTaskID string `json:"hermes_task_id"`
	KanbanBoard  string `json:"kanban_board"`
	Notes        string `json:"notes"`
	QAImpact     string `json:"qa_impact"`
	Changelog    string `json:"changelog_entry"`
	DateCreated  string `json:"date_created"`

	Steps        stringList `json:"steps"`
	Dependencies stringList `json:"dependencies"`
}

// NextID returns the next available ECA ID in the format "ECA-NNN".
func (h *ecaHandlers) NextID(c *gin.Context) {
	var maxID string
	h.db.Raw("SELECT eca_id FROM eca_registry WHERE deleted_at IS NULL ORDER BY id DESC LIMIT 1").Scan(&maxID)
	next := 1
	if maxID != "" {
		// Extract number from "ECA-017" → 17
		parts := strings.Split(maxID, "-")
		if len(parts) == 2 {
			if n, err := strconv.Atoi(parts[1]); err == nil {
				next = n + 1
			}
		}
	}
	c.JSON(http.StatusOK, gin.H{"next_eca_id": fmt.Sprintf("ECA-%03d", next)})
}

// Register monta as rotas de ECA (lista, detalhe, criacao, atualizacao e remocao).
// remocao).
func (h *ecaHandlers) Register(rg *gin.RouterGroup) {
	rg.GET("/next-id", h.NextID)
	rg.GET("", h.List)
	rg.GET("/:id", h.Show)
	rg.POST("", h.Create)
	rg.PUT("/:id", h.Update)
	rg.PATCH("/:id", h.Update)
	rg.DELETE("/:id", h.Delete)
}

// List atende GET /api/v1/ecas.
func (h *ecaHandlers) List(c *gin.Context) {
	page := parsePage(c)
	query := h.db.Model(&models.ECA{}).Where("deleted_at IS NULL")
	if queryBool(c, "include_deleted", false) {
		query = h.db.Model(&models.ECA{})
	}
	if status := c.Query("status"); status != "" {
		query = query.Where("status = ?", status)
	}
	if repo := c.Query("repo"); repo != "" {
		query = query.Where("repo = ?", repo)
	}
	if projectID := queryInt(c, "project_id", 0); projectID > 0 {
		query = query.Where("project_id = ?", projectID)
	}
	if term := strings.TrimSpace(c.Query("q")); term != "" {
		like := "%" + strings.ToLower(term) + "%"
		query = query.Where("(LOWER(eca_id) LIKE ? OR LOWER(titulo) LIKE ? OR LOWER(responsavel) LIKE ?)", like, like, like)
	}
	if from, hasFrom := parseTimeParam(c.Query("from")); hasFrom {
		query = query.Where("created_at >= ?", models.NewTimestamp(from))
	}
	if to, hasTo := parseTimeParam(c.Query("to")); hasTo {
		query = query.Where("created_at <= ?", models.NewTimestamp(to))
	}

	var total int64
	if err := query.Count(&total).Error; err != nil {
		failDB(c, err)
		return
	}

	ecas := []models.ECA{}
	order := "eca_id asc"
	if sortParam := strings.TrimSpace(c.Query("sort")); sortParam != "" {
		switch strings.TrimPrefix(sortParam, "-") {
		case "eca_id", "status", "titulo", "id", "created_at", "updated_at", "repo", "responsavel":
			direction := "asc"
			if strings.HasPrefix(sortParam, "-") || strings.EqualFold(c.Query("order"), "desc") {
				direction = "desc"
			}
			order = strings.TrimPrefix(sortParam, "-") + " " + direction
		}
	}
	if err := query.Order(order).Limit(page.PerPage).Offset(page.Offset).Find(&ecas).Error; err != nil {
		failDB(c, err)
		return
	}
	if err := h.decorate(ecas); err != nil {
		failDB(c, err)
		return
	}
	listOK(c, ecas, total, page)
}

// Show atende GET /api/v1/ecas/:id (id numerico ou ECA-xxx).
func (h *ecaHandlers) Show(c *gin.Context) {
	eca, err := h.find(c)
	if err != nil {
		failDB(c, err)
		return
	}
	ok(c, http.StatusOK, eca)
}

// Create atende POST /api/v1/ecas.
func (h *ecaHandlers) Create(c *gin.Context) {
	payload := ecaPayload{}
	if err := c.ShouldBindJSON(&payload); err != nil {
		fail(c, http.StatusBadRequest, codeBadRequest, "corpo JSON invalido: "+err.Error())
		return
	}
	eca, err := h.apply(&payload, nil)
	if err != nil {
		failValidation(c, err)
		return
	}
	eca.TouchNew(models.Now())

	txErr := h.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(&eca).Error; err != nil {
			return err
		}
		if err := h.replaceChildren(tx, eca.ECAID, []string(payload.Steps), []string(payload.Dependencies)); err != nil {
			return err
		}
		return nil
	})
	if txErr != nil {
		failDB(c, txErr)
		return
	}

	// Breakdown de tasks em ECAs com status=planned é feito pelo cron
	// eca_breakdown.py — polling automático a cada hora.
	// Não cria tasks aqui para manter o fluxo síncrono e controlável.

	if err := h.decorateOne(&eca); err != nil {
		failDB(c, err)
		return
	}
	h.rec.Record(c, "create", "ecas/"+itoa(eca.ID), "", eca)
	ok(c, http.StatusCreated, eca)
}

// Update atende PUT/PATCH /api/v1/ecas/:id.
func (h *ecaHandlers) Update(c *gin.Context) {
	current, err := h.find(c)
	if err != nil {
		failDB(c, err)
		return
	}

	payload := ecaPayload{}
	raw := map[string]any{}
	if err := c.ShouldBindJSON(&raw); err != nil {
		fail(c, http.StatusBadRequest, codeBadRequest, "corpo JSON invalido: "+err.Error())
		return
	}
	encoded, err := jsonMarshal(raw)
	if err != nil {
		fail(c, http.StatusBadRequest, codeBadRequest, err.Error())
		return
	}
	if err := jsonUnmarshal(encoded, &payload); err != nil {
		fail(c, http.StatusBadRequest, codeBadRequest, "corpo JSON invalido: "+err.Error())
		return
	}
	for _, step := range payload.Steps {
		if strings.TrimSpace(step) == "" {
			fail(c, http.StatusUnprocessableEntity, codeValidation, "steps nao pode conter texto vazio")
			return
		}
	}

	updated, err := h.apply(&payload, &current)
	if err != nil {
		failValidation(c, err)
		return
	}
	updated.Touch(models.Now())

	if err := h.db.Omit(clause.Associations).Save(&updated).Error; err != nil {
		failDB(c, err)
		return
	}
	if _, hasSteps := raw["steps"]; hasSteps || raw["dependencies"] != nil {
		if err := h.db.Transaction(func(tx *gorm.DB) error {
			return h.replaceChildren(tx, updated.ECAID, []string(payload.Steps), []string(payload.Dependencies))
		}); err != nil {
			failDB(c, err)
			return
		}
	}
	if err := h.decorateOne(&updated); err != nil {
		failDB(c, err)
		return
	}
	h.rec.Record(c, "update", "ecas/"+itoa(updated.ID), "", updated)
	ok(c, http.StatusOK, updated)
}

// Delete faz soft delete da ECA (e mantem as tabelas filhas intactas, para
// rastreabilidade historica).
func (h *ecaHandlers) Delete(c *gin.Context) {
	current, err := h.find(c)
	if err != nil {
		failDB(c, err)
		return
	}
	now := models.Now()
	if err := h.db.Model(&models.ECA{}).Where("id = ?", current.ID).
		Updates(map[string]any{"deleted_at": now, "updated_at": now}).Error; err != nil {
		failDB(c, err)
		return
	}
	h.rec.Record(c, "delete", "ecas/"+itoa(current.ID), "", current)
	c.JSON(http.StatusOK, gin.H{"data": gin.H{"id": current.ID, "deleted": "soft"}})
}

// find localiza a ECA por id numerico ou por eca_id.
func (h *ecaHandlers) find(c *gin.Context) (models.ECA, error) {
	raw := c.Param("id")
	query := h.db.Model(&models.ECA{})
	if !queryBool(c, "include_deleted", false) {
		query = query.Where("deleted_at IS NULL")
	}
	var eca models.ECA
	if id, isNumeric := numericID(raw); isNumeric {
		if err := query.Where("id = ?", id).First(&eca).Error; err != nil {
			return eca, err
		}
	} else if err := query.Where("eca_id = ?", strings.ToUpper(raw)).First(&eca).Error; err != nil {
		return eca, err
	}
	return eca, nil
}

// apply valida o payload e devolve a entidade resultante.
func (h *ecaHandlers) apply(payload *ecaPayload, current *models.ECA) (models.ECA, error) {
	eca := models.ECA{}
	if current != nil {
		eca = *current
	}

	if payload.ECAID != "" {
		eca.ECAID = strings.ToUpper(strings.TrimSpace(payload.ECAID))
	}
	// Auto-generate ECA ID if creating new (current is nil) and no ID provided.
	if current == nil && eca.ECAID == "" {
		var maxID string
		h.db.Raw("SELECT eca_id FROM eca_registry WHERE deleted_at IS NULL ORDER BY id DESC LIMIT 1").Scan(&maxID)
		next := 1
		if maxID != "" {
			parts := strings.Split(maxID, "-")
			if len(parts) == 2 {
				if n, err := strconv.Atoi(parts[1]); err == nil {
					next = n + 1
				}
			}
		}
		eca.ECAID = fmt.Sprintf("ECA-%03d", next)
	}
	if payload.Title != "" {
		eca.Title = strings.TrimSpace(payload.Title)
	}
	if payload.Summary != "" {
		eca.Summary = payload.Summary
	}
	if payload.Status != "" {
		normalized, ok := normalizeECAStatus(payload.Status)
		if !ok {
			return eca, errors.New("status invalido (created|in_progress|blocked|done|cancelled|paused)")
		}
		eca.Status = normalized
	}
	if payload.Responsible != "" {
		eca.Resp = payload.Responsible
	}
	if payload.Repo != "" {
		eca.Repo = payload.Repo
	}
	if payload.ProjectID != 0 {
		eca.ProjectID = payload.ProjectID
	}
	if payload.HermesTaskID != "" {
		eca.HermesTaskID = payload.HermesTaskID
	}
	if payload.KanbanBoard != "" {
		eca.KanbanBoard = payload.KanbanBoard
	}
	if payload.Notes != "" {
		eca.Notes = payload.Notes
	}
	if payload.QAImpact != "" {
		eca.QAImpact = payload.QAImpact
	}
	if payload.Changelog != "" {
		eca.ChangelogNote = payload.Changelog
	}
	if payload.DateCreated != "" {
		eca.Created = payload.DateCreated
	}

	if err := requireText(map[string]string{eca.ECAID: "eca_id", eca.Title: "title"}); err != nil {
		return eca, err
	}
	if eca.Status == "" {
		eca.Status = "created"
	}
	if eca.Created == "" {
		eca.Created = models.Now().Time.Format("2006-01-02")
	}
	eca.Updated = models.Now().Time.Format(models.Layout)

	// project_id derivado do repo (slug do projeto) quando nao informado.
	if eca.ProjectID == 0 && eca.Repo != "" {
		var project models.Project
		if err := h.db.Select("id").Where("slug = ?", eca.Repo).First(&project).Error; err == nil {
			eca.ProjectID = project.ID
		}
	}
	// repo herdado do project_id quando nao informado.
	if eca.Repo == "" && eca.ProjectID != 0 {
		var project models.Project
		if err := h.db.Select("slug").Where("id = ?", eca.ProjectID).First(&project).Error; err == nil {
			eca.Repo = project.Slug
		}
	}
	if eca.ProjectID != 0 {
		var count int64
		if err := h.db.Model(&models.Project{}).Where("id = ?", eca.ProjectID).Count(&count).Error; err != nil {
			return eca, err
		}
		if count == 0 {
			return eca, errors.New("project_id informado nao existe")
		}
	}
	return eca, uniqueField(h.db, &models.ECA{}, "eca_id", eca.ECAID, eca.ID)
}

// replaceChildren regrava etapas e dependencias da ECA.
func (h *ecaHandlers) replaceChildren(tx *gorm.DB, ecaID string, steps, dependencies []string) error {
	if steps != nil {
		if err := tx.Where("eca_id = ?", ecaID).Delete(&models.ECAStep{}).Error; err != nil {
			return err
		}
		order := 0
		for _, text := range steps {
			for _, line := range strings.Split(text, "\n") {
				line = strings.TrimSpace(line)
				if line == "" {
					continue
				}
				order++
				step := models.ECAStep{ECAID: ecaID, Order: order, Text: line, CreatedAt: models.Now()}
				if err := tx.Create(&step).Error; err != nil {
					return err
				}
			}
		}
	}
	if dependencies != nil {
		if err := tx.Where("eca_id = ?", ecaID).Delete(&models.ECADependency{}).Error; err != nil {
			return err
		}
		for _, dep := range dependencies {
			dep = strings.ToUpper(strings.TrimSpace(dep))
			if dep == "" {
				continue
			}
			row := models.ECADependency{ECAID: ecaID, DependsOn: dep}
			if err := tx.Create(&row).Error; err != nil {
				return err
			}
		}
	}
	return nil
}

// decorate preenche steps/dependencias de uma lista de ECAs.
func (h *ecaHandlers) decorate(ecas []models.ECA) error {
	for index := range ecas {
		if err := h.decorateOne(&ecas[index]); err != nil {
			return err
		}
	}
	return nil
}

// decorateOne carrega etapas e dependencias de uma ECA e resolve o projeto.
func (h *ecaHandlers) decorateOne(eca *models.ECA) error {
	steps := []models.ECAStep{}
	if err := h.db.Where("eca_id = ?", eca.ECAID).Order("ordem asc").Find(&steps).Error; err != nil {
		return err
	}
	eca.Steps = make([]string, 0, len(steps))
	for _, step := range steps {
		eca.Steps = append(eca.Steps, step.Text)
	}

	deps := []models.ECADependency{}
	if err := h.db.Where("eca_id = ?", eca.ECAID).Find(&deps).Error; err != nil {
		return err
	}
	ids := make([]string, 0, len(deps))
	for _, dep := range deps {
		ids = append(ids, dep.DependsOn)
	}
	sort.Strings(ids)
	eca.Dependencies = ids
	eca.DependenciesText = strings.Join(ids, ", ")

	h.resolveProject(eca)
	return nil
}

// resolveProject preenche project_id a partir da coluna repo quando possivel.
//
// A migracao gravou em eca_registry.repo tanto slugs quanto URLs git
// ("git@github.com:itscwf/smallngo.git"), entao a associacao e feita por nome
// normalizado, em memoria (GET nao escreve no banco).
func (h *ecaHandlers) resolveProject(eca *models.ECA) {
	if eca.ProjectID != 0 || strings.TrimSpace(eca.Repo) == "" {
		return
	}
	target := normalizeRepoName(eca.Repo)
	if target == "" {
		return
	}

	projects := []models.Project{}
	if err := h.db.Select("id", "slug", "name").Find(&projects).Error; err != nil {
		return
	}
	for _, project := range projects {
		if normalizeRepoName(project.Slug) == target || normalizeRepoName(project.Name) == target {
			eca.ProjectID = project.ID
			return
		}
	}

	repos := []models.Repo{}
	if err := h.db.Select("project_id", "url", "name").Find(&repos).Error; err != nil {
		return
	}
	for _, repo := range repos {
		if normalizeRepoName(repo.URL) == target || normalizeRepoName(repo.Name) == target {
			eca.ProjectID = repo.ProjectID
			return
		}
	}
}

// normalizeRepoName reduz "git@github.com:itscwf/smallngo.git" e "small-ngo" a
// "smallngo", permitindo comparar URLs, slugs e nomes de diretorio.
func normalizeRepoName(raw string) string {
	value := strings.TrimSpace(raw)
	if value == "" {
		return ""
	}
	value = strings.TrimSuffix(value, "/")
	if index := strings.LastIndexAny(value, "/:"); index >= 0 {
		value = value[index+1:]
	}
	value = strings.TrimSuffix(value, ".git")
	value = strings.ToLower(value)
	buf := strings.Builder{}
	for _, r := range value {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			buf.WriteRune(r)
		}
	}
	return buf.String()
}
