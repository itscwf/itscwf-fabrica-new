package handlers

import (
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/itscwf/itscwf-fabrica-new/internal/middleware"
)

func TestHealthIsPublic(t *testing.T) {
	env := newTestEnv(t)

	for _, path := range []string{"/health", "/healthz", "/api/v1/health"} {
		rec := env.request(http.MethodGet, path, nil, "")
		env.expectStatus(rec, http.StatusOK)
		body := env.decode(rec)
		if body["status"] != "ok" {
			t.Fatalf("%s: status = %v", path, body["status"])
		}
		if mode, _ := body["journal_mode"].(string); mode != "wal" {
			t.Fatalf("%s: journal_mode = %v, want wal (body %v)", path, body["journal_mode"], body)
		}
		if body["database"] != "ok" {
			t.Fatalf("%s: database = %v", path, body["database"])
		}
	}
}
func TestProtectedRoutesRequireJWT(t *testing.T) {
	env := newTestEnv(t)

	routes := []struct {
		method string
		path   string
	}{
		{http.MethodGet, "/api/v1/projects"},
		{http.MethodPost, "/api/v1/projects"},
		{http.MethodGet, "/api/v1/projects/1"},
		{http.MethodPut, "/api/v1/projects/1"},
		{http.MethodDelete, "/api/v1/projects/1"},
		{http.MethodGet, "/api/v1/repos"},
		{http.MethodPost, "/api/v1/repos"},
		{http.MethodGet, "/api/v1/worktrees"},
		{http.MethodGet, "/api/v1/ecas"},
		{http.MethodPost, "/api/v1/ecas"},
		{http.MethodGet, "/api/v1/qa/cycles"},
		{http.MethodPost, "/api/v1/qa/cycles"},
		{http.MethodPost, "/api/v1/qa/cycles/1/close"},
		{http.MethodGet, "/api/v1/qa/testcases"},
		{http.MethodPost, "/api/v1/qa/testcases"},
		{http.MethodGet, "/api/v1/qa/executions"},
		{http.MethodPost, "/api/v1/qa/executions"},
		{http.MethodGet, "/api/v1/cron/jobs"},
		{http.MethodPost, "/api/v1/cron/jobs"},
		{http.MethodPut, "/api/v1/cron/jobs/1"},
		{http.MethodDelete, "/api/v1/cron/jobs/1"},
		{http.MethodPost, "/api/v1/cron/jobs/1/toggle"},
		{http.MethodGet, "/api/v1/cron/executions"},
		{http.MethodPost, "/api/v1/cron/executions"},
		{http.MethodGet, "/api/v1/agents"},
		{http.MethodPost, "/api/v1/agents"},
		{http.MethodGet, "/api/v1/providers"},
		{http.MethodGet, "/api/v1/servers"},
		{http.MethodGet, "/api/v1/activity"},
		{http.MethodGet, "/api/v1/hermes/tasks"},
		{http.MethodGet, "/api/v1/hermes/agents"},
		{http.MethodGet, "/api/v1/hermes/crons"},
		{http.MethodGet, "/api/v1/stats"},
	}

	for _, route := range routes {
		rec := env.request(route.method, route.path, nil, "")
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("%s %s without token: status = %d, want 401 (body %s)", route.method, route.path, rec.Code, rec.Body.String())
		}
		if code := env.errorCode(rec); code != "unauthorized" {
			t.Fatalf("%s %s: error code = %q", route.method, route.path, code)
		}
	}

	rec := env.request(http.MethodGet, "/api/v1/projects", nil, "not-a-jwt")
	env.expectStatus(rec, http.StatusUnauthorized)
}

