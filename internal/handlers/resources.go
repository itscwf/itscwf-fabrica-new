package handlers

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"github.com/itscwf/itscwf-fabrica-new/internal/models"
	"github.com/itscwf/itscwf-fabrica-new/pkg/fabrica"
)

// conflictError marca uma falha de validacao que e na verdade um conflito de
// unicidade (HTTP 409 em vez de 422).
type conflictError struct{ msg string }

func (e conflictError) Error() string { return e.msg }

func newConflict(format string, args ...any) error {
	return conflictError{msg: fmt.Sprintf(format, args...)}
}

// Status canonicos do QA (ver CHECK constraints em scripts/schema_init.sql).
var (
	cycleStatuses = map[string]bool{
		"em_andamento": true, "pronto": true, "concluido": true,
		"encerrado": true, "bloqueado": true, "cancelado": true,
	}
	testCaseStatuses = map[string]bool{
		"pending": true, "PASS": true, "FAIL": true, "SKIP": true,
		"N/A": true, "BLOCKED": true, "running": true,
	}
	testCaseStatusFromResult = map[string]string{
		"pass": "PASS", "fail": "FAIL", "skip": "SKIP", "skipped": "SKIP",
		"blocked": "BLOCKED", "n/a": "N/A", "running": "running", "pending": "pending",
	}
)

// cycleStatusAliases traduz os status usados pelo frontend ("done") para os
// valores canonicos gravados no banco.
var cycleStatusAliases = map[string]string{
	"done": "concluido", "finished": "concluido", "passed": "concluido",
	"passou": "concluido", "completed": "concluido", "pronto": "pronto",
	"running": "em_andamento", "in_progress": "em_andamento", "em_andamento": "em_andamento",
	"failed": "encerrado", "fail": "encerrado", "encerrado": "encerrado",
	"aborted": "cancelado", "cancelled": "cancelado", "canceled": "cancelado",
	"blocked": "bloqueado", "bloqueado": "bloqueado",
}

