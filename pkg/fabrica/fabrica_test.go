package fabrica

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const testSecret = "0123456789abcdef0123456789abcdef"

func TestTokenManagerIssueAndParse(t *testing.T) {
	manager, err := NewTokenManager(testSecret, "itscwf-fabrica-test", time.Hour)
	if err != nil {
		t.Fatalf("new manager: %v", err)
	}

	token, expires, err := manager.Issue("admin", "admin")
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	if token == "" || expires.Before(time.Now()) {
		t.Fatalf("bad token/expiry: %q %v", token, expires)
	}

	claims, err := manager.Parse(token)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if claims.Username != "admin" || claims.Role != "admin" || claims.Subject != "admin" {
		t.Fatalf("claims = %+v", claims)
	}
	if claims.ID == "" {
		t.Fatalf("missing jti")
	}
}

func TestTokenManagerRejectsBadTokens(t *testing.T) {
	manager, err := NewTokenManager(testSecret, "itscwf-fabrica-test", time.Hour)
	if err != nil {
		t.Fatalf("new manager: %v", err)
	}

	if _, err := manager.Parse("garbage"); err == nil {
		t.Fatalf("garbage token accepted")
	}

	other, err := NewTokenManager("ffffffffffffffffffffffffffffffff", "itscwf-fabrica-test", time.Hour)
	if err != nil {
		t.Fatalf("other manager: %v", err)
	}
	foreign, _, err := other.Issue("intruder", "admin")
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	if _, err := manager.Parse(foreign); err == nil {
		t.Fatalf("token signed with another secret accepted")
	}

	wrongIssuer, err := NewTokenManager(testSecret, "another-issuer", time.Hour)
	if err != nil {
		t.Fatalf("wrong issuer manager: %v", err)
	}
	misissued, _, err := wrongIssuer.Issue("admin", "admin")
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	if _, err := manager.Parse(misissued); err == nil {
		t.Fatalf("token from another issuer accepted")
	}
}

func TestTokenManagerExpired(t *testing.T) {
	manager, err := NewTokenManager(testSecret, "itscwf-fabrica-test", -time.Hour)
	if err != nil {
		t.Fatalf("new manager: %v", err)
	}
	token, _, err := manager.Issue("admin", "admin")
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	if _, err := manager.Parse(token); err == nil {
		t.Fatalf("expired token accepted")
	}
}

func TestNewTokenManagerValidatesSecret(t *testing.T) {
	if _, err := NewTokenManager("short", "issuer", time.Hour); err == nil {
		t.Fatalf("short secret accepted")
	}
	manager, err := NewTokenManager(testSecret, "", 0)
	if err != nil {
		t.Fatalf("new manager: %v", err)
	}
	if manager.TTL() != 12*time.Hour {
		t.Fatalf("default ttl = %v", manager.TTL())
	}
}