func TestLoginRefreshAndMe(t *testing.T) {
	env := newTestEnv(t)

	rec := env.request(http.MethodPost, "/api/v1/auth/login", map[string]any{"username": "admin", "password": "wrong"}, "")
	env.expectStatus(rec, http.StatusUnauthorized)

	rec = env.request(http.MethodPost, "/api/v1/auth/login", map[string]any{"username": "nobody", "password": "s3cret"}, "")
	env.expectStatus(rec, http.StatusUnauthorized)

	rec = env.request(http.MethodPost, "/api/v1/auth/login", map[string]any{"username": "admin", "password": "s3cret"}, "")
	env.expectStatus(rec, http.StatusOK)
	body := env.decode(rec)
	token, _ := body["token"].(string)
	if token == "" {
		t.Fatalf("login returned no token: %s", rec.Body.String())
	}
	if body["token_type"] != "Bearer" {
		t.Fatalf("token_type = %v", body["token_type"])
	}

	rec = env.request(http.MethodGet, "/api/v1/auth/me", nil, token)
	env.expectStatus(rec, http.StatusOK)
	me := env.object(rec)
	if me["username"] != "admin" || me["role"] != middleware.RoleAdmin {
		t.Fatalf("unexpected me payload: %v", me)
	}

	rec = env.request(http.MethodPost, "/api/v1/auth/refresh", nil, token)
	env.expectStatus(rec, http.StatusOK)
	if refreshed, _ := env.decode(rec)["token"].(string); refreshed == "" {
		t.Fatalf("refresh returned no token")
	}

	// The failed and successful logins must be audited.
	rec = env.authed(http.MethodGet, "/api/v1/activity?action=login", nil)
	env.expectStatus(rec, http.StatusOK)
	if len(env.items(rec)) == 0 {
		t.Fatalf("expected a login entry in activity_log")
	}
}

func TestExpiredTokenIsRejected(t *testing.T) {
	env := newTestEnv(t)

	expired, err := fabricaNewManager(t, env.Cfg.Auth.JWTSecret, -time.Hour)
	if err != nil {
		t.Fatalf("expired manager: %v", err)
	}
	token, _, err := expired.Issue("admin", middleware.RoleAdmin)
	if err != nil {
		t.Fatalf("issue expired token: %v", err)
	}

	rec := env.request(http.MethodGet, "/api/v1/projects", nil, token)
	env.expectStatus(rec, http.StatusUnauthorized)
}