// RegisterResources monta todos os grupos CRUD descritos no card T3.
func RegisterResources(authed *gin.RouterGroup, d *Deps) {
	db, rec := d.DB, d.Recorder

	projects := &Resource[models.Project, *models.Project]{
		Name: "projects", DB: db, Recorder: rec,
		Allowed: map[string]bool{
			"name": true, "slug": true, "description": true, "status": true,
			"board_slug": true, "kanban_board": true, "local_path": true, "repo_url": true, "icon": true,
			"color": true, "archived": true, "tech_stack": true, "last_activity": true,
		},
		Search:       []string{"name", "slug", "description", "tech_stack", "repo_url"},
		Filters:      map[string]string{"status": "status", "board_slug": "board_slug"},
		KeyColumn:    "slug",
		Preload:      []string{"Repos", "Worktrees"},
		Validate:     projectValidator(db),
		DefaultOrder: "name asc",
	}

	repos := &Resource[models.Repo, *models.Repo]{
		Name: "repos", DB: db, Recorder: rec,
		Allowed: map[string]bool{
			"project_id": true, "name": true, "url": true, "host": true, "branch": true,
			"head_branch": true, "last_commit": true, "local_path": true, "is_primary": true,
			"status": true, "last_activity": true,
		},
		Search:       []string{"name", "url", "branch", "local_path", "host"},
		Filters:      map[string]string{"project_id": "project_id", "status": "status"},
		Validate:     repoValidator(db),
		DefaultOrder: "id asc",
	}

	worktrees := &Resource[models.Worktree, *models.Worktree]{
		Name: "worktrees", DB: db, Recorder: rec,
		Allowed: map[string]bool{
			"repo_id": true, "project_id": true, "name": true, "branch": true,
			"local_path": true, "git_dir": true, "kind": true, "task_id": true,
			"last_commit": true, "last_activity": true, "status": true,
		},
		Search:   []string{"name", "branch", "local_path", "task_id", "kind"},
		Filters:  map[string]string{"project_id": "project_id", "status": "status", "branch": "branch", "kind": "kind"},
		Validate: worktreeValidator(db),
		// repo_id e opcional no schema canonico (worktrees.repo_id REFERENCES
		// repos(id), anulavel): sem repo informado o INSERT e omitido para o
		// banco gravar NULL em vez de 0, que violaria a FOREIGN KEY.
		NilWhenZero:  []string{"repo_id"},
		DefaultOrder: "id desc",
	}

	qaCycles := &Resource[models.QACycle, *models.QACycle]{
		Name: "qa/cycles", DB: db, Recorder: rec,
		Allowed: map[string]bool{
			"project_id": true, "project_slug": true, "ciclo": true, "volta": true,
			"status": true, "started_at": true, "finished_at": true, "fim_previsto": true,
			"build": true, "build_commit": true, "build_tested": true, "next_tc": true,
			"worktree": true, "worktree_path": true, "blockers": true, "notes": true,
			"source_file": true,
		},
		Search:       []string{"build", "project_slug", "notes", "next_tc", "worktree"},
		Filters:      map[string]string{"project_id": "project_id", "status": "status", "build": "build", "project_slug": "project_slug"},
		TimeColumn:   "inicio",
		Preload:      []string{"TestCases", "Executions"},
		Validate:     qaCycleValidator(db),
		DefaultOrder: "id desc",
	}

	qaTestCases := &Resource[models.QATestCase, *models.QATestCase]{
		Name: "qa/testcases", DB: db, Recorder: rec,
		Allowed: map[string]bool{
			"cycle_id": true, "project_slug": true, "tc_number": true, "title": true,
			"category": true, "priority": true, "status": true, "last_execution": true,
		},
		Search:       []string{"tc_id", "titulo", "modulo", "projeto_slug"},
		Filters:      map[string]string{"cycle_id": "cycle_id", "status": "status", "category": "modulo", "project_slug": "project_slug"},
		Validate:     qaTestCaseValidator(db),
		DefaultOrder: "tc_id asc",
	}

	qaExecutions := &Resource[models.QAExecution, *models.QAExecution]{
		Name: "qa/executions", DB: db, Recorder: rec,
		Allowed: map[string]bool{
			"cycle_id": true, "tc_id": true, "tc_code": true, "project_slug": true, "result": true,
			"status": true, "duration_ms": true, "output": true, "nota": true,
			"executed_at": true, "commit_hash": true, "execution_id": true,
		},
		Search:       []string{"execution_id", "status", "tc_id", "nota"},
		Filters:      map[string]string{"cycle_id": "cycle_id", "tc_id": "testcase_id", "result": "status", "status": "status"},
		TimeColumn:   "data",
		Validate:     qaExecutionValidator(db),
		DefaultOrder: "id desc",
	}

	cronJobs := &Resource[models.CronJob, *models.CronJob]{
		Name: "cron/jobs", DB: db, Recorder: rec,
		Allowed: map[string]bool{
			"job_id": true, "name": true, "description": true, "schedule": true,
			"schedule_kind": true, "enabled": true, "state": true, "script_path": true,
			"no_agent": true, "prompt": true, "skills": true, "model": true, "provider": true,
			"deliver": true, "workdir": true, "project_slug": true, "last_run": true,
			"last_status": true, "last_error": true, "next_run_at": true, "run_count": true,
		},
		Search:       []string{"name", "schedule", "script", "job_id", "project_slug"},
		Filters:      map[string]string{"last_status": "last_status", "provider": "provider", "state": "state", "project_slug": "project_slug", "job_id": "job_id"},
		KeyColumn:    "job_id",
		Validate:     cronJobValidator(db),
		DefaultOrder: "name asc",
	}

	cronExecutions := &Resource[models.CronExecution, *models.CronExecution]{
		Name: "cron/executions", DB: d.DB, Recorder: rec,
		Allowed: map[string]bool{
			"exec_id": true, "job_id": true, "job_name": true, "source": true,
			"status": true, "claimed_at": true, "started_at": true, "finished_at": true,
			"scheduled_instant": true, "delivery_outcome": true, "duration_ms": true,
			"error": true, "exit_code": true, "output": true,
		},
		Search:     []string{"exec_id", "job_name", "status", "error", "output"},
		Filters:    map[string]string{"job_id": "job_id", "status": "status"},
		TimeColumn: "started_at",
		// ?job_id=1 e ?job_id=e054c4e9d970 funcionam igual.
		ResolveFilter: map[string]func(string) any{
			"job_id": func(value string) any {
				if id, isNumeric := numericID(value); isNumeric {
					var job models.CronJob
					if err := db.Select("job_id").Where("id = ?", id).First(&job).Error; err == nil && job.JobRef != "" {
						return string(job.JobRef)
					}
				}
				return value
			},
		},
		Validate:     cronExecutionValidator(db),
		DefaultOrder: "id desc",
	}
	cronExecutions.NotSoftDelete = false

	agents := &Resource[models.Agent, *models.Agent]{
		Name: "agents", DB: db, Recorder: rec,
		Allowed: map[string]bool{
			"slug": true, "name": true, "profile": true, "model": true, "provider": true,
			"status": true, "role": true, "tools": true, "last_seen": true,
		},
		Search:       []string{"name", "slug", "provider", "model", "role"},
		Filters:      map[string]string{"provider": "provider", "status": "status", "role": "role"},
		KeyColumn:    "slug",
		Validate:     agentValidator(db),
		DefaultOrder: "name asc",
	}

	providers := &Resource[models.Provider, *models.Provider]{
		Name: "providers", DB: db, Recorder: rec,
		Allowed: map[string]bool{
			"name": true, "kind": true, "endpoint": true, "models": true, "enabled": true,
			"limits": true, "api_key_ref": true, "status": true,
		},
		Search:       []string{"name", "base_url", "kind"},
		Filters:      map[string]string{"status": "status", "kind": "kind"},
		KeyColumn:    "name",
		Validate:     providerValidator(db),
		DefaultOrder: "name asc",
	}

	servers := &Resource[models.Server, *models.Server]{
		Name: "servers", DB: db, Recorder: rec,
		Allowed: map[string]bool{
			"hostname": true, "ip": true, "kind": true, "environment": true,
			"status": true, "notes": true, "services": true, "last_check": true,
		},
		Search:       []string{"name", "host", "kind", "services"},
		Filters:      map[string]string{"status": "status", "kind": "kind", "environment": "environment"},
		KeyColumn:    "name",
		Validate:     serverValidator(db),
		DefaultOrder: "name asc",
	}

	activityLog := &Resource[models.ActivityLog, *models.ActivityLog]{
		Name: "activity", DB: db, Recorder: nil, ReadOnly: true,
		Allowed:      map[string]bool{},
		Search:       []string{"message", "ref", "actor", "kind"},
		Filters:      map[string]string{"actor": "actor", "action": "kind", "kind": "kind", "target": "ref", "project_slug": "project_slug", "project_id": "project_id"},
		TimeColumn:   "ts",
		DefaultOrder: "id desc",
	}

	projects.Register(authed.Group("/projects"))
	repos.Register(authed.Group("/repos"))
	worktrees.Register(authed.Group("/worktrees"))
	qaCycles.Register(authed.Group("/qa/cycles"))
	qaTestCases.Register(authed.Group("/qa/testcases"))
	qaExecutions.Register(authed.Group("/qa/executions"))
	cronJobs.Register(authed.Group("/cron/jobs"))
	cronExecutions.Register(authed.Group("/cron/executions"))
	agents.Register(authed.Group("/agents"))
	providers.Register(authed.Group("/providers"))
	servers.Register(authed.Group("/servers"))
	activityLog.Register(authed.Group("/activity"))

	// ECAs tem steps/dependencias em tabelas filhas: handlers proprios.
	eh := &ecaHandlers{db: db, rec: rec}
	ecas := authed.Group("/ecas")
	eh.Register(ecas)

	// Acoes que nao sao CRUD puro.
	qh := &qaHandlers{db: db, rec: rec}
	qa := authed.Group("/qa")
	qa.POST("/cycles/:id/close", qh.CloseCycle)
	qa.POST("/cycles/:id/reopen", qh.ReopenCycle)
	qa.GET("/cycles/stats", qh.CycleStats)
	qa.GET("/testcases/:id/executions", qh.GetTestCaseExecutions)

	authed.Group("/projects").GET("/:id/testcases", qh.GetProjectTestCases)

	ch := &cronHandlers{db: db, rec: rec}
	authed.Group("/cron").POST("/jobs/:id/toggle", ch.Toggle)
}

