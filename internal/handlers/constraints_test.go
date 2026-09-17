package handlers

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gorm.io/gorm"
)

// canonicalDDL le scripts/schema_init.sql (a referencia canonica do banco de
// producao) subindo da pasta do pacote ate a raiz do modulo.
func canonicalDDL(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	for {
		candidate := filepath.Join(dir, "scripts", "schema_init.sql")
		if raw, err := os.ReadFile(candidate); err == nil {
			return string(raw)
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatalf("scripts/schema_init.sql nao encontrado a partir de %s", dir)
		}
		dir = parent
	}
}

// newCanonicalTestEnv sobe o router sobre o schema canonico (com as FKs e as
// restricoes NOT NULL/UNIQUE que o banco de producao tem). O schema gerado
// pelos modelos e mais permissivo; testar so nele esconde erros como o POST
// que grava 0/NULL numa coluna com FOREIGN KEY/NOT NULL.
func newCanonicalTestEnv(t *testing.T) *testEnv {
	t.Helper()
	ddl := canonicalDDL(t)
	return newTestEnvWith(t, func(conn *gorm.DB) error {
		return conn.Exec(ddl).Error
	})
}

// canonicalSchemaApplied confirma que o preparo realmente criou o schema
// canonico — sem isso o teste passaria a toa se o .sql nao fosse aplicado.
func canonicalSchemaApplied(t *testing.T, env *testEnv) {
	t.Helper()
	var sql string
	err := env.DB.Raw("SELECT sql FROM sqlite_master WHERE type = 'table' AND name = 'worktrees'").Scan(&sql).Error
	if err != nil || sql == "" {
		t.Fatalf("tabela worktrees ausente: %v %q", err, sql)
	}
	if !strings.Contains(sql, "REFERENCES repos(id)") {
		t.Fatalf("schema canonico nao aplicado (worktrees sem FK para repos): %s", sql)
	}
}

// TestWorktreeRepositorioOpcionalNoSchemaCanonico cobre o defeito encontrado
// contra o banco real: worktrees.repo_id e anulavel, mas o Insert gravava o
// zero do Go (repo_id = 0) e a FOREIGN KEY derrubava o POST com 409.
func TestWorktreeRepositorioOpcionalNoSchemaCanonico(t *testing.T) {
	env := newCanonicalTestEnv(t)
	canonicalSchemaApplied(t, env)
	projectID := env.seedProject("small-ngo")

	// sem repo_id: deve criar com NULL, nao falhar
	rec := env.authed(http.MethodPost, "/api/v1/worktrees", map[string]any{
		"project_id": projectID, "local_path": "/root/projetos/x/.worktrees/wt-tc-1",
	})
	env.expectStatus(rec, http.StatusCreated)
	created := env.object(rec)
	worktreeID := uint(created["id"].(float64))
	if created["repo_id"] != nil && created["repo_id"].(float64) != 0 {
		t.Fatalf("repo_id deveria vir nulo/ausente: %v", created["repo_id"])
	}
	if created["kind"] != "worktree" || created["status"] != "active" {
		t.Fatalf("defaults do worktree errados: %v", created)
	}

	// updated_at/vinculo nulo nao pode quebrar o PUT (o Save nao pode zerar
	// uma FK valida nem gravar 0 numa coluna nula)
	rec = env.authed(http.MethodPut, "/api/v1/worktrees/"+itoa(int64(worktreeID)), map[string]any{"branch": "wt/tc-1"})
	env.expectStatus(rec, http.StatusOK)
	if body := env.object(rec); body["branch"] != "wt/tc-1" {
		t.Fatalf("branch nao atualizou: %v", body)
	}

	// com repo_id valido: cria vinculado
	rec = env.authed(http.MethodPost, "/api/v1/repos", map[string]any{
		"project_id": projectID, "name": "small-ngo", "url": "https://github.com/itscwf/small-ngo.git",
	})
	env.expectStatus(rec, http.StatusCreated)
	repoID := uint(env.object(rec)["id"].(float64))

	rec = env.authed(http.MethodPost, "/api/v1/worktrees", map[string]any{
		"project_id": projectID, "repo_id": repoID, "local_path": "/root/projetos/x/.worktrees/wt-tc-2",
	})
	env.expectStatus(rec, http.StatusCreated)
	if linked := env.object(rec); linked["repo_id"].(float64) != float64(repoID) {
		t.Fatalf("repo_id nao vinculou: %v", linked["repo_id"])
	}

	// repo_id inexistente: 422 com mensagem util (nao 409 generico)
	rec = env.authed(http.MethodPost, "/api/v1/worktrees", map[string]any{
		"project_id": projectID, "repo_id": 999999, "local_path": "/root/projetos/x/.worktrees/wt-tc-3",
	})
	env.expectStatus(rec, http.StatusUnprocessableEntity)
	if code := env.errorCode(rec); code != "validation_error" {
		t.Fatalf("code = %q, want validation_error (%s)", code, rec.Body.String())
	}

	// zerar o vinculo explicitamente limpa a coluna (NULL) e mantem o PUT 200
	rec = env.authed(http.MethodPut, "/api/v1/worktrees/"+itoa(int64(worktreeID)), map[string]any{"repo_id": 0})
	env.expectStatus(rec, http.StatusOK)
	var stored *int64
	if err := env.DB.Raw("SELECT repo_id FROM worktrees WHERE id = ?", worktreeID).Scan(&stored).Error; err != nil {
		t.Fatalf("select repo_id: %v", err)
	}
	if stored != nil {
		t.Fatalf("repo_id deveria ser NULL depois do clear: %v", *stored)
	}
}

