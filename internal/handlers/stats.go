package handlers

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	dbpkg "github.com/itscwf/itscwf-fabrica-new/internal/db"
	"github.com/itscwf/itscwf-fabrica-new/internal/models"
)

// statsHandlers alimenta o dashboard com uma unica chamada.
type statsHandlers struct {
	deps *Deps
}

type statsResponse struct {
	Projects       projectStats           `json:"projects"`
	Repos          int64                  `json:"repos"`
	Worktrees      int64                  `json:"worktrees"`
	ECAs           ecaStats               `json:"ecas"`
	CronJobs       cronStats              `json:"cron_jobs"`
	Agents         agentStats             `json:"agents"`
	QA             qaStats                `json:"qa"`
	RecentCron     []models.CronExecution `json:"recent_cron_executions"`
	RecentActivity []models.ActivityLog   `json:"recent_activity"`
	GeneratedAt    string                 `json:"generated_at"`
}

type projectStats struct {
	Total    int64            `json:"total"`
	Active   int64            `json:"active"`
	Archived int64            `json:"archived"`
	ByStatus map[string]int64 `json:"by_status"`
}

type ecaStats struct {
	Total      int64            `json:"total"`
	InProgress int64            `json:"in_progress"`
	Done       int64            `json:"done"`
	ByStatus   map[string]int64 `json:"by_status"`
}

type cronStats struct {
	Total     int64 `json:"total"`
	Enabled   int64 `json:"enabled"`
	Failing   int64 `json:"failing"`
	ExecToday int64 `json:"executions_today"`
}

type agentStats struct {
	Total  int64 `json:"total"`
	Online int64 `json:"online"`
	Busy   int64 `json:"busy"`
}

type qaStats struct {
	Cycles           int64         `json:"cycles"`
	RunningCycles    int64         `json:"running_cycles"`
	TestCases        int64         `json:"test_cases"`
	Executions       int64         `json:"executions"`
	PassRate         float64       `json:"pass_rate"`
	PassedExecutions int64         `json:"passed_executions"`
	FailedExecutions int64         `json:"failed_executions"`
	RecentCycles     []recentCycle `json:"recent_cycles"`
}

type recentCycle struct {
	ID          int64  `json:"id"`
	ProjectID   int64  `json:"project_id"`
	ProjectName string `json:"project_name"`
	Build       string `json:"build"`
	Status      string `json:"status"`
	Ciclo       int    `json:"ciclo"`
	StartedAt   any    `json:"started_at"`
	FinishedAt  any    `json:"finished_at"`
	NextTC      string `json:"next_tc"`
	TotalTCs    int    `json:"total"`
	PassedTCs   int64  `json:"passed"`
	FailedTCs   int64  `json:"failed"`
}