// ---------------------------------------------------------------------------
// Validadores
// ---------------------------------------------------------------------------

func projectValidator(db *gorm.DB) func(*models.Project) error {
	return func(p *models.Project) error {
		if err := requireText(map[string]string{p.Name: "name"}); err != nil {
			return err
		}
		p.Name = strings.TrimSpace(p.Name)
		if strings.TrimSpace(p.Slug) == "" {
			p.Slug = fabrica.Slugify(p.Name)
		} else {
			p.Slug = fabrica.Slugify(p.Slug)
		}
		if p.Slug == "" {
			return errors.New("slug e obrigatorio (nao foi possivel derivar de name)")
		}
		if p.Status == "" {
			p.Status = "active"
		}
		switch p.Status {
		case "active", "paused", "archived", "planning":
		default:
			return fmt.Errorf("status %q invalido (active|paused|archived|planning)", p.Status)
		}
		if p.BoardSlug == "" {
			p.BoardSlug = p.Slug
		}
		// Se slug existe mas esta soft-deleted, restaura o registro.
		var existing models.Project
		if err := db.Unscoped().Where("slug = ? AND deleted_at IS NOT NULL", p.Slug).First(&existing).Error; err == nil {
			// Restaura: limpa deleted_at e atualiza os campos do payload.
			updates := map[string]any{
				"deleted_at":     nil,
				"name":           p.Name,
				"description":    p.Description,
				"status":        p.Status,
				"board_slug":    p.BoardSlug,
				"primary_path":  p.LocalPath,
				"repo_url":      p.RepoURL,
				"icon":          p.Icon,
				"color":         p.Color,
				"archived":      false,
				"updated_at":    time.Now().UTC().Format("2006-01-02T15:04:05Z"),
			}
			if err := db.Unscoped().Model(&existing).Updates(updates).Error; err != nil {
				return fmt.Errorf("restaurar project: %w", err)
			}
			// Copia o ID gerado para o payload (assim a resposta mostra o ID correto).
			p.ID = existing.ID
			return nil
		}
		return uniqueField(db, &models.Project{}, "slug", p.Slug, p.ID)
	}
}