func TestProjectsCRUDSoftDeleteAndAudit(t *testing.T) {
	env := newTestEnv(t)

	// create
	rec := env.authed(http.MethodPost, "/api/v1/projects", map[string]any{
		"name": "Small NGO", "repo_url": "https://github.com/itscwf/small-ngo.git", "tech_stack": "go,react",
	})
	env.expectStatus(rec, http.StatusCreated)
	project := env.object(rec)
	if project["slug"] != "small-ngo" {
		t.Fatalf("slug = %v, want small-ngo", project["slug"])
	}
	if project["status"] != "active" {
		t.Fatalf("status = %v, want active", project["status"])
	}
	id := uint(project["id"].(float64))

	// duplicate slug -> 409
	rec = env.authed(http.MethodPost, "/api/v1/projects", map[string]any{"name": "small-ngo"})
	env.expectStatus(rec, http.StatusConflict)
	if code := env.errorCode(rec); code != "conflict" {
		t.Fatalf("error code = %q", code)
	}

	// validation -> 422
	rec = env.authed(http.MethodPost, "/api/v1/projects", map[string]any{"repo_url": "x"})
	env.expectStatus(rec, http.StatusUnprocessableEntity)
	if code := env.errorCode(rec); code != "validation_error" {
		t.Fatalf("error code = %q", code)
	}

	// read by id and by natural key
	rec = env.authed(http.MethodGet, fmt.Sprintf("/api/v1/projects/%d", id), nil)
	env.expectStatus(rec, http.StatusOK)
	rec = env.authed(http.MethodGet, "/api/v1/projects/small-ngo", nil)
	env.expectStatus(rec, http.StatusOK)

	// merge update: description must survive an update that only sends status
	rec = env.authed(http.MethodPut, fmt.Sprintf("/api/v1/projects/%d", id), map[string]any{"description": "ONG SaaS", "status": "archived"})
	env.expectStatus(rec, http.StatusOK)
	updated := env.object(rec)
	if updated["status"] != "archived" || updated["description"] != "ONG SaaS" {
		t.Fatalf("update lost data: %v", updated)
	}
	if updated["slug"] != "small-ngo" {
		t.Fatalf("update changed the slug: %v", updated["slug"])
	}

	// partial update keeps untouched fields
	rec = env.authed(http.MethodPatch, fmt.Sprintf("/api/v1/projects/%d", id), map[string]any{"status": "active"})
	env.expectStatus(rec, http.StatusOK)
	if body := env.object(rec); body["description"] != "ONG SaaS" || body["tech_stack"] != "go,react" {
		t.Fatalf("patch zeroed fields: %v", body)
	}

	// soft delete
	rec = env.authed(http.MethodDelete, fmt.Sprintf("/api/v1/projects/%d", id), nil)
	env.expectStatus(rec, http.StatusOK)
	if body := env.decode(rec); body["data"].(map[string]any)["deleted"] != "soft" {
		t.Fatalf("expected soft delete: %v", body)
	}
	rec = env.authed(http.MethodGet, fmt.Sprintf("/api/v1/projects/%d", id), nil)
	env.expectStatus(rec, http.StatusNotFound)
	rec = env.authed(http.MethodGet, "/api/v1/projects", nil)
	if len(env.items(rec)) != 0 {
		t.Fatalf("soft-deleted project leaked into the list")
	}
	rec = env.authed(http.MethodGet, fmt.Sprintf("/api/v1/projects/%d?include_deleted=1", id), nil)
	env.expectStatus(rec, http.StatusOK)

	// the audit trail recorded create/update/delete
	rec = env.authed(http.MethodGet, "/api/v1/activity?target=projects/"+fmt.Sprint(id), nil)
	env.expectStatus(rec, http.StatusOK)
	actions := map[string]bool{}
	for _, entry := range env.items(rec) {
		actions[entry.(map[string]any)["action"].(string)] = true
		if entry.(map[string]any)["actor"] != "admin" {
			t.Fatalf("activity actor = %v", entry.(map[string]any)["actor"])
		}
	}
	for _, want := range []string{"create", "update", "delete"} {
		if !actions[want] {
			t.Fatalf("activity_log missing %q (got %v)", want, actions)
		}
	}
}

func TestHardDeleteRequiresAdminAndPaginationSorting(t *testing.T) {
	env := newTestEnv(t)

	for _, name := range []string{"zeta", "alpha", "mike", "bravo", "charlie"} {
		env.seedProject(name)
	}

	rec := env.authed(http.MethodGet, "/api/v1/projects?per_page=2&page=2&sort=name", nil)
	env.expectStatus(rec, http.StatusOK)
	body := env.decode(rec)
	if len(body["data"].([]any)) != 2 {
		t.Fatalf("page size not honoured: %v", body)
	}
	if body["total"].(float64) != 5 || body["total_pages"].(float64) != 3 || body["page"].(float64) != 2 {
		t.Fatalf("pagination metadata wrong: %v", body)
	}
	first := body["data"].([]any)[0].(map[string]any)["name"]
	if first != "charlie" {
		t.Fatalf("sort=name asc, page 2 first row = %v, want charlie", first)
	}

	// search
	rec = env.authed(http.MethodGet, "/api/v1/projects?q=alp", nil)
	if len(env.items(rec)) != 1 {
		t.Fatalf("search failed: %s", rec.Body.String())
	}

	// unknown sort column falls back to the default order instead of exploding
	rec = env.authed(http.MethodGet, "/api/v1/projects?sort=1;DROP+TABLE+projects", nil)
	env.expectStatus(rec, http.StatusOK)

	// a viewer token may not hard delete
	viewerToken, _, err := env.Tokens.Issue("viewer", middleware.RoleViewer)
	if err != nil {
		t.Fatalf("issue viewer token: %v", err)
	}
	id := env.seedProject("temporary")
	rec = env.request(http.MethodDelete, fmt.Sprintf("/api/v1/projects/%d?hard=true", id), nil, viewerToken)
	env.expectStatus(rec, http.StatusForbidden)

	rec = env.authed(http.MethodDelete, fmt.Sprintf("/api/v1/projects/%d?hard=true", id), nil)
	env.expectStatus(rec, http.StatusOK)
	if body := env.decode(rec); body["data"].(map[string]any)["deleted"] != "hard" {
		t.Fatalf("expected hard delete: %v", body)
	}
}