// TestCronJobIdGeradoNoSchemaCanonico cobre o outro defeito: cron_jobs.job_id e
// NOT NULL UNIQUE no banco real, mas o POST sem job_id gravava NULL (a API nao
// gerava a chave natural) e falhava com 409.
func TestCronJobIdGeradoNoSchemaCanonico(t *testing.T) {
	env := newCanonicalTestEnv(t)
	canonicalSchemaApplied(t, env)

	rec := env.authed(http.MethodPost, "/api/v1/cron/jobs", map[string]any{
		"name": "backup-diario", "schedule": "0 3 * * *", "enabled": true,
	})
	env.expectStatus(rec, http.StatusCreated)
	job := env.object(rec)
	jobID, ok := job["job_id"].(string)
	if !ok || len(jobID) != 12 {
		t.Fatalf("job_id deveria ser gerado com 12 caracteres hex: %v", job["job_id"])
	}
	if job["state"] != "scheduled" {
		t.Fatalf("state = %v", job["state"])
	}

	// dois jobs sem job_id nao podem colidir
	rec = env.authed(http.MethodPost, "/api/v1/cron/jobs", map[string]any{"name": "outro-job", "schedule": "0 4 * * *"})
	env.expectStatus(rec, http.StatusCreated)
	if other := env.object(rec); other["job_id"] == jobID {
		t.Fatalf("job_id repetido: %v", other["job_id"])
	}

	// job_id explicito duplicado continua sendo 409
	rec = env.authed(http.MethodPost, "/api/v1/cron/jobs", map[string]any{
		"job_id": jobID, "name": "duplicado", "schedule": "0 5 * * *",
	})
	env.expectStatus(rec, http.StatusConflict)

	// put sem job_id mantem a chave natural (nao zera a coluna NOT NULL)
	jobPK := uint(job["id"].(float64))
	rec = env.authed(http.MethodPut, "/api/v1/cron/jobs/"+itoa(int64(jobPK)), map[string]any{"enabled": false})
	env.expectStatus(rec, http.StatusOK)
	if body := env.object(rec); body["job_id"] != jobID {
		t.Fatalf("job_id foi perdido no update: %v", body["job_id"])
	}
}

// TestSortComAliasJSONNaoQuebra cobre o ORDER BY montado com o nome do JSON:
// as colunas canonicas divergem do nome exposto (local_path→primary_path,
// output→nota, script_path→script, hostname→name, last_seen→last_seen_at) e a
// ordenacao por esses campos devolvia 500 ("no such column").
func TestSortComAliasJSONNaoQuebra(t *testing.T) {
	env := newTestEnv(t)
	projectID := env.seedProject("smallcrm")

	rec := env.authed(http.MethodPost, "/api/v1/repos", map[string]any{
		"project_id": projectID, "name": "smallcrm", "url": "https://github.com/itscwf/smallcrm.git",
	})
	env.expectStatus(rec, http.StatusCreated)

	for _, tc := range []struct {
		path  string
		alias string
		seed  map[string]any
	}{
		{path: "/api/v1/projects", alias: "local_path"},
		{path: "/api/v1/worktrees", alias: "local_path"},
		{path: "/api/v1/cron/jobs", alias: "script_path"},
		{path: "/api/v1/providers", alias: "endpoint"},
		{path: "/api/v1/servers", alias: "hostname"},
		{path: "/api/v1/agents", alias: "last_seen"},
		{path: "/api/v1/qa/executions", alias: "output"},
	} {
		if tc.seed != nil {
			env.expectStatus(env.authed(http.MethodPost, tc.path, tc.seed), http.StatusCreated)
		}
		rec = env.authed(http.MethodGet, tc.path+"?sort="+tc.alias+"&order=asc", nil)
		if rec.Code != http.StatusOK {
			t.Fatalf("sort=%s em %s devolveu %d (body: %s)", tc.alias, tc.path, rec.Code, rec.Body.String())
		}
	}

	// coluna inexistente cai no ordenamento padrao, sem 500
	rec = env.authed(http.MethodGet, "/api/v1/projects?sort=coluna_que_nao_existe", nil)
	env.expectStatus(rec, http.StatusOK)

	// ordenacao continua funcionando de verdade
	for _, project := range []string{"aaa", "bbb", "ccc"} {
		env.expectStatus(env.authed(http.MethodPost, "/api/v1/projects", map[string]any{"name": project}), http.StatusCreated)
	}
	rec = env.authed(http.MethodGet, "/api/v1/projects?sort=name&order=desc", nil)
	env.expectStatus(rec, http.StatusOK)
	items := env.items(rec)
	if len(items) == 0 {
		t.Fatalf("lista vazia: %s", rec.Body.String())
	}
	firstName := items[0].(map[string]any)["name"]
	if firstName != "smallcrm" { // o maior em ordem decrescente
		t.Fatalf("sort=name&order=desc devolveu %v primeiro", firstName)
	}
	lastName := items[len(items)-1].(map[string]any)["name"]
	if lastName != "aaa" {
		t.Fatalf("sort=name&order=desc devolveu %v por ultimo", lastName)
	}
}