func repoValidator(db *gorm.DB) func(*models.Repo) error {
	return func(r *models.Repo) error {
		if r.ProjectID == 0 {
			return errors.New("project_id e obrigatorio")
		}
		if err := requireText(map[string]string{r.Name: "name", r.URL: "url"}); err != nil {
			return err
		}
		if r.Branch == "" {
			r.Branch = "main"
		}
		if r.Status == "" {
			r.Status = "active"
		}
		if err := projectExists(db, r.ProjectID); err != nil {
			return err
		}
		return uniqueField(db, &models.Repo{}, "url", r.URL, r.ID)
	}
}

func worktreeValidator(db *gorm.DB) func(*models.Worktree) error {
	return func(w *models.Worktree) error {
		if w.ProjectID == 0 {
			return errors.New("project_id e obrigatorio")
		}
		if err := requireText(map[string]string{w.LocalPath: "local_path"}); err != nil {
			return err
		}
		if w.Kind == "" {
			w.Kind = "worktree"
		}
		switch w.Kind {
		case "worktree", "clone", "qa", "task", "feature":
		default:
			return fmt.Errorf("kind %q invalido (worktree|clone|qa|task|feature)", w.Kind)
		}
		if w.Name == "" {
			w.Name = filepath.Base(strings.TrimRight(w.LocalPath, "/"))
		}
		if w.Status == "" {
			w.Status = "active"
		}
		if err := projectExists(db, w.ProjectID); err != nil {
			return err
		}
		// repo_id e opcional (a coluna e anulavel), mas se vier precisa
		// apontar para um repo existente — sem isso a FOREIGN KEY estoura no
		// INSERT e o cliente receberia um conflito generico.
		if w.RepoID != 0 {
			if err := repoExists(db, w.RepoID); err != nil {
				return err
			}
		}
		return uniqueField(db, &models.Worktree{}, "local_path", w.LocalPath, w.ID)
	}
}