func TestReposAndWorktreesCRUD(t *testing.T) {
	env := newTestEnv(t)
	projectID := env.seedProject("smallcrm-dashboard-v2")

	rec := env.authed(http.MethodPost, "/api/v1/repos", map[string]any{
		"project_id": projectID, "name": "smallcrm-dashboard-v2", "url": "https://github.com/itscwf/smallcrm-dashboard-v2.git", "branch": "dev",
	})
	env.expectStatus(rec, http.StatusCreated)
	repo := env.object(rec)
	if repo["branch"] != "dev" || repo["status"] != "active" {
		t.Fatalf("repo defaults wrong: %v", repo)
	}

	rec = env.authed(http.MethodPost, "/api/v1/repos", map[string]any{"project_id": 9999, "name": "orphan"})
	env.expectStatus(rec, http.StatusUnprocessableEntity)

	rec = env.authed(http.MethodGet, fmt.Sprintf("/api/v1/repos?project_id=%d", projectID), nil)
	if len(env.items(rec)) != 1 {
		t.Fatalf("repo filter by project_id failed: %s", rec.Body.String())
	}

	rec = env.authed(http.MethodPost, "/api/v1/worktrees", map[string]any{
		"project_id": projectID, "name": "wt/tc-123", "branch": "wt/tc-123", "local_path": "/root/projetos/x/.worktrees/tc-123",
	})
	env.expectStatus(rec, http.StatusCreated)
	worktreeID := uint(env.object(rec)["id"].(float64))

	rec = env.authed(http.MethodPut, fmt.Sprintf("/api/v1/worktrees/%d", worktreeID), map[string]any{"last_commit": "e32e3d8"})
	env.expectStatus(rec, http.StatusOK)
	if body := env.object(rec); body["last_commit"] != "e32e3d8" || body["branch"] != "wt/tc-123" {
		t.Fatalf("worktree update wrong: %v", body)
	}

	rec = env.authed(http.MethodDelete, fmt.Sprintf("/api/v1/worktrees/%d", worktreeID), nil)
	env.expectStatus(rec, http.StatusOK)
	rec = env.authed(http.MethodGet, "/api/v1/worktrees", nil)
	if len(env.items(rec)) != 0 {
		t.Fatalf("worktree not soft deleted")
	}
}

func TestECACRUDWithNaturalKey(t *testing.T) {
	env := newTestEnv(t)

	rec := env.authed(http.MethodPost, "/api/v1/ecas", map[string]any{
		"eca_id": "eca-011", "title": "Remoção do módulo Terceiro Setor", "responsible": "coder",
		"dependencies": "ECA-007", "repo": "smallcrm-dashboard-v2", "steps": `["remover toggle","soft-delete tabelas"]`,
	})
	env.expectStatus(rec, http.StatusCreated)
	eca := env.object(rec)
	if eca["eca_id"] != "ECA-011" {
		t.Fatalf("eca_id normalization failed: %v", eca["eca_id"])
	}
	if eca["status"] != "created" {
		t.Fatalf("default status = %v", eca["status"])
	}

	rec = env.authed(http.MethodGet, "/api/v1/ecas/ECA-011", nil)
	env.expectStatus(rec, http.StatusOK)

	rec = env.authed(http.MethodPost, "/api/v1/ecas", map[string]any{"eca_id": "ECA-011", "title": "duplicada"})
	env.expectStatus(rec, http.StatusConflict)

	rec = env.authed(http.MethodPut, "/api/v1/ecas/ECA-011", map[string]any{"status": "in_progress"})
	env.expectStatus(rec, http.StatusOK)
	if body := env.object(rec); body["status"] != "in_progress" || body["title"] != "Remoção do módulo Terceiro Setor" {
		t.Fatalf("eca update wrong: %v", body)
	}

	rec = env.authed(http.MethodGet, "/api/v1/ecas?status=in_progress", nil)
	if len(env.items(rec)) != 1 {
		t.Fatalf("eca filter failed: %s", rec.Body.String())
	}
}

