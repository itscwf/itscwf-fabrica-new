package handlers

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/itscwf/itscwf-fabrica-new/pkg/fabrica"
)

const kanbanFixture = `[
  {
    "id": "t_e8293160",
    "title": "T3: API REST — CRUD completo",
    "assignee": "coder",
    "status": "running",
    "priority": 0
  },
  {
    "id": "t_b9443393",
    "title": "T5: Migração",
    "assignee": "coder",
    "status": "blocked",
    "priority": 0
  }
]`

func TestHermesTasksEndpoint(t *testing.T) {
	env := newTestEnv(t)
	env.Runner.handler = func(name string, args []string, env map[string]string) fabrica.CommandResult {
		return fabrica.CommandResult{Command: name, Args: args, ExitCode: 0, Stdout: kanbanFixture}
	}

	rec := env.authed(http.MethodGet, "/api/v1/hermes/tasks", nil)
	env.expectStatus(rec, http.StatusOK)
	body := env.decode(rec)

	if body["count"].(float64) != 2 {
		t.Fatalf("count = %v", body["count"])
	}
	if body["board"] != "itscwf_fabrica" {
		t.Fatalf("board = %v", body["board"])
	}
	byStatus := body["by_status"].(map[string]any)
	if byStatus["running"].(float64) != 1 || byStatus["blocked"].(float64) != 1 {
		t.Fatalf("by_status = %v", byStatus)
	}
	command := body["command"].(map[string]any)
	if command["exit_code"].(float64) != 0 {
		t.Fatalf("command exit_code = %v", command["exit_code"])
	}
	args, _ := command["args"].([]any)
	joined := make([]string, 0, len(args))
	for _, arg := range args {
		joined = append(joined, arg.(string))
	}
	if strings.Join(joined, " ") != "kanban list --json" {
		t.Fatalf("args = %v", joined)
	}

	call, ok := env.Runner.lastCall()
	if !ok {
		t.Fatalf("runner was not called")
	}
	if call.Name != "hermes" {
		t.Fatalf("runner command = %q", call.Name)
	}
	if call.Env["HERMES_KANBAN_BOARD"] != "itscwf_fabrica" {
		t.Fatalf("board env not passed: %v", call.Env)
	}

	// ?board= and ?status= are forwarded
	rec = env.authed(http.MethodGet, "/api/v1/hermes/tasks?board=outro_board&status=blocked&assignee=coder", nil)
	env.expectStatus(rec, http.StatusOK)
	call, _ = env.Runner.lastCall()
	if call.Env["HERMES_KANBAN_BOARD"] != "outro_board" {
		t.Fatalf("board override not honoured: %v", call.Env)
	}
	if !contains(call.Args, "--status") || !contains(call.Args, "blocked") || !contains(call.Args, "--assignee") {
		t.Fatalf("status/assignee not forwarded: %v", call.Args)
	}
}

func TestHermesTasksFailureIsBadGateway(t *testing.T) {
	env := newTestEnv(t)
	env.Runner.handler = func(name string, args []string, env map[string]string) fabrica.CommandResult {
		return fabrica.CommandResult{Command: name, Args: args, ExitCode: 1, Stderr: "kanban: delegate_task child contexts cannot mutate Kanban tasks"}
	}

	rec := env.authed(http.MethodGet, "/api/v1/hermes/tasks", nil)
	env.expectStatus(rec, http.StatusBadGateway)
	body := env.decode(rec)
	if body["error"].(map[string]any)["code"] != "hermes_unavailable" {
		t.Fatalf("error payload = %v", body)
	}
	if !strings.Contains(body["command"].(map[string]any)["stderr"].(string), "delegate_task") {
		t.Fatalf("stderr not surfaced: %v", body["command"])
	}
}