func qaCycleValidator(db *gorm.DB) func(*models.QACycle) error {
	return func(cycle *models.QACycle) error {
		if cycle.ProjectID == 0 && strings.TrimSpace(cycle.ProjectSlug) == "" {
			return errors.New("project_id ou project_slug e obrigatorio")
		}
		var project models.Project
		switch {
		case cycle.ProjectID != 0:
			if err := db.Where("id = ?", cycle.ProjectID).First(&project).Error; err != nil {
				return fmt.Errorf("project %d nao existe", cycle.ProjectID)
			}
		default:
			if err := db.Where("slug = ?", cycle.ProjectSlug).First(&project).Error; err != nil {
				return fmt.Errorf("project %q nao existe", cycle.ProjectSlug)
			}
			cycle.ProjectID = project.ID
		}
		cycle.ProjectSlug = project.Slug

		if normalized, ok := normalizeCycleStatus(cycle.Status); ok {
			cycle.Status = normalized
		} else if cycle.Status == "" {
			cycle.Status = "em_andamento"
		} else {
			return fmt.Errorf("status %q invalido (em_andamento|pronto|concluido|encerrado|bloqueado|cancelado)", cycle.Status)
		}

		if cycle.Ciclo <= 0 {
			var last models.QACycle
			query := db.Where("project_slug = ?", cycle.ProjectSlug)
			if cycle.ID != 0 {
				query = query.Where("id <> ?", cycle.ID)
			}
			if err := query.Order("ciclo desc").Limit(1).Find(&last).Error; err != nil {
				return err
			}
			cycle.Ciclo = last.Ciclo + 1
		}
		if cycle.Volta <= 0 {
			cycle.Volta = 1
		}
		if cycle.StartedAt == nil && cycle.Status == "em_andamento" {
			now := models.Now()
			cycle.StartedAt = &now
		}
		return uniqueCiclo(db, cycle)
	}
}

func qaTestCaseValidator(db *gorm.DB) func(*models.QATestCase) error {
	return func(tc *models.QATestCase) error {
		if tc.CycleID == 0 {
			return errors.New("cycle_id e obrigatorio")
		}
		if err := requireText(map[string]string{tc.Title: "title", tc.TCNumber: "tc_number"}); err != nil {
			return err
		}
		var cycle models.QACycle
		if err := db.Where("id = ?", tc.CycleID).First(&cycle).Error; err != nil {
			return fmt.Errorf("cycle %d nao existe", tc.CycleID)
		}
		tc.Slug = cycle.ProjectSlug
		tc.Status = normalizeTestCaseStatus(tc.Status)
		if !testCaseStatuses[tc.Status] {
			return fmt.Errorf("status %q invalido (pending|PASS|FAIL|SKIP|N/A|BLOCKED|running)", tc.Status)
		}
		return uniqueField(db, &models.QATestCase{}, "cycle_id, tc_id",
			fmt.Sprintf("%d, %s", tc.CycleID, tc.TCNumber), tc.ID)
	}
}