// TestQAExecutionPorCodigoDoTC garante o caminho alternativo documentado: criar
// a execucao pelo codigo do caso de teste (tc_code) em vez do id interno.
func TestQAExecutionPorCodigoDoTC(t *testing.T) {
	env := newTestEnv(t)
	projectID := env.seedProject("small-ngo")
	cycleID := env.seedCycle(projectID, "2a685a2")

	rec := env.authed(http.MethodPost, "/api/v1/qa/testcases", map[string]any{
		"cycle_id": cycleID, "tc_number": "TC-007", "title": "logout invalida o access token",
	})
	env.expectStatus(rec, http.StatusCreated)

	rec = env.authed(http.MethodPost, "/api/v1/qa/executions", map[string]any{
		"tc_code": "TC-007", "cycle_id": cycleID, "result": "pass",
	})
	env.expectStatus(rec, http.StatusCreated)
	execution := env.object(rec)
	if execution["result"] != "pass" || execution["tc_code"] != "TC-007" {
		t.Fatalf("execucao por codigo errada: %v", execution)
	}
	if execution["tc_id"] == nil {
		t.Fatalf("tc_id (id interno) nao foi resolvido: %v", execution)
	}

	rec = env.authed(http.MethodPost, "/api/v1/qa/executions", map[string]any{
		"tc_code": "TC-999", "result": "pass",
	})
	env.expectStatus(rec, http.StatusUnprocessableEntity)

	rec = env.authed(http.MethodPost, "/api/v1/qa/executions", map[string]any{"result": "pass"})
	env.expectStatus(rec, http.StatusUnprocessableEntity)
	if message, _ := env.decode(rec)["error"].(map[string]any)["message"].(string); !strings.Contains(message, "tc_code") {
		t.Fatalf("mensagem nao cita as duas formas: %q", message)
	}
}

// TestDuasExecucoesDeCronNoMesmoSegundo cobre o exec_id de cron_executions: o
// id gerado usava so os segundos do inicio, portanto registrar duas execucoes
// do mesmo job no mesmo segundo colidia no UNIQUE(exec_id) e devolvia 409.
func TestDuasExecucoesDeCronNoMesmoSegundo(t *testing.T) {
	env := newTestEnv(t)

	rec := env.authed(http.MethodPost, "/api/v1/cron/jobs", map[string]any{
		"name": "qa-cycle-smallngo", "schedule": "0 */6 * * *", "enabled": true,
	})
	env.expectStatus(rec, http.StatusCreated)
	jobID := env.object(rec)["job_id"].(string)

	seen := map[string]bool{}
	for i := 0; i < 3; i++ {
		rec = env.authed(http.MethodPost, "/api/v1/cron/executions", map[string]any{
			"job_id": jobID, "status": "ok", "exit_code": 0,
		})
		env.expectStatus(rec, http.StatusCreated)
		execID, _ := env.object(rec)["exec_id"].(string)
		if !strings.HasPrefix(execID, "EXEC-"+jobID+"-") {
			t.Fatalf("exec_id fora do formato esperado: %q", execID)
		}
		if seen[execID] {
			t.Fatalf("exec_id repetido na %da execucao: %q", i+1, execID)
		}
		seen[execID] = true
	}
}

// TestConstraintViolationMapeiaPara422 garante que violacao de integridade do
// schema (FK/NOT NULL/CHECK) nao seja reportada como conflito de unicidade.
func TestConstraintViolationMapeiaPara422(t *testing.T) {
	env := newTestEnv(t)

	env.expectStatus(env.authed(http.MethodPost, "/api/v1/projects", map[string]any{"name": "dup"}), http.StatusCreated)
	rec := env.authed(http.MethodPost, "/api/v1/projects", map[string]any{"name": "dup"})
	env.expectStatus(rec, http.StatusConflict)
	if code := env.errorCode(rec); code != "conflict" {
		t.Fatalf("slug duplicado deveria ser conflict: %v", rec.Body.String())
	}

	rec = env.authed(http.MethodPost, "/api/v1/cron/jobs", map[string]any{"job_id": "e054c4e9d970", "name": "job-a", "schedule": "0 1 * * *"})
	env.expectStatus(rec, http.StatusCreated)
	rec = env.authed(http.MethodPost, "/api/v1/cron/jobs", map[string]any{"job_id": "e054c4e9d970", "name": "job-b", "schedule": "0 2 * * *"})
	env.expectStatus(rec, http.StatusConflict)
}