func TestHermesTasksCaching(t *testing.T) {
	env := newTestEnv(t)
	env.Cfg.Hermes.CacheTTLSecs = 60
	// Rebuild the router so the cache picks up the TTL.
	env = rebuildWithRouter(env)

	calls := 0
	env.Runner.handler = func(name string, args []string, env map[string]string) fabrica.CommandResult {
		calls++
		return fabrica.CommandResult{Command: name, Args: args, ExitCode: 0, Stdout: kanbanFixture}
	}

	rec := env.authed(http.MethodGet, "/api/v1/hermes/tasks", nil)
	env.expectStatus(rec, http.StatusOK)
	if cached, _ := env.decode(rec)["cached"].(bool); cached {
		t.Fatalf("first call must not be cached")
	}

	rec = env.authed(http.MethodGet, "/api/v1/hermes/tasks", nil)
	env.expectStatus(rec, http.StatusOK)
	if cached, _ := env.decode(rec)["cached"].(bool); !cached {
		t.Fatalf("second call should be served from cache")
	}
	if calls != 1 {
		t.Fatalf("runner calls = %d, want 1", calls)
	}

	rec = env.authed(http.MethodGet, "/api/v1/hermes/tasks?refresh=1", nil)
	env.expectStatus(rec, http.StatusOK)
	if calls != 2 {
		t.Fatalf("refresh did not bypass the cache (calls=%d)", calls)
	}
}

func TestHermesAgentsFallsBackToPlainText(t *testing.T) {
	env := newTestEnv(t)
	env.Runner.handler = func(name string, args []string, env map[string]string) fabrica.CommandResult {
		if contains(args, "--json") {
			return fabrica.CommandResult{Command: name, Args: args, ExitCode: 2, Stderr: "unrecognized arguments: --json"}
		}
		return fabrica.CommandResult{Command: name, Args: args, ExitCode: 0, Stdout: hermesStatusFixture}
	}

	rec := env.authed(http.MethodGet, "/api/v1/hermes/agents", nil)
	env.expectStatus(rec, http.StatusOK)
	body := env.decode(rec)
	if body["fallback_used"] != true {
		t.Fatalf("fallback_used = %v", body["fallback_used"])
	}
	if body["parsed"] != false {
		t.Fatalf("parsed = %v", body["parsed"])
	}
	if !strings.Contains(body["raw"].(string), "Hermes Agent Status") {
		t.Fatalf("raw output missing: %v", body["raw"])
	}
	summary := body["summary"].(map[string]any)
	if summary["model"] != "deepseek-v4-flash" || summary["provider"] != "DeepSeek" || summary["project"] != "/usr/local/lib/hermes-agent" {
		t.Fatalf("summary = %v", summary)
	}
}

func TestHermesAgentsPrefersJSON(t *testing.T) {
	env := newTestEnv(t)
	env.Runner.handler = func(name string, args []string, env map[string]string) fabrica.CommandResult {
		return fabrica.CommandResult{Command: name, Args: args, ExitCode: 0, Stdout: `{"model":"deepseek-v4-flash","provider":"deepseek","agents":[{"name":"coder"}]}`}
	}

	rec := env.authed(http.MethodGet, "/api/v1/hermes/agents", nil)
	env.expectStatus(rec, http.StatusOK)
	body := env.decode(rec)
	if body["parsed"] != true || body["fallback_used"] != false {
		t.Fatalf("unexpected flags: %v", body)
	}
	data := body["data"].(map[string]any)
	if data["model"] != "deepseek-v4-flash" {
		t.Fatalf("data = %v", data)
	}
}

func TestHermesCronsReadsJobsFile(t *testing.T) {
	env := newTestEnv(t)
	jobs := map[string]any{
		"jobs": []map[string]any{
			{"id": "e054c4e9d970", "name": "sprint-controller", "enabled": false, "last_status": "ok", "schedule_display": "*/10 * * * *"},
			{"id": "ddc1", "name": "qa-cycle-smallngo", "enabled": true, "last_status": "error", "schedule_display": "0 */6 * * *"},
			{"id": "ddc2", "name": "daily-report", "enabled": true, "last_status": "ok", "schedule_display": "0 9 * * *"},
		},
	}
	raw, err := json.Marshal(jobs)
	if err != nil {
		t.Fatalf("marshal fixture: %v", err)
	}
	path := filepath.Join(env.Dir, "jobs.json")
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	rec := env.authed(http.MethodGet, "/api/v1/hermes/crons", nil)
	env.expectStatus(rec, http.StatusOK)
	body := env.decode(rec)
	if body["count"].(float64) != 3 {
		t.Fatalf("count = %v", body["count"])
	}
	if body["enabled_count"].(float64) != 2 || body["failing_count"].(float64) != 1 {
		t.Fatalf("counters wrong: enabled=%v failing=%v", body["enabled_count"], body["failing_count"])
	}
	if body["source"] != path {
		t.Fatalf("source = %v, want %v", body["source"], path)
	}

	rec = env.authed(http.MethodGet, "/api/v1/hermes/crons?enabled=true&limit=1", nil)
	env.expectStatus(rec, http.StatusOK)
	body = env.decode(rec)
	if body["total"].(float64) != 2 || body["count"].(float64) != 1 {
		t.Fatalf("filter/limit wrong: total=%v count=%v", body["total"], body["count"])
	}
	first := body["data"].([]any)[0].(map[string]any)
	if first["name"] != "daily-report" {
		t.Fatalf("unexpected first job: %v", first)
	}

	rec = env.authed(http.MethodGet, "/api/v1/hermes/crons?status=error", nil)
	env.expectStatus(rec, http.StatusOK)
	if env.decode(rec)["count"].(float64) != 1 {
		t.Fatalf("status filter failed")
	}
}