func qaExecutionValidator(db *gorm.DB) func(*models.QAExecution) error {
	return func(exec *models.QAExecution) error {
		result := strings.ToLower(strings.TrimSpace(exec.Result))
		switch result {
		case "pass", "fail", "blocked", "skipped", "skip", "running", "pending":
		case "":
			return errors.New("result e obrigatorio (pass|fail|blocked|skipped)")
		default:
			return fmt.Errorf("result %q invalido (pass|fail|blocked|skipped)", exec.Result)
		}
		if result == "skip" {
			result = "skipped"
		}
		exec.Result = result

		// Aceita tc_id numerico (id interno) ou o codigo do TC (TC-001).
		var testCase models.QATestCase
		switch {
		case exec.TestCaseID != 0:
			if err := db.Where("id = ?", exec.TestCaseID).First(&testCase).Error; err != nil {
				return fmt.Errorf("testcase %d nao existe", exec.TestCaseID)
			}
		case strings.TrimSpace(exec.TCCode) != "":
			query := db.Where("tc_id = ?", exec.TCCode)
			if exec.CycleID != 0 {
				query = query.Where("cycle_id = ?", exec.CycleID)
			}
			if err := query.First(&testCase).Error; err != nil {
				return fmt.Errorf("testcase %q nao existe", exec.TCCode)
			}
		default:
			return errors.New("informe tc_id (id do caso de teste) ou tc_code (TC-001)")
		}
		if exec.CycleID != 0 && exec.CycleID != testCase.CycleID {
			return fmt.Errorf("testcase %d pertence ao ciclo %d", testCase.ID, testCase.CycleID)
		}
		exec.TestCaseID = testCase.ID
		exec.CycleID = testCase.CycleID
		exec.TCCode = testCase.TCNumber
		exec.Slug = testCase.Slug

		if exec.When == nil {
			now := models.Now()
			exec.When = &now
		}
		if exec.ExecutionID == "" {
			exec.ExecutionID = fmt.Sprintf("EXEC-%d-%d", testCase.ID, exec.When.Time.UnixNano()/1e6)
		}
		// Espelha o resultado no caso de teste (contadores do ciclo sao
		// recalculados no fechamento).
		status := testCaseStatusFromResult[result]
		if err := db.Model(&models.QATestCase{}).Where("id = ?", testCase.ID).
			Updates(map[string]any{"status": status, "ultima_execucao": exec.When}).Error; err != nil {
			return err
		}
		return nil
	}
}

func cronJobValidator(db *gorm.DB) func(*models.CronJob) error {
	return func(job *models.CronJob) error {
		if err := requireText(map[string]string{job.Name: "name"}); err != nil {
			return err
		}
		if job.Schedule == "" && job.Script == "" && job.Prompt == "" {
			return errors.New("informe schedule, script_path ou prompt")
		}
		if job.State == "" {
			if job.Enabled {
				job.State = "scheduled"
			} else {
				job.State = "paused"
			}
		}
		// job_id e NOT NULL UNIQUE no schema canonico e, na pratica, e a chave
		// natural do jobs.json do Hermes. Quando o cliente nao informa um
		// (job criado pelo dashboard), a API gera no mesmo formato — sem isso o
		// INSERT gravaria NULL e o POST falharia em producao.
		if strings.TrimSpace(string(job.JobRef)) == "" {
			generated, err := fabrica.NewJobID()
			if err != nil {
				return err
			}
			job.JobRef = models.RefString(generated)
		}
		return uniqueField(db, &models.CronJob{}, "job_id", string(job.JobRef), job.ID)
	}
}