func TestQACycleTestCasesExecutionsAndClose(t *testing.T) {
	env := newTestEnv(t)
	projectID := env.seedProject("small-ngo")
	cycleID := env.seedCycle(projectID, "2a685a2")

	rec := env.authed(http.MethodPost, "/api/v1/qa/cycles", map[string]any{"project_id": projectID, "build": "2a685a2"})
	env.expectStatus(rec, http.StatusCreated)
	if body := env.object(rec); body["ciclo"].(float64) != 2 {
		t.Fatalf("segundo ciclo do projeto deveria ser 2: %v", body["ciclo"])
	}
	rec = env.authed(http.MethodPost, "/api/v1/qa/cycles", map[string]any{"project_id": projectID, "build": "duplicado", "ciclo": 1})
	env.expectStatus(rec, http.StatusConflict)

	passID, failID := 0, 0
	for index, tc := range []map[string]any{
		{"tc_number": "TC-001", "title": "login funciona", "category": "smoke", "cycle_id": cycleID},
		{"tc_number": "TC-002", "title": "checkout falha", "category": "regression", "cycle_id": cycleID},
	} {
		rec = env.authed(http.MethodPost, "/api/v1/qa/testcases", tc)
		env.expectStatus(rec, http.StatusCreated)
		id := int(env.object(rec)["id"].(float64))
		if index == 0 {
			passID = id
		} else {
			failID = id
		}
	}

	rec = env.authed(http.MethodPost, "/api/v1/qa/executions", map[string]any{"tc_id": passID, "result": "pass", "duration_ms": 120})
	env.expectStatus(rec, http.StatusCreated)
	execution := env.object(rec)
	if execution["cycle_id"].(float64) != float64(cycleID) {
		t.Fatalf("execution did not inherit cycle_id: %v", execution)
	}
	if _, hasID := execution["execution_id"].(string); !hasID {
		t.Fatalf("execution_id not generated: %v", execution)
	}
	if execution["executed_at"] == nil {
		t.Fatalf("executed_at not stamped: %v", execution)
	}

	rec = env.authed(http.MethodPost, "/api/v1/qa/executions", map[string]any{"tc_id": failID, "result": "nope"})
	env.expectStatus(rec, http.StatusUnprocessableEntity)

	rec = env.authed(http.MethodPost, "/api/v1/qa/executions", map[string]any{"tc_id": failID, "result": "fail", "output": "500 no checkout"})
	env.expectStatus(rec, http.StatusCreated)

	// the execution result is mirrored onto the test case
	rec = env.authed(http.MethodGet, fmt.Sprintf("/api/v1/qa/testcases/%d", passID), nil)
	if status := env.object(rec)["status"]; status != "PASS" {
		t.Fatalf("testcase status = %v, want PASS", status)
	}

	// close derives "encerrado" from the mixed results (one PASS, one FAIL)
	rec = env.authed(http.MethodPost, fmt.Sprintf("/api/v1/qa/cycles/%d/close", cycleID), nil)
	env.expectStatus(rec, http.StatusOK)
	closed := env.object(rec)
	if closed["status"] != "encerrado" {
		t.Fatalf("closed cycle status = %v, want encerrado", closed["status"])
	}
	if closed["total"].(float64) != 2 || closed["passed"].(float64) != 1 || closed["failed"].(float64) != 1 {
		t.Fatalf("counters not recalculated: %v", closed)
	}
	if closed["finished_at"] == nil {
		t.Fatalf("finished_at not set: %v", closed)
	}

	// explicit status wins
	cycle2 := env.seedCycle(projectID, "build-3")
	rec = env.authed(http.MethodPost, fmt.Sprintf("/api/v1/qa/cycles/%d/close", cycle2), map[string]any{"status": "aborted", "next_tc": "TC-002"})
	env.expectStatus(rec, http.StatusOK)
	if body := env.object(rec); body["status"] != "cancelado" || body["next_tc"] != "TC-002" {
		t.Fatalf("close payload not honoured (alias aborted->cancelado): %v", body)
	}

	rec = env.authed(http.MethodPost, fmt.Sprintf("/api/v1/qa/cycles/%d/close", cycle2), map[string]any{"status": "inventado"})
	env.expectStatus(rec, http.StatusUnprocessableEntity)

	// o frontend envia status "done": precisa virar "concluido"
	cycle3 := env.seedCycle(projectID, "build-4")
	rec = env.authed(http.MethodPost, fmt.Sprintf("/api/v1/qa/cycles/%d/close", cycle3), map[string]any{"status": "done"})
	env.expectStatus(rec, http.StatusOK)
	if body := env.object(rec); body["status"] != "concluido" {
		t.Fatalf("alias done->concluido falhou: %v", body["status"])
	}

	rec = env.authed(http.MethodPost, fmt.Sprintf("/api/v1/qa/cycles/%d/reopen", cycle2), nil)
	env.expectStatus(rec, http.StatusOK)
	if body := env.object(rec); body["status"] != "em_andamento" || body["finished_at"] != nil {
		t.Fatalf("reopen wrong: %v", body)
	}

	// cycle detail expands test cases and executions
	rec = env.authed(http.MethodGet, fmt.Sprintf("/api/v1/qa/cycles/%d", cycleID), nil)
	env.expectStatus(rec, http.StatusOK)
	detail := env.object(rec)
	if len(detail["testcases"].([]any)) != 2 {
		t.Fatalf("cycle detail missing testcases: %v", detail)
	}
	if len(detail["executions"].([]any)) != 2 {
		t.Fatalf("cycle detail missing executions: %v", detail)
	}

	rec = env.authed(http.MethodGet, fmt.Sprintf("/api/v1/qa/executions?cycle_id=%d&result=fail", cycleID), nil)
	if len(env.items(rec)) != 1 {
		t.Fatalf("execution filter failed: %s", rec.Body.String())
	}
}

