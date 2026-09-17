package fabrica

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
	"unicode"
)

// CommandResult is a serialisable report of one subprocess invocation.
type CommandResult struct {
	Command    string   `json:"command"`
	Args       []string `json:"args"`
	ExitCode   int      `json:"exit_code"`
	DurationMS int64    `json:"duration_ms"`
	Stdout     string   `json:"stdout,omitempty"`
	Stderr     string   `json:"stderr,omitempty"`
	Error      string   `json:"error,omitempty"`
	OK         bool     `json:"ok"`
}

func (r CommandResult) String() string {
	return strings.TrimSpace(r.Command + " " + strings.Join(r.Args, " "))
}

func (r CommandResult) IsOK() bool { return r.ExitCode == 0 && r.Error == "" }

// KanbanService defines how the Fabrica backend talks to Hermes Kanban.
// Two implementations exist:
//   - ExecRunner: spawns the `hermes` CLI directly (local development)
//   - HTTPClient: calls the hermes-kanban-api REST server (Docker deployment)
type KanbanService interface {
	// KanbanBoards returns all boards.
	KanbanBoards(ctx context.Context) ([]map[string]any, error)
	// KanbanTasks returns tasks from a board, optionally filtered.
	KanbanTasks(ctx context.Context, board, status, assignee string) ([]map[string]any, error)
	// KanbanTask returns a single task.
	KanbanTask(ctx context.Context, taskID, board string) (map[string]any, error)
	// KanbanRuns returns run history for a task.
	KanbanRuns(ctx context.Context, taskID string) ([]map[string]any, error)
	// Agents returns Hermes agent status (map + raw string + result).
	Agents(ctx context.Context) (map[string]any, string, CommandResult, bool, error)
	// CronJobs returns the list of cron jobs (parsed + raw string).
	CronJobs() ([]map[string]any, string, error)
	// CreateTask creates a new kanban task.
	CreateTask(ctx context.Context, req KanbanCreateTaskRequest) (map[string]any, error)
}

// KanbanCreateTaskRequest encapsulates the fields needed to create a task.
type KanbanCreateTaskRequest struct {
	Title     string
	Body      string
	Assignee  string
	Project   string
	Workspace string
	Priority  int
}

// --- ExecRunner: CLI-based implementation (kept for backward compat + tests) ---

// ExecRunner implements KanbanService by spawning the Hermes CLI.
type ExecRunner struct {
	Bin     string
	WorkDir string
	Timeout time.Duration
}

var _ KanbanService = (*ExecRunner)(nil)

func NewExecRunner(bin, workdir string, timeout time.Duration) *ExecRunner {
	if bin == "" {
		bin = "hermes"
	}
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	return &ExecRunner{Bin: bin, WorkDir: workdir, Timeout: timeout}
}

func (r *ExecRunner) Run(ctx context.Context, name string, args []string, extraEnv map[string]string) CommandResult {
	bin := r.Bin
	if bin == "" {
		bin = name
	}
	runCtx, cancel := context.WithTimeout(ctx, r.Timeout)
	defer cancel()
	cmd := exec.CommandContext(runCtx, bin, args...)
	if r.WorkDir != "" {
		cmd.Dir = r.WorkDir
	}
	cmd.Env = BuildEnv(extraEnv)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	started := time.Now()
	err := cmd.Run()
	result := CommandResult{
		Command:    bin,
		Args:       args,
		DurationMS: time.Since(started).Milliseconds(),
		Stdout:     stdout.String(),
		Stderr:     stderr.String(),
	}
	if exitErr, ok := err.(*exec.ExitError); ok {
		result.ExitCode = exitErr.ExitCode()
		result.OK = result.ExitCode == 0
	} else if err != nil {
		result.Error = err.Error()
		result.ExitCode = -1
	} else {
		result.ExitCode = 0
		result.OK = true
	}
	return result
}