func cronExecutionValidator(db *gorm.DB) func(*models.CronExecution) error {
	return func(exec *models.CronExecution) error {
		ref := strings.TrimSpace(string(exec.JobRef))
		if ref == "" {
			return errors.New("job_id e obrigatorio (id do job ou id numerico)")
		}
		// Aceita tanto a chave interna (id) quanto o job_id do Hermes.
		if id, isNumeric := numericID(ref); isNumeric {
			var job models.CronJob
			if err := db.Where("id = ?", id).First(&job).Error; err != nil {
				return fmt.Errorf("cron job %d nao existe", id)
			}
			ref = string(job.JobRef)
			if exec.JobName == "" {
				exec.JobName = job.Name
			}
		} else if exec.JobName == "" {
			var job models.CronJob
			if err := db.Where("job_id = ?", ref).First(&job).Error; err == nil {
				exec.JobName = job.Name
			}
		}
		exec.JobRef = models.RefString(ref)

		if exec.Status == "" {
			if exec.ExitCode == 0 {
				exec.Status = "ok"
			} else {
				exec.Status = "error"
			}
		}
		if exec.StartedAt == nil {
			now := models.Now()
			exec.StartedAt = &now
		}
		if exec.FinishedAt == nil {
			finished := models.Now()
			if exec.StartedAt != nil && exec.DurationMS > 0 {
				finished = models.NewTimestamp(exec.StartedAt.Add(timeMillis(exec.DurationMS)))
			}
			exec.FinishedAt = &finished
		}
		if exec.ExecID == "" {
			generated, err := newExecutionID(ref, exec.StartedAt.Time)
			if err != nil {
				return err
			}
			exec.ExecID = generated
		}
		return nil
	}
}

func agentValidator(db *gorm.DB) func(*models.Agent) error {
	return func(a *models.Agent) error {
		if err := requireText(map[string]string{a.Name: "name"}); err != nil {
			return err
		}
		a.Name = strings.TrimSpace(a.Name)
		if strings.TrimSpace(a.Slug) == "" {
			a.Slug = fabrica.Slugify(a.Name)
		}
		switch strings.ToLower(strings.TrimSpace(a.Status)) {
		case "online", "ativo", "active":
			a.Status = "online"
		case "busy", "ocupado":
			a.Status = "busy"
		case "error", "erro", "failed":
			a.Status = "error"
		case "offline", "unknown", "", "desconhecido":
			a.Status = "offline"
		default:
			return fmt.Errorf("status %q invalido (online|offline|busy|error)", a.Status)
		}
		// O schema canonico exige slug unico.
		return uniqueField(db, &models.Agent{}, "slug", a.Slug, a.ID)
	}
}

func providerValidator(db *gorm.DB) func(*models.Provider) error {
	return func(p *models.Provider) error {
		if err := requireText(map[string]string{p.Name: "name"}); err != nil {
			return err
		}
		p.Name = strings.TrimSpace(p.Name)
		if p.Status == "" {
			p.Status = "active"
		}
		return uniqueField(db, &models.Provider{}, "name", p.Name, p.ID)
	}
}

