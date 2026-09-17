package handlers

import (
	"errors"
	"io"
	"net/http"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/itscwf/itscwf-fabrica-new/internal/models"
)

// qaHandlers implementa as acoes de QA que nao sao CRUD puro.
type qaHandlers struct {
	db  *gorm.DB
	rec *Recorder
}

type closeCycleRequest struct {
	Status     string            `json:"status"`
	Build      string            `json:"build"`
	NextTC     string            `json:"next_tc"`
	Notes      string            `json:"notes"`
	FinishedAt *models.Timestamp `json:"finished_at"`
}

// CloseCycle fecha um ciclo de QA: POST /api/v1/qa/cycles/:id/close.
//
// O payload e opcional. Sem status explicito, o resultado e derivado dos casos
// de teste (todos PASS -> concluido, algum pending/running -> pronto, resto ->
// encerrado) e os contadores denormalizados (total_tcs/passou/falhou/
// na_aplicavel) sao recalculados a partir de qa_testcases.
func (q *qaHandlers) CloseCycle(c *gin.Context) {
	id, isNumeric := numericID(c.Param("id"))
	if !isNumeric {
		fail(c, http.StatusBadRequest, codeBadRequest, "id do ciclo deve ser numerico")
		return
	}

	var cycle models.QACycle
	if err := q.db.First(&cycle, id).Error; err != nil {
		failDB(c, err)
		return
	}

	req := closeCycleRequest{}
	if c.Request != nil && c.Request.Body != nil {
		if err := c.ShouldBindJSON(&req); err != nil && !errors.Is(err, io.EOF) {
			fail(c, http.StatusBadRequest, codeBadRequest, "corpo JSON invalido: "+err.Error())
			return
		}
	}

	if req.Build != "" {
		cycle.Build = req.Build
	}
	if req.NextTC != "" {
		cycle.NextTC = req.NextTC
	}
	if req.Notes != "" {
		cycle.Notes = req.Notes
	}

	counters, derived := cycleCounters(q.db, cycle.ID)
	cycle.TotalTCs = counters.total
	cycle.Passed = counters.passed
	cycle.Failed = counters.failed
	cycle.NotApplicable = counters.notApplicable

	if req.Status != "" {
		normalized, ok := normalizeCycleStatus(req.Status)
		if !ok {
			fail(c, http.StatusUnprocessableEntity, codeValidation,
				"status "+req.Status+" invalido (em_andamento|pronto|concluido|encerrado|bloqueado|cancelado)")
			return
		}
		cycle.Status = normalized
	} else if cycle.Status == "" || cycle.Status == "em_andamento" {
		cycle.Status = derived
	}

	finished := models.Now()
	if req.FinishedAt != nil && !req.FinishedAt.IsZero() {
		finished = models.NewTimestamp(req.FinishedAt.Time)
	}
	cycle.FinishedAt = &finished
	if cycle.StartedAt == nil {
		cycle.StartedAt = &finished
	}
	cycle.Touch(models.Now())

	if err := q.db.Omit(clause.Associations).Save(&cycle).Error; err != nil {
		failDB(c, err)
		return
	}
	q.rec.Record(c, "close_cycle", "qa/cycles/"+itoa(cycle.ID), "", gin.H{
		"status": cycle.Status, "build": cycle.Build, "total": cycle.TotalTCs,
		"passed": cycle.Passed, "failed": cycle.Failed,
	})
	ok(c, http.StatusOK, cycle)
}

// ReopenCycle devolve um ciclo fechado para em_andamento.
func (q *qaHandlers) ReopenCycle(c *gin.Context) {
	id, isNumeric := numericID(c.Param("id"))
	if !isNumeric {
		fail(c, http.StatusBadRequest, codeBadRequest, "id do ciclo deve ser numerico")
		return
	}
	var cycle models.QACycle
	if err := q.db.First(&cycle, id).Error; err != nil {
		failDB(c, err)
		return
	}
	cycle.Status = "em_andamento"
	cycle.FinishedAt = nil
	cycle.Touch(models.Now())
	if err := q.db.Omit(clause.Associations).Save(&cycle).Error; err != nil {
		failDB(c, err)
		return
	}
	q.rec.Record(c, "reopen_cycle", "qa/cycles/"+itoa(cycle.ID), "", gin.H{"status": cycle.Status})
	ok(c, http.StatusOK, cycle)
}

type qaCounters struct {
	total         int
	passed        int
	failed        int
	notApplicable int
	pending       int
}