func TestCronJobsExecutionsAndToggle(t *testing.T) {
	env := newTestEnv(t)

	rec := env.authed(http.MethodPost, "/api/v1/cron/jobs", map[string]any{
		"job_id": "e054c4e9d970", "name": "qa-cycle-smallngo", "schedule": "0 */6 * * *",
		"provider": "deepseek", "model": "deepseek-v4-flash",
		"script_path": "/root/scripts/qa_smallngo.py", "enabled": true, "last_status": "ok",
	})
	env.expectStatus(rec, http.StatusCreated)
	job := env.object(rec)
	if job["enabled"] != true {
		t.Fatalf("enabled = %v", job["enabled"])
	}
	jobID := uint(job["id"].(float64))

	rec = env.authed(http.MethodPost, fmt.Sprintf("/api/v1/cron/jobs/%d/toggle", jobID), nil)
	env.expectStatus(rec, http.StatusOK)
	if body := env.object(rec); body["enabled"] != false {
		t.Fatalf("toggle did not disable the job: %v", body)
	}
	rec = env.authed(http.MethodPost, fmt.Sprintf("/api/v1/cron/jobs/%d/toggle", jobID), nil)
	if body := env.object(rec); body["enabled"] != true {
		t.Fatalf("toggle did not re-enable the job: %v", body)
	}

	rec = env.authed(http.MethodPost, "/api/v1/cron/executions", map[string]any{"job_id": jobID, "exit_code": 1, "error": "boom", "output": "trace"})
	env.expectStatus(rec, http.StatusCreated)
	execution := env.object(rec)
	if execution["status"] != "error" {
		t.Fatalf("derived status = %v, want error", execution["status"])
	}

	rec = env.authed(http.MethodPost, "/api/v1/cron/executions", map[string]any{"job_id": 4242, "exit_code": 0})
	env.expectStatus(rec, http.StatusUnprocessableEntity)

	rec = env.authed(http.MethodGet, "/api/v1/cron/executions?job_id=e054c4e9d970", nil)
	if len(env.items(rec)) != 1 {
		t.Fatalf("cron executions filter by job_id failed: %s", rec.Body.String())
	}
	// o id interno tambem funciona como filtro
	rec = env.authed(http.MethodGet, fmt.Sprintf("/api/v1/cron/executions?job_id=%d", jobID), nil)
	if len(env.items(rec)) != 1 {
		t.Fatalf("cron executions filter by internal id failed: %s", rec.Body.String())
	}

	rec = env.authed(http.MethodGet, "/api/v1/cron/jobs/e054c4e9d970", nil)
	env.expectStatus(rec, http.StatusOK)

	rec = env.authed(http.MethodDelete, fmt.Sprintf("/api/v1/cron/jobs/%d", jobID), nil)
	env.expectStatus(rec, http.StatusOK)
}