func TestHermesCronsMissingFileFallsBackToBridge(t *testing.T) {
	env := newTestEnv(t)
	env.Cfg.Hermes.CronJobsPath = filepath.Join(env.Dir, "does-not-exist.json")
	env.Runner.handler = func(name string, args []string, env map[string]string) fabrica.CommandResult {
		if len(args) >= 2 && args[0] == "cron" && args[1] == "list" {
			return fabrica.CommandResult{Command: name, Args: args, ExitCode: 0, Stdout: cronsTUIFixture}
		}
		return fabrica.CommandResult{Command: name, Args: args, ExitCode: 1}
	}

	rec := env.authed(http.MethodGet, "/api/v1/hermes/crons", nil)
	env.expectStatus(rec, http.StatusOK)
	body := env.decode(rec)
	if body["count"].(float64) != 2 {
		t.Fatalf("count = %v (queria 2 jobs da ponte kanban)", body["count"])
	}
	first := body["data"].([]any)[0].(map[string]any)
	if first["name"] != "bbb-relay" || first["schedule"] != "*/10 * * * *" {
		t.Fatalf("primeiro job inesperado: %v", first)
	}
	if body["source"] != "itscwf_fabrica (hermes-kanban-api)" {
		t.Fatalf("source = %v, queria a ponte kanban-api", body["source"])
	}
}

// cronsTUIFixture mimics the `hermes cron list` box-drawing output that the
// kanban bridge returns (asterisks must survive the parser).
const cronsTUIFixture = `
┌──────────────────────────────────────────┐
│              Scheduled Jobs              │
└──────────────────────────────────────────┘

  aaa111bbb222 [active]
    Name:      bbb-relay
    Schedule:  */10 * * * *
    Last run:  2026-09-16T09:00:00-04:00  ok

  ccc333ddd444 [active]
    Name:      zulu-report
    Schedule:  0 6 * * 1-5
`

const hermesStatusFixture = `┌─────────────────────────────────────────────────────────┐
│                 ⚕ Hermes Agent Status                  │
└─────────────────────────────────────────────────────────┘

◆ Environment
  Project:      /usr/local/lib/hermes-agent
  Python:       3.11.15
  Model:        deepseek-v4-flash
  Provider:     DeepSeek

◆ API Keys
  OpenRouter    ✗ (not set)
  DeepSeek      ✓ sk-e...1d40
`

func contains(values []string, needle string) bool {
	for _, value := range values {
		if value == needle {
			return true
		}
	}
	return false
}

// rebuildWithRouter recreates the router so a config change (cache TTL) takes
// effect. The database, token and fake runner are preserved.
func rebuildWithRouter(env *testEnv) *testEnv {
	client := &fabrica.HermesClient{
		Runner:       env.Runner,
		CronJobsPath: env.Cfg.Hermes.CronJobsPath,
		DefaultBoard: env.Cfg.Hermes.KanbanBoard,
	}
	env.Router = NewRouter(Deps{
		Config:   env.Cfg,
		DB:       env.DB,
		Hermes:   client,
		Kanban:   env.Runner,
		Auth:     env.AuthMW,
		Recorder: NewRecorder(env.DB),
	})
	return env
}