var blockedEnv = []string{
	"HERMES_DELEGATED_CHILD_CONTEXT",
	"HERMES_KANBAN_TASK",
	"HERMES_KANBAN_DB",
	"HERMES_KANBAN_WORKSPACE",
	"HERMES_KANBAN_WORKSPACES_ROOT",
	"HERMES_KANBAN_BRANCH",
	"HERMES_KANBAN_RUN_ID",
	"HERMES_CRON_SESSION",
	"HERMES_SINGLE_QUERY_SESSION",
	"HERMES_INTERACTIVE",
	"HERMES_EXEC_ASK",
	"HERMES_QUIET",
	"HERMES_AGENT",
	"AI_AGENT",
}

func BuildEnv(extra map[string]string) []string {
	skip := make(map[string]bool, len(blockedEnv)+len(extra))
	for _, k := range blockedEnv {
		skip[k] = true
	}
	for k := range extra {
		skip[k] = true
	}
	var env []string
	for _, kv := range os.Environ() {
		if idx := strings.IndexByte(kv, '='); idx > 0 {
			if skip[kv[:idx]] {
				continue
			}
		}
		env = append(env, kv)
	}
	for k, v := range extra {
		env = append(env, k+"="+v)
	}
	return env
}

func (r *ExecRunner) KanbanBoards(ctx context.Context) ([]map[string]any, error) {
	result := r.Run(ctx, "hermes", []string{"kanban", "boards", "list", "--json"}, nil)
	if !result.IsOK() {
		return nil, errors.New(result.Stderr)
	}
	var boards []map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(result.Stdout)), &boards); err != nil {
		return nil, fmt.Errorf("parse boards JSON: %w", err)
	}
	return boards, nil
}

func (r *ExecRunner) KanbanTasks(ctx context.Context, board, status, assignee string) ([]map[string]any, error) {
	args := []string{"kanban", "list", "--json"}
	extraEnv := map[string]string{}
	if board != "" {
		extraEnv["HERMES_KANBAN_BOARD"] = board
	}
	if status != "" {
		args = append(args, "--status", status)
	}
	if assignee != "" {
		args = append(args, "--assignee", assignee)
	}
	result := r.Run(ctx, "hermes", args, extraEnv)
	if !result.IsOK() {
		return nil, errors.New(result.Stderr)
	}
	var tasks []map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(result.Stdout)), &tasks); err != nil {
		return nil, fmt.Errorf("parse tasks JSON: %w", err)
	}
	return tasks, nil
}