// cycleCounters recalcula os contadores do ciclo a partir dos casos de teste.
func cycleCounters(db *gorm.DB, cycleID int64) (qaCounters, string) {
	var rows []struct {
		Status string
		Total  int
	}
	counters := qaCounters{}
	if err := db.Model(&models.QATestCase{}).
		Select("status, count(*) as total").
		Where("cycle_id = ?", cycleID).
		Group("status").Scan(&rows).Error; err != nil {
		return counters, "encerrado"
	}
	for _, row := range rows {
		counters.total += row.Total
		switch normalizeTestCaseStatus(row.Status) {
		case "PASS":
			counters.passed += row.Total
		case "FAIL":
			counters.failed += row.Total
		case "N/A", "SKIP":
			counters.notApplicable += row.Total
		default:
			counters.pending += row.Total
		}
	}
	switch {
	case counters.total == 0:
		return counters, "encerrado"
	case counters.pending > 0:
		return counters, "pronto"
	case counters.passed == counters.total:
		return counters, "concluido"
	default:
		return counters, "encerrado"
	}
}

// cronHandlers implementa as acoes de cron que nao sao CRUD puro.
type cronHandlers struct {
	db  *gorm.DB
	rec *Recorder
}

// Toggle inverte a flag enabled: POST /api/v1/cron/jobs/:id/toggle.
func (h *cronHandlers) Toggle(c *gin.Context) {
	id, isNumeric := numericID(c.Param("id"))
	if !isNumeric {
		fail(c, http.StatusBadRequest, codeBadRequest, "id do job deve ser numerico")
		return
	}
	var job models.CronJob
	if err := h.db.First(&job, id).Error; err != nil {
		failDB(c, err)
		return
	}
	job.Enabled = !job.Enabled
	if job.Enabled {
		job.State = "scheduled"
	} else {
		job.State = "paused"
	}
	job.Touch(models.Now())
	if err := h.db.Omit(clause.Associations).Save(&job).Error; err != nil {
		failDB(c, err)
		return
	}
	action := "disable_cron"
	if job.Enabled {
		action = "enable_cron"
	}
	h.rec.Record(c, action, "cron/jobs/"+itoa(job.ID), "", gin.H{"enabled": job.Enabled})
	ok(c, http.StatusOK, job)
}

// GetTestCaseExecutions retorna o historico de execucoes de um TC:
// GET /api/v1/qa/testcases/:id/executions
func (q *qaHandlers) GetTestCaseExecutions(c *gin.Context) {
	id, isNumeric := numericID(c.Param("id"))
	if !isNumeric {
		fail(c, http.StatusBadRequest, codeBadRequest, "id do caso de teste deve ser numerico")
		return
	}

	var tc models.QATestCase
	if err := q.db.Where("id = ?", id).First(&tc).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			fail(c, http.StatusNotFound, codeNotFound, "caso de teste nao encontrado")
			return
		}
		failDB(c, err)
		return
	}

	var executions []models.QAExecution
	if err := q.db.Where("testcase_id = ?", id).Order("data desc").Find(&executions).Error; err != nil {
		failDB(c, err)
		return
	}

	ok(c, http.StatusOK, gin.H{
		"testcase": tc,
		"executions": executions,
		"total": len(executions),
	})
}

// GetProjectTestCases retorna todos os TCs de um projeto (por project_slug):
// GET /api/v1/projects/:id/testcases
func (q *qaHandlers) GetProjectTestCases(c *gin.Context) {
	slug := c.Param("id") // project slug
	if slug == "" {
		fail(c, http.StatusBadRequest, codeBadRequest, "id do projeto obrigatorio")
		return
	}

	var executions []models.QAExecution
	if err := q.db.Where("project_slug = ?", slug).Order("data desc").Find(&executions).Error; err != nil {
		failDB(c, err)
		return
	}

	// agrupar por tc_code para dar o historico completo
	type tcHistory struct {
		TCCode      string               `json:"tc_code"`
		Title       string               `json:"title"`
		Total       int                  `json:"total"`
		PassCount   int                  `json:"pass_count"`
		FailCount   int                  `json:"fail_count"`
		LastResult  string               `json:"last_result"`
		LastRun     *models.Timestamp    `json:"last_run"`
		Executions  []models.QAExecution `json:"executions"`
	}
	histMap := make(map[string]*tcHistory)
	for _, ex := range executions {
		h, ok := histMap[ex.TCCode]
		if !ok {
			h = &tcHistory{TCCode: ex.TCCode, Executions: []models.QAExecution{}}
			histMap[ex.TCCode] = h
		}
		h.Executions = append(h.Executions, ex)
		h.Total++
		if ex.Result == "PASS" {
			h.PassCount++
		} else if ex.Result == "FAIL" {
			h.FailCount++
		}
		if h.LastRun == nil || (ex.When != nil && ex.When.Time.After(h.LastRun.Time)) {
			h.LastResult = ex.Result
			h.LastRun = ex.When
		}
	}
	// buscar titulos dos TCs
	var tcs []models.QATestCase
	q.db.Where("project_slug = ?", slug).Find(&tcs)
	titleMap := make(map[string]string)
	for _, tc := range tcs {
		titleMap[tc.TCNumber] = tc.Title
	}
	result := make([]tcHistory, 0, len(histMap))
	for code, h := range histMap {
		if titleMap[code] != "" {
			h.Title = titleMap[code]
		}
		result = append(result, *h)
	}

	ok(c, http.StatusOK, gin.H{
		"project_slug": slug,
		"total": len(result),
		"testcases": result,
	})
}