func TestSlugify(t *testing.T) {
	cases := map[string]string{
		"Small NGO":             "small-ngo",
		"smallcrm-dashboard-v2": "smallcrm-dashboard-v2",
		"  ITSCWF   Fábrica  ":  "itscwf-fabrica",
		"ed2ti_website_new":     "ed2ti-website-new",
		"fyigo-api":             "fyigo-api",
		"ECAs & Fábrica!!!":     "ecas-fabrica",
		"Fábrica de Software":   "fabrica-de-software",
		"---already-slug---":    "already-slug",
		"":                      "",
	}
	for input, want := range cases {
		if got := Slugify(input); got != want {
			t.Fatalf("Slugify(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestBuildEnvScrubsAgentContext(t *testing.T) {
	t.Setenv("HERMES_DELEGATED_CHILD_CONTEXT", "1")
	t.Setenv("HERMES_KANBAN_TASK", "t_e8293160")
	t.Setenv("HERMES_KANBAN_DB", "/root/.hermes/kanban/boards/other/kanban.db")
	t.Setenv("HERMES_KANBAN_WORKSPACE", "/tmp/ws")
	t.Setenv("HERMES_PROFILE", "coder")
	t.Setenv("PATH", "/usr/bin")

	env := BuildEnv(map[string]string{"HERMES_KANBAN_BOARD": "itscwf_fabrica"})

	joined := strings.Join(env, "\n")
	for _, forbidden := range []string{
		"HERMES_DELEGATED_CHILD_CONTEXT=",
		"HERMES_KANBAN_TASK=",
		"HERMES_KANBAN_DB=",
		"HERMES_KANBAN_WORKSPACE=",
	} {
		if strings.Contains(joined, forbidden) {
			t.Fatalf("environment still contains %s:\n%s", forbidden, joined)
		}
	}
	if !strings.Contains(joined, "HERMES_KANBAN_BOARD=itscwf_fabrica") {
		t.Fatalf("board override missing:\n%s", joined)
	}
	if !strings.Contains(joined, "HERMES_PROFILE=coder") || !strings.Contains(joined, "PATH=/usr/bin") {
		t.Fatalf("unrelated variables were dropped:\n%s", joined)
	}
}

func TestExecRunnerRunsRealProcessAndSanitisesEnv(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "fake-hermes.sh")
	content := "#!/bin/sh\necho \"$@\"\necho \"delegated=${HERMES_DELEGATED_CHILD_CONTEXT:-unset}\"\necho \"board=${HERMES_KANBAN_BOARD:-unset}\"\n"
	if err := os.WriteFile(script, []byte(content), 0o755); err != nil {
		t.Fatalf("write script: %v", err)
	}
	t.Setenv("HERMES_DELEGATED_CHILD_CONTEXT", "1")

	runner := &ExecRunner{Bin: script, WorkDir: dir, Timeout: 10 * time.Second}
	result := runner.Run(context.Background(), "hermes", []string{"kanban", "list", "--json"}, map[string]string{"HERMES_KANBAN_BOARD": "itscwf_fabrica"})
	if !result.IsOK() {
		t.Fatalf("run failed: %+v", result)
	}
	if !strings.Contains(result.Stdout, "kanban list --json") {
		t.Fatalf("args not forwarded: %q", result.Stdout)
	}
	if !strings.Contains(result.Stdout, "delegated=unset") {
		t.Fatalf("HERMES_DELEGATED_CHILD_CONTEXT was not scrubbed: %q", result.Stdout)
	}
	if !strings.Contains(result.Stdout, "board=itscwf_fabrica") {
		t.Fatalf("board override missing: %q", result.Stdout)
	}
	if result.DurationMS < 0 {
		t.Fatalf("duration not measured: %+v", result)
	}
}

func TestExecRunnerReportsFailure(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "fail.sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\necho boom >&2\nexit 3\n"), 0o755); err != nil {
		t.Fatalf("write script: %v", err)
	}
	runner := &ExecRunner{Bin: script, WorkDir: dir, Timeout: 5 * time.Second}
	result := runner.Run(context.Background(), "hermes", nil, nil)
	if result.IsOK() {
		t.Fatalf("expected failure: %+v", result)
	}
	if result.ExitCode != 3 {
		t.Fatalf("exit code = %d, want 3 (%+v)", result.ExitCode, result)
	}
	if !strings.Contains(result.Stderr, "boom") {
		t.Fatalf("stderr not captured: %+v", result)
	}
}

func TestDecodeJSONToleratesBanner(t *testing.T) {
	var payload []map[string]any
	if err := decodeJSON("warning: something\n[{\"id\":\"t_1\"}]\ntrailing noise", &payload); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(payload) != 1 || payload[0]["id"] != "t_1" {
		t.Fatalf("payload = %v", payload)
	}
	if err := decodeJSON("no json here", &payload); err == nil {
		t.Fatalf("expected an error for non-JSON output")
	}
	if err := decodeJSON("", &payload); err == nil {
		t.Fatalf("expected an error for empty output")
	}
}

// stubRunner records calls and returns canned results.
type stubRunner struct {
	results []CommandResult
	calls   []string
}

func (s *stubRunner) Run(_ context.Context, name string, args []string, env map[string]string) CommandResult {
	s.calls = append(s.calls, name+" "+strings.Join(args, " "))
	if len(s.results) == 0 {
		return CommandResult{Command: name, Args: args}
	}
	next := s.results[0]
	s.results = s.results[1:]
	return next
}