func (r *ExecRunner) KanbanTask(ctx context.Context, taskID, board string) (map[string]any, error) {
	args := []string{"kanban", "show", "--json", taskID}
	extraEnv := map[string]string{}
	if board != "" {
		extraEnv["HERMES_KANBAN_BOARD"] = board
	}
	result := r.Run(ctx, "hermes", args, extraEnv)
	if !result.IsOK() {
		return nil, errors.New(result.Stderr)
	}
	var payload struct {
		Task map[string]any `json:"task"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(result.Stdout)), &payload); err != nil {
		return nil, fmt.Errorf("parse task JSON: %w", err)
	}
	return payload.Task, nil
}

func (r *ExecRunner) KanbanRuns(ctx context.Context, taskID string) ([]map[string]any, error) {
	result := r.Run(ctx, "hermes", []string{"kanban", "runs", taskID, "--json"}, nil)
	if !result.IsOK() {
		return nil, errors.New(result.Stderr)
	}
	var runs []map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(result.Stdout)), &runs); err != nil {
		return nil, fmt.Errorf("parse runs JSON: %w", err)
	}
	return runs, nil
}

// Agents runs `hermes status` and returns structured + raw output.
func (r *ExecRunner) Agents(ctx context.Context) (map[string]any, string, CommandResult, bool, error) {
	result := r.Run(ctx, "hermes", []string{"status"}, nil)
	return map[string]any{"raw": result.Stdout}, result.Stdout, result, false, nil
}

// CronJobs runs `hermes cron list` and parses the output.
func (r *ExecRunner) CronJobs() ([]map[string]any, string, error) {
	result := r.Run(context.Background(), "hermes", []string{"cron", "list"}, nil)
	// Use the parser from hermes_http.go (same package)
	jobs, err := parseCronList(result.Stdout)
	return jobs, result.Stdout, err
}

// CreateTask creates a new kanban task via the hermes CLI.
func (r *ExecRunner) CreateTask(ctx context.Context, req KanbanCreateTaskRequest) (map[string]any, error) {
	args := []string{"kanban", "create", req.Title, "--json"}
	extraEnv := map[string]string{}
	if req.Body != "" {
		args = append(args, "--body", req.Body)
	}
	if req.Assignee != "" {
		args = append(args, "--assignee", req.Assignee)
	}
	if req.Project != "" {
		args = append(args, "--project", req.Project)
	}
	if req.Workspace != "" {
		args = append(args, "--workspace", req.Workspace)
	}
	if req.Priority != 0 {
		args = append(args, "--priority", fmt.Sprintf("%d", req.Priority))
	}
	result := r.Run(ctx, "hermes", args, extraEnv)
	if !result.IsOK() {
		return nil, errors.New(result.Stderr)
	}
	var task map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(result.Stdout)), &task); err != nil {
		return nil, fmt.Errorf("parse task JSON: %w", err)
	}
	return task, nil
}

// HermesClient is the old struct name kept for backward compatibility.
type HermesClient struct {
	Runner       Runner
	CronJobsPath string
	DefaultBoard string
}

// Runner interface (legacy — kept for tests).
type Runner interface {
	Run(ctx context.Context, name string, args []string, extraEnv map[string]string) CommandResult
}

// NewHermesClient wires an ExecRunner for production use.
func NewHermesClient(bin, workdir, cronJobsPath, defaultBoard string, timeout time.Duration) *HermesClient {
	return &HermesClient{
		Runner:       &ExecRunner{Bin: bin, WorkDir: workdir, Timeout: timeout},
		CronJobsPath: cronJobsPath,
		DefaultBoard: defaultBoard,
	}
}

// KanbanTasks runs `hermes kanban list --json` for the given board.
func (h *HermesClient) KanbanTasks(ctx context.Context, board, status, assignee string) ([]map[string]any, CommandResult, error) {
	runner := h.Runner
	if runner == nil {
		runner = &ExecRunner{Bin: "hermes", Timeout: 30 * time.Second}
	}
	args := []string{"kanban", "list", "--json"}
	extraEnv := map[string]string{}
	if board != "" {
		extraEnv["HERMES_KANBAN_BOARD"] = board
	}
	if status != "" {
		args = append(args, "--status", status)
	}
	if assignee != "" {
		args = append(args, "--assignee", assignee)
	}
	result := runner.Run(ctx, "hermes", args, extraEnv)
	var tasks []map[string]any
	if err := decodeJSON(result.Stdout, &tasks); err != nil {
		return nil, result, fmt.Errorf("non-JSON: %w", err)
	}
	return tasks, result, nil
}

// Agents runs `hermes status --json`.
func (h *HermesClient) Agents(ctx context.Context) (map[string]any, string, CommandResult, bool, error) {
	runner := h.Runner
	if runner == nil {
		runner = &ExecRunner{Bin: "hermes", Timeout: 30 * time.Second}
	}
	jsonArgs := []string{"status", "--json"}
	result := runner.Run(ctx, "hermes", jsonArgs, nil)
	var parsed map[string]any
	if result.ExitCode == 0 && decodeJSON(result.Stdout, &parsed) == nil {
		return parsed, result.Stdout, result, false, nil
	}
	fallback := runner.Run(ctx, "hermes", []string{"status"}, nil)
	if fallback.ExitCode != 0 && strings.TrimSpace(fallback.Stdout) == "" {
		return nil, "", fallback, true, fmt.Errorf("hermes status failed")
	}
	return nil, fallback.Stdout, fallback, true, nil
}

// CronJobs reads the Hermes cron job registry from disk.
func (h *HermesClient) CronJobs() ([]map[string]any, string, error) {
	path := h.CronJobsPath
	if path == "" {
		return nil, "", fmt.Errorf("cron jobs path not configured")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, path, fmt.Errorf("read %s: %w", path, err)
	}
	var payload struct {
		Jobs []map[string]any `json:"jobs"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return nil, path, fmt.Errorf("parse %s: %w", path, err)
	}
	if payload.Jobs == nil {
		payload.Jobs = []map[string]any{}
	}
	return payload.Jobs, path, nil
}