func TestAgentsProvidersAndServersCRUD(t *testing.T) {
	env := newTestEnv(t)

	cases := []struct {
		path     string
		create   map[string]any
		update   map[string]any
		lookup   string
		conflict map[string]any
	}{
		{
			path:   "/api/v1/agents",
			create: map[string]any{"name": "coder", "provider": "deepseek", "model": "deepseek-v4-flash", "role": "worker", "status": "online"},
			update: map[string]any{"status": "busy"},
		},
		{
			path:     "/api/v1/providers",
			create:   map[string]any{"name": "deepseek", "endpoint": "https://api.deepseek.com", "api_key_ref": "env:DeepSeek", "limits": `{"rpm":600}`},
			update:   map[string]any{"status": "degraded"},
			lookup:   "/api/v1/providers/deepseek",
			conflict: map[string]any{"name": "deepseek", "endpoint": "https://dupe"},
		},
		{
			path:     "/api/v1/servers",
			create:   map[string]any{"hostname": "docker-dev", "ip": "192.168.0.62", "services": "docker, sqlite", "status": "online"},
			update:   map[string]any{"status": "maintenance"},
			lookup:   "/api/v1/servers/docker-dev",
			conflict: map[string]any{"hostname": "docker-dev"},
		},
	}

	for _, tc := range cases {
		rec := env.authed(http.MethodPost, tc.path, tc.create)
		env.expectStatus(rec, http.StatusCreated)
		id := uint(env.object(rec)["id"].(float64))

		rec = env.authed(http.MethodGet, fmt.Sprintf("%s/%d", tc.path, id), nil)
		env.expectStatus(rec, http.StatusOK)

		rec = env.authed(http.MethodPut, fmt.Sprintf("%s/%d", tc.path, id), tc.update)
		env.expectStatus(rec, http.StatusOK)
		for key, want := range tc.update {
			if got := env.object(rec)[key]; got != want {
				t.Fatalf("%s update %s = %v, want %v", tc.path, key, got, want)
			}
		}

		rec = env.authed(http.MethodGet, tc.path, nil)
		if len(env.items(rec)) != 1 {
			t.Fatalf("%s list size = %d", tc.path, len(env.items(rec)))
		}

		if tc.lookup != "" {
			rec = env.authed(http.MethodGet, tc.lookup, nil)
			env.expectStatus(rec, http.StatusOK)
		}
		if tc.conflict != nil {
			rec = env.authed(http.MethodPost, tc.path, tc.conflict)
			env.expectStatus(rec, http.StatusConflict)
		}

		rec = env.authed(http.MethodDelete, fmt.Sprintf("%s/%d", tc.path, id), nil)
		env.expectStatus(rec, http.StatusOK)
		rec = env.authed(http.MethodGet, tc.path, nil)
		if len(env.items(rec)) != 0 {
			t.Fatalf("%s not soft deleted", tc.path)
		}
	}
}