func TestHermesClientKanbanTasks(t *testing.T) {
	runner := &stubRunner{results: []CommandResult{{ExitCode: 0, Stdout: `[{"id":"t_1","status":"running"}]`}}}
	client := &HermesClient{Runner: runner, DefaultBoard: "itscwf_fabrica"}

	tasks, result, err := client.KanbanTasks(context.Background(), "", "", "")
	if err != nil {
		t.Fatalf("kanban tasks: %v", err)
	}
	if len(tasks) != 1 || tasks[0]["status"] != "running" {
		t.Fatalf("tasks = %v", tasks)
	}
	if !result.IsOK() {
		t.Fatalf("result = %+v", result)
	}
	if runner.calls[0] != "hermes kanban list --json" {
		t.Fatalf("call = %q", runner.calls[0])
	}
}

func TestHermesClientKanbanTasksFailure(t *testing.T) {
	runner := &stubRunner{results: []CommandResult{{ExitCode: 1, Stderr: "boom"}}}
	client := &HermesClient{Runner: runner}

	if _, result, err := client.KanbanTasks(context.Background(), "b", "", ""); err == nil {
		t.Fatalf("expected an error, got %+v", result)
	}
}

func TestHermesClientAgentsFallback(t *testing.T) {
	runner := &stubRunner{results: []CommandResult{
		{ExitCode: 2, Stderr: "unrecognized arguments: --json"},
		{ExitCode: 0, Stdout: "Hermes Agent Status\n  Model: deepseek\n"},
	}}
	client := &HermesClient{Runner: runner}

	parsed, raw, result, fallback, err := client.Agents(context.Background())
	if err != nil {
		t.Fatalf("agents: %v", err)
	}
	if parsed != nil || !fallback || !result.IsOK() {
		t.Fatalf("parsed=%v fallback=%v result=%+v", parsed, fallback, result)
	}
	if !strings.Contains(raw, "Mock") && !strings.Contains(raw, "Model: deepseek") {
		t.Fatalf("raw = %q", raw)
	}
	if len(runner.calls) != 2 || runner.calls[0] != "hermes status --json" || runner.calls[1] != "hermes status" {
		t.Fatalf("calls = %v", runner.calls)
	}
}

func TestHermesClientCronJobs(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "jobs.json")
	payload, err := json.Marshal(map[string]any{"jobs": []map[string]any{{"name": "daily-report", "enabled": true}}})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := os.WriteFile(path, payload, 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	client := &HermesClient{CronJobsPath: path}
	jobs, source, err := client.CronJobs()
	if err != nil {
		t.Fatalf("cron jobs: %v", err)
	}
	if len(jobs) != 1 || jobs[0]["name"] != "daily-report" {
		t.Fatalf("jobs = %v", jobs)
	}
	if source != path {
		t.Fatalf("source = %q", source)
	}

	broken := &HermesClient{CronJobsPath: filepath.Join(dir, "nope.json")}
	if _, _, err := broken.CronJobs(); err == nil {
		t.Fatalf("expected an error for a missing jobs file")
	}

	invalid := filepath.Join(dir, "invalid.json")
	if err := os.WriteFile(invalid, []byte("not json"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, _, err := (&HermesClient{CronJobsPath: invalid}).CronJobs(); err == nil {
		t.Fatalf("expected an error for invalid JSON")
	}
}

func TestTTLCache(t *testing.T) {
	cache := NewTTLCache(50 * time.Millisecond)
	if _, found := cache.Get("missing"); found {
		t.Fatalf("empty cache returned a value")
	}
	cache.Set("key", "value")
	if value, found := cache.Get("key"); !found || value != "value" {
		t.Fatalf("get = %v %v", value, found)
	}
	time.Sleep(70 * time.Millisecond)
	if _, found := cache.Get("key"); found {
		t.Fatalf("expired entry still returned")
	}

	disabled := NewTTLCache(0)
	disabled.Set("key", "value")
	if _, found := disabled.Get("key"); found {
		t.Fatalf("disabled cache returned a value")
	}

	var nilCache *TTLCache
	nilCache.Set("key", "value")
	if _, found := nilCache.Get("key"); found {
		t.Fatalf("nil cache returned a value")
	}
}