// Stats atende GET /api/v1/stats (e /api/v1/dashboard).
func (h *statsHandlers) Stats(c *gin.Context) {
	db := h.deps.DB
	response := statsResponse{
		Projects:       projectStats{ByStatus: map[string]int64{}},
		ECAs:           ecaStats{ByStatus: map[string]int64{}},
		RecentCron:     []models.CronExecution{},
		RecentActivity: []models.ActivityLog{},
	}

	if err := db.Model(&models.Project{}).Where("deleted_at IS NULL").Count(&response.Projects.Total).Error; err != nil {
		failDB(c, err)
		return
	}
	if err := db.Model(&models.Project{}).Where("deleted_at IS NULL AND status = ?", "active").Count(&response.Projects.Active).Error; err != nil {
		failDB(c, err)
		return
	}
	if err := db.Model(&models.Project{}).Where("deleted_at IS NULL AND (archived = 1 OR status = 'archived')").Count(&response.Projects.Archived).Error; err != nil {
		failDB(c, err)
		return
	}
	response.Projects.ByStatus = groupCount(db, &models.Project{}, "status")

	if err := db.Model(&models.Repo{}).Where("deleted_at IS NULL").Count(&response.Repos).Error; err != nil {
		failDB(c, err)
		return
	}
	if err := db.Model(&models.Worktree{}).Where("deleted_at IS NULL").Count(&response.Worktrees).Error; err != nil {
		failDB(c, err)
		return
	}

	if err := db.Model(&models.ECA{}).Where("deleted_at IS NULL").Count(&response.ECAs.Total).Error; err != nil {
		failDB(c, err)
		return
	}
	if err := db.Model(&models.ECA{}).Where("deleted_at IS NULL AND status = ?", "in_progress").Count(&response.ECAs.InProgress).Error; err != nil {
		failDB(c, err)
		return
	}
	if err := db.Model(&models.ECA{}).Where("deleted_at IS NULL AND status = ?", "done").Count(&response.ECAs.Done).Error; err != nil {
		failDB(c, err)
		return
	}
	response.ECAs.ByStatus = groupCount(db, &models.ECA{}, "status")

	if err := db.Model(&models.CronJob{}).Where("deleted_at IS NULL").Count(&response.CronJobs.Total).Error; err != nil {
		failDB(c, err)
		return
	}
	if err := db.Model(&models.CronJob{}).Where("deleted_at IS NULL AND enabled = 1").Count(&response.CronJobs.Enabled).Error; err != nil {
		failDB(c, err)
		return
	}
	if err := db.Model(&models.CronJob{}).Where("deleted_at IS NULL AND last_status IN ?", []string{"error", "failed"}).Count(&response.CronJobs.Failing).Error; err != nil {
		failDB(c, err)
		return
	}
	if err := db.Model(&models.CronExecution{}).
		Where("started_at >= ?", models.Now().Time.AddDate(0, 0, -1).Format(models.Layout)).
		Count(&response.CronJobs.ExecToday).Error; err != nil {
		failDB(c, err)
		return
	}

	if err := db.Model(&models.Agent{}).Where("deleted_at IS NULL").Count(&response.Agents.Total).Error; err != nil {
		failDB(c, err)
		return
	}
	if err := db.Model(&models.Agent{}).Where("deleted_at IS NULL AND status = ?", "online").Count(&response.Agents.Online).Error; err != nil {
		failDB(c, err)
		return
	}
	if err := db.Model(&models.Agent{}).Where("deleted_at IS NULL AND status = ?", "busy").Count(&response.Agents.Busy).Error; err != nil {
		failDB(c, err)
		return
	}

	if err := db.Model(&models.QACycle{}).Where("deleted_at IS NULL").Count(&response.QA.Cycles).Error; err != nil {
		failDB(c, err)
		return
	}
	if err := db.Model(&models.QACycle{}).Where("deleted_at IS NULL AND status = ?", "em_andamento").Count(&response.QA.RunningCycles).Error; err != nil {
		failDB(c, err)
		return
	}
	if err := db.Model(&models.QATestCase{}).Where("deleted_at IS NULL").Count(&response.QA.TestCases).Error; err != nil {
		failDB(c, err)
		return
	}
	if err := db.Model(&models.QAExecution{}).Where("deleted_at IS NULL").Count(&response.QA.Executions).Error; err != nil {
		failDB(c, err)
		return
	}
	if err := db.Model(&models.QAExecution{}).Where("deleted_at IS NULL AND LOWER(status) = ?", "pass").Count(&response.QA.PassedExecutions).Error; err != nil {
		failDB(c, err)
		return
	}
	if err := db.Model(&models.QAExecution{}).Where("deleted_at IS NULL AND LOWER(status) = ?", "fail").Count(&response.QA.FailedExecutions).Error; err != nil {
		failDB(c, err)
		return
	}
	graded := response.QA.PassedExecutions + response.QA.FailedExecutions
	if graded > 0 {
		response.QA.PassRate = float64(response.QA.PassedExecutions) / float64(graded)
	}

	var cycles []models.QACycle
	if err := db.Where("deleted_at IS NULL").Order("id desc").Limit(10).Find(&cycles).Error; err != nil {
		failDB(c, err)
		return
	}
	response.QA.RecentCycles = make([]recentCycle, 0, len(cycles))
	for _, cycle := range cycles {
		var total, passed, failed int64
		db.Model(&models.QATestCase{}).Where("cycle_id = ?", cycle.ID).Count(&total)
		db.Model(&models.QATestCase{}).Where("cycle_id = ? AND status = ?", cycle.ID, "PASS").Count(&passed)
		db.Model(&models.QATestCase{}).Where("cycle_id = ? AND status = ?", cycle.ID, "FAIL").Count(&failed)
		response.QA.RecentCycles = append(response.QA.RecentCycles, recentCycle{
			ID:          cycle.ID,
			ProjectID:   cycle.ProjectID,
			ProjectName: cycle.ProjectSlug,
			Build:       cycle.Build,
			Status:      cycle.Status,
			Ciclo:       cycle.Ciclo,
			StartedAt:   cycle.StartedAt,
			FinishedAt:  cycle.FinishedAt,
			NextTC:      cycle.NextTC,
			TotalTCs:    cycle.TotalTCs,
			PassedTCs:   passed,
			FailedTCs:   failed,
		})
	}

	if err := db.Order("id desc").Limit(10).Find(&response.RecentCron).Error; err != nil {
		failDB(c, err)
		return
	}
	if err := db.Order("id desc").Limit(10).Find(&response.RecentActivity).Error; err != nil {
		failDB(c, err)
		return
	}
	response.GeneratedAt = models.Now().Time.Format(models.Layout)
	c.JSON(http.StatusOK, response)
}

// groupCount devolve a contagem agrupada por uma coluna.
func groupCount(db *gorm.DB, model any, column string) map[string]int64 {
	type row struct {
		Key   string
		Total int64
	}
	rows := []row{}
	if err := db.Model(model).Select(column + " as key, count(*) as total").Group(column).Scan(&rows).Error; err != nil {
		return map[string]int64{}
	}
	out := make(map[string]int64, len(rows))
	for _, item := range rows {
		out[item.Key] = item.Total
	}
	return out
}

// journalModeOf expoe o modo de journal do SQLite no /health.
func journalModeOf(conn *gorm.DB) (string, error) {
	return dbpkg.JournalMode(conn)
}