func TestActivityLogIsReadOnly(t *testing.T) {
	env := newTestEnv(t)
	env.seedProject("ed2ti-website")

	rec := env.authed(http.MethodGet, "/api/v1/activity", nil)
	env.expectStatus(rec, http.StatusOK)
	body := env.decode(rec)
	if body["total"].(float64) < 1 {
		t.Fatalf("activity log empty: %s", rec.Body.String())
	}

	rec = env.authed(http.MethodGet, "/api/v1/activity?action=create&actor=admin", nil)
	env.expectStatus(rec, http.StatusOK)
	if len(env.items(rec)) == 0 {
		t.Fatalf("activity filter returned nothing")
	}

	rec = env.authed(http.MethodPost, "/api/v1/activity", map[string]any{"action": "forged"})
	if rec.Code != http.StatusMethodNotAllowed && rec.Code != http.StatusNotFound {
		t.Fatalf("activity log accepted a write: %d %s", rec.Code, rec.Body.String())
	}
}

func TestStatsEndpoint(t *testing.T) {
	env := newTestEnv(t)
	projectID := env.seedProject("itscwf-website")
	env.seedProject("small-ngo")
	cycleID := env.seedCycle(projectID, "abc123")

	rec := env.authed(http.MethodPost, "/api/v1/qa/testcases", map[string]any{"cycle_id": cycleID, "tc_number": "TC-001", "title": "home carrega"})
	env.expectStatus(rec, http.StatusCreated)
	tcID := uint(env.object(rec)["id"].(float64))
	rec = env.authed(http.MethodPost, "/api/v1/qa/executions", map[string]any{"tc_id": tcID, "result": "pass", "duration_ms": 42})
	env.expectStatus(rec, http.StatusCreated)

	rec = env.authed(http.MethodPost, "/api/v1/ecas", map[string]any{"eca_id": "ECA-015", "title": "fábrica", "status": "in_progress"})
	env.expectStatus(rec, http.StatusCreated)

	rec = env.authed(http.MethodPost, "/api/v1/cron/jobs", map[string]any{"name": "daily-report", "schedule": "0 9 * * *", "enabled": true, "last_status": "error"})
	env.expectStatus(rec, http.StatusCreated)

	rec = env.authed(http.MethodPost, "/api/v1/agents", map[string]any{"name": "coder", "status": "online"})
	env.expectStatus(rec, http.StatusCreated)

	rec = env.authed(http.MethodGet, "/api/v1/stats", nil)
	env.expectStatus(rec, http.StatusOK)
	stats := env.decode(rec)

	if stats["projects"].(map[string]any)["total"].(float64) != 2 {
		t.Fatalf("projects.total = %v", stats["projects"])
	}
	if stats["projects"].(map[string]any)["active"].(float64) != 2 {
		t.Fatalf("projects.active = %v", stats["projects"])
	}
	if stats["ecas"].(map[string]any)["in_progress"].(float64) != 1 {
		t.Fatalf("ecas.in_progress = %v", stats["ecas"])
	}
	if stats["cron_jobs"].(map[string]any)["failing"].(float64) != 1 {
		t.Fatalf("cron_jobs.failing = %v", stats["cron_jobs"])
	}
	if stats["agents"].(map[string]any)["online"].(float64) != 1 {
		t.Fatalf("agents.online = %v", stats["agents"])
	}
	qa := stats["qa"].(map[string]any)
	if qa["test_cases"].(float64) != 1 || qa["executions"].(float64) != 1 || qa["pass_rate"].(float64) != 1 {
		t.Fatalf("qa stats wrong: %v", qa)
	}
	if len(qa["recent_cycles"].([]any)) != 1 {
		t.Fatalf("recent_cycles = %v", qa["recent_cycles"])
	}
	if len(stats["recent_activity"].([]any)) == 0 {
		t.Fatalf("recent_activity empty")
	}
}

func TestUnknownRouteAndMethod(t *testing.T) {
	env := newTestEnv(t)

	rec := env.authed(http.MethodGet, "/api/v1/nope", nil)
	env.expectStatus(rec, http.StatusNotFound)
	if code := env.errorCode(rec); code != "not_found" {
		t.Fatalf("error code = %q", code)
	}

	rec = env.authed(http.MethodPut, "/api/v1/health", nil)
	if rec.Code != http.StatusMethodNotAllowed && rec.Code != http.StatusNotFound {
		t.Fatalf("unexpected status for PUT /health: %d", rec.Code)
	}
}