// CycleStats aggregates test results per cycle for charts.
// GET /api/v1/qa/cycles/stats?project_slug=small-ngo
func (q *qaHandlers) CycleStats(c *gin.Context) {
	var rows []struct {
		CycleID     uint   `gorm:"column:ciclo_id"`
		ProjectSlug string `gorm:"column:project_slug"`
		Cycle       int    `gorm:"column:ciclo"`
		Status      string `gorm:"column:status"`
		Total       int64
		Passed      int64
		Failed      int64
		StartedAt   *models.Timestamp `gorm:"column:started_at"`
		FinishedAt  *models.Timestamp `gorm:"column:finished_at"`
	}

	projectSlug := c.Query("project_slug")

	query := q.db.Table("qa_executions").
		Select(`qa_executions.ciclo_id,
			qa_cycles.project_slug,
			qa_cycles.ciclo,
			qa_executions.status,
			COUNT(*) as total,
			SUM(CASE WHEN qa_executions.status = 'passed' THEN 1 ELSE 0 END) as passed,
			SUM(CASE WHEN qa_executions.status = 'failed' THEN 1 ELSE 0 END) as failed,
			qa_cycles.started_at,
			qa_cycles.finished_at`).
		Joins("JOIN qa_cycles ON qa_cycles.id = qa_executions.ciclo_id").
		Group("qa_executions.ciclo_id, qa_executions.status, qa_cycles.project_slug, qa_cycles.ciclo, qa_cycles.started_at, qa_cycles.finished_at")

	if projectSlug != "" {
		query = query.Where("qa_cycles.project_slug = ?", projectSlug)
	}

	query = query.Order("qa_cycles.started_at ASC")

	if err := query.Scan(&rows).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "db_error", "message": err.Error()})
		return
	}

	// Aggregate per cycle: merge passed/failed rows into one per cycle
	type CycleData struct {
		CycleID    uint                 `json:"cycle_id"`
		Label      string               `json:"label"`
		Total      int64                `json:"total"`
		Passed     int64                `json:"passed"`
		Failed     int64                `json:"failed"`
		StartedAt  *models.Timestamp    `json:"started_at,omitempty"`
		FinishedAt *models.Timestamp    `json:"finished_at,omitempty"`
	}

	cycleMap := make(map[uint]*CycleData)
	for _, r := range rows {
		if _, ok := cycleMap[r.CycleID]; !ok {
			cycleMap[r.CycleID] = &CycleData{
				CycleID:    r.CycleID,
				Label:      itoa(int64(r.Cycle)),
				StartedAt:  r.StartedAt,
				FinishedAt: r.FinishedAt,
			}
		}
		switch r.Status {
		case "passed":
			cycleMap[r.CycleID].Total += r.Total
			cycleMap[r.CycleID].Passed += r.Passed
		case "failed":
			cycleMap[r.CycleID].Total += r.Total
			cycleMap[r.CycleID].Failed += r.Failed
		default:
			cycleMap[r.CycleID].Total += r.Total
		}
	}

	result := make([]CycleData, 0, len(cycleMap))
	for _, cd := range cycleMap {
		result = append(result, *cd)
	}

	ok(c, http.StatusOK, gin.H{
		"total": len(result),
		"cycles": result,
	})
}

// itoa evita importar strconv nos handlers.
func itoa(value int64) string {
	if value == 0 {
		return "0"
	}
	negative := value < 0
	if negative {
		value = -value
	}
	buf := [20]byte{}
	index := len(buf)
	for value > 0 {
		index--
		buf[index] = byte('0' + value%10)
		value /= 10
	}
	if negative {
		index--
		buf[index] = '-'
	}
	return string(buf[index:])
}