// decodeJSON parses stdout, tolerating leading/trailing noise.
func decodeJSON(raw string, target any) error {
	trimmed := strings.TrimSpace(strings.TrimPrefix(raw, "\ufeff"))
	if trimmed == "" {
		return errors.New("empty output")
	}
	if err := json.Unmarshal([]byte(trimmed), target); err == nil {
		return nil
	}
	start := strings.IndexAny(trimmed, "[{")
	end := strings.LastIndexAny(trimmed, "]}")
	if start >= 0 && end > start {
		return json.Unmarshal([]byte(trimmed[start:end+1]), target)
	}
	return fmt.Errorf("no JSON document found in output")
}

// DecodeJSON is the exported form of decodeJSON, for tests that need to parse
// CLI-shaped output the same way the production code does.
func DecodeJSON(raw string, target any) error { return decodeJSON(raw, target) }

// AgentsProfiles runs `hermes profile list` and parses the table output.
func (h *HermesClient) AgentsProfiles(ctx context.Context) ([]map[string]any, string, error) {
	runner := h.Runner
	if runner == nil {
		runner = &ExecRunner{Bin: "hermes", Timeout: 30 * time.Second}
	}
	result := runner.Run(ctx, "hermes", []string{"profile", "list"}, nil)

	var profiles []map[string]any
	for _, line := range strings.Split(result.Stdout, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		// Skip header, separator, and leading-blank lines
		if strings.HasPrefix(trimmed, "Profile") || strings.HasPrefix(trimmed, "─") {
			continue
		}
		// Data rows start with ◆ or a word character (not whitespace)
		if !strings.HasPrefix(trimmed, "◆") && !unicode.IsLetter(rune(trimmed[0])) {
			continue
		}
		fields := strings.Fields(trimmed)
		if len(fields) < 3 {
			continue
		}
		name := strings.TrimPrefix(fields[0], "◆")
		model := fields[1]
		gateway := fields[2]
		alias := ""
		if len(fields) > 3 && fields[3] != "—" {
			alias = fields[3]
		}
		profiles = append(profiles, map[string]any{
			"name":    name,
			"model":   model,
			"gateway": gateway,
			"running": gateway == "running",
			"alias":   alias,
		})
	}
	return profiles, result.Stdout, nil
}

// Providers parses `hermes config` to extract configured API-key providers.
func (h *HermesClient) Providers(ctx context.Context, board string) ([]map[string]any, string, error) {
	runner := h.Runner
	if runner == nil {
		runner = &ExecRunner{Bin: "hermes", Timeout: 30 * time.Second}
	}
	result := runner.Run(ctx, "hermes", []string{"config"}, nil)

	var providers []map[string]any
	inSection := false
	for _, line := range strings.Split(result.Stdout, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		// Detect API Keys section
		if strings.Contains(trimmed, "API Keys") || strings.Contains(trimmed, "API-Key") {
			inSection = true
			continue
		}
		// Stop at next section
		if inSection && (strings.HasPrefix(trimmed, "◆") || strings.HasPrefix(trimmed, "Model") || strings.HasPrefix(trimmed, "Display") || strings.HasPrefix(trimmed, "Terminal") || strings.HasPrefix(trimmed, "Timezone") || strings.HasPrefix(trimmed, "Context")) {
			break
		}
		if inSection {
			// Line like "  OpenRouter     sk-o...2d53" or "  OpenAI (STT/TTS) (not set)"
			fullLine := strings.TrimSpace(line)
			notSet := strings.Contains(fullLine, "(not set)")
			parts := strings.Fields(fullLine)
			if len(parts) >= 1 {
				name := parts[0]
				// Skip section headers that slipped through
				if name == "Provider" || name == "Key" || name == "API" || name == "API-Key" || name == "◆" {
					continue
				}
				// Handle multi-word names like "OpenAI (STT/TTS)" — join remaining parts as key hint
				var keyHint string
				if notSet {
					keyHint = "(not set)"
				} else if len(parts) > 1 {
					keyHint = parts[len(parts)-1]
				}
				providers = append(providers, map[string]any{
					"name":       name,
					"configured": !notSet && keyHint != "",
					"key_hint":   keyHint,
				})
			}
		}
	}
	return providers, result.Stdout, nil
}