func serverValidator(db *gorm.DB) func(*models.Server) error {
	return func(s *models.Server) error {
		if err := requireText(map[string]string{s.Name: "hostname"}); err != nil {
			return err
		}
		s.Name = strings.TrimSpace(s.Name)
		if s.Status == "" {
			s.Status = "unknown"
		}
		return uniqueField(db, &models.Server{}, "name", s.Name, s.ID)
	}
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// uniqueField gera 409 quando outra linha viva ja usa o valor. A unicidade e
// garantida na aplicacao porque soft delete + UNIQUE INDEX se atrapalham.
func uniqueField(db *gorm.DB, model any, columns, value string, excludeID int64) error {
	parts := strings.Split(columns, ",")
	if len(parts) == 2 {
		// Suporta chave composta simples: coluna + valor formatado "a, b".
		values := strings.SplitN(value, ",", 2)
		if len(values) == 2 {
			query := db.Model(model).Where("deleted_at IS NULL AND "+strings.TrimSpace(parts[0])+" = ? AND "+strings.TrimSpace(parts[1])+" = ?",
				strings.TrimSpace(values[0]), strings.TrimSpace(values[1]))
			if excludeID != 0 {
				query = query.Where("id <> ?", excludeID)
			}
			var count int64
			if err := query.Count(&count).Error; err != nil {
				return err
			}
			if count > 0 {
				return newConflict("%s %q ja existe", columns, value)
			}
			return nil
		}
	}

	query := db.Model(model).Where("deleted_at IS NULL AND "+columns+" = ?", value)
	if excludeID != 0 {
		query = query.Where("id <> ?", excludeID)
	}
	var count int64
	if err := query.Count(&count).Error; err != nil {
		return err
	}
	if count > 0 {
		return newConflict("%s %q ja esta em uso", columns, value)
	}
	return nil
}

func projectExists(db *gorm.DB, id int64) error {
	var count int64
	if err := db.Model(&models.Project{}).Where("id = ?", id).Count(&count).Error; err != nil {
		return err
	}
	if count == 0 {
		return fmt.Errorf("project %d nao existe", id)
	}
	return nil
}

// repoExists garante que a FK opcional repo_id aponte para uma linha viva.
func repoExists(db *gorm.DB, id int64) error {
	var count int64
	if err := db.Model(&models.Repo{}).Where("id = ? AND deleted_at IS NULL", id).Count(&count).Error; err != nil {
		return err
	}
	if count == 0 {
		return fmt.Errorf("repo %d nao existe", id)
	}
	return nil
}

// uniqueCiclo garante UNIQUE(project_slug, ciclo) antes do INSERT.
func uniqueCiclo(db *gorm.DB, cycle *models.QACycle) error {
	query := db.Model(&models.QACycle{}).Where("project_slug = ? AND ciclo = ?", cycle.ProjectSlug, cycle.Ciclo)
	if cycle.ID != 0 {
		query = query.Where("id <> ?", cycle.ID)
	}
	var count int64
	if err := query.Count(&count).Error; err != nil {
		return err
	}
	if count > 0 {
		return newConflict("ciclo %d do projeto %s ja existe", cycle.Ciclo, cycle.ProjectSlug)
	}
	return nil
}

// normalizeCycleStatus traduz aliases ("done") para os status canonicos.
func normalizeCycleStatus(raw string) (string, bool) {
	normalized := strings.ToLower(strings.TrimSpace(raw))
	normalized = strings.ReplaceAll(normalized, " ", "_")
	if normalized == "" {
		return "", false
	}
	if cycleStatuses[normalized] {
		return normalized, true
	}
	if mapped, ok := cycleStatusAliases[normalized]; ok {
		return mapped, true
	}
	return normalized, false
}

// normalizeTestCaseStatus normaliza o status do caso de teste.
func normalizeTestCaseStatus(raw string) string {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "pending"
	}
	upper := strings.ToUpper(trimmed)
	switch upper {
	case "PASS", "PASSED", "OK", "SUCCESS":
		return "PASS"
	case "FAIL", "FAILED", "ERROR":
		return "FAIL"
	case "SKIP", "SKIPPED", "N/A-", "NA":
		return "SKIP"
	case "BLOCKED", "BLOCKER":
		return "BLOCKED"
	case "N/A":
		return "N/A"
	case "PENDING", "TODO", "":
		return "pending"
	case "RUNNING", "IN_PROGRESS", "EM_ANDAMENTO":
		return "running"
	default:
		return trimmed
	}
}

// testCaseStatusFromResult mapeia o resultado da execucao para o status do TC.

func sanitizeRef(raw string) string {
	cleaned := strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			return r
		default:
			return '-'
		}
	}, raw)
	return strings.Trim(cleaned, "-")
}

// newExecutionID gera o id legivel de uma execucao registrada pela API:
// EXEC-<ref>-<unix_millis>-<hex>. A versao anterior usava so os segundos do
// inicio, portanto duas execucoes do mesmo job no mesmo segundo colidiam no
// UNIQUE(exec_id) de cron_executions e a segunda devolvia 409 conflict.
func newExecutionID(ref string, at time.Time) (string, error) {
	suffix, err := randomHex(3)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("EXEC-%s-%d-%s", sanitizeRef(ref), at.UnixMilli(), suffix), nil
}

// randomHex devolve n bytes aleatorios em hexadecimal.
func randomHex(bytes int) (string, error) {
	buf := make([]byte, bytes)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("gerar id aleatorio: %w", err)
	}
	return hex.EncodeToString(buf), nil
}

func timeMillis(ms int64) time.Duration {
	return time.Duration(ms) * time.Millisecond
}
