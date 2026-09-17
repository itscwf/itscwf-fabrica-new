package fabrica

import (
	"context"
)

// KanbanExecAdapter wraps *HermesClient so it satisfies KanbanService.
// It delegates kanban-specific calls to the embedded ExecRunner instead of
// the hermes-kanban-api REST server. Use this when the monolith runs on
// the same host as Hermes (no Docker bridge needed).
type KanbanExecAdapter struct {
	cli *HermesClient
}

// NewKanbanExecAdapter returns a KanbanService backed by the Hermes CLI.
func NewKanbanExecAdapter(cli *HermesClient) *KanbanExecAdapter {
	return &KanbanExecAdapter{cli: cli}
}

var _ KanbanService = (*KanbanExecAdapter)(nil)

func (a *KanbanExecAdapter) KanbanBoards(ctx context.Context) ([]map[string]any, error) {
	return a.cli.KanbanBoards(ctx)
}

func (a *KanbanExecAdapter) KanbanTasks(ctx context.Context, board, status, assignee string) ([]map[string]any, error) {
	tasks, _, err := a.cli.KanbanTasks(ctx, board, status, assignee)
	return tasks, err
}

func (a *KanbanExecAdapter) KanbanTask(ctx context.Context, taskID, board string) (map[string]any, error) {
	runner := a.cli.Runner
	if runner == nil {
		runner = &ExecRunner{Bin: "hermes", Timeout: 30}
	}
	args := []string{"kanban", "show", "--json", taskID}
	extraEnv := map[string]string{}
	if board != "" {
		extraEnv["HERMES_KANBAN_BOARD"] = board
	}
	result := runner.Run(ctx, "hermes", args, extraEnv)
	if !result.IsOK() {
		return nil, &CLIError{Message: result.Stderr}
	}
	var payload struct {
		Task map[string]any `json:"task"`
	}
	if err := decodeJSON(result.Stdout, &payload); err != nil {
		return nil, err
	}
	return payload.Task, nil
}

func (a *KanbanExecAdapter) KanbanRuns(ctx context.Context, taskID string) ([]map[string]any, error) {
	runner := a.cli.Runner
	if runner == nil {
		runner = &ExecRunner{Bin: "hermes", Timeout: 30}
	}
	result := runner.Run(ctx, "hermes", []string{"kanban", "runs", taskID, "--json"}, nil)
	if !result.IsOK() {
		return nil, &CLIError{Message: result.Stderr}
	}
	var runs []map[string]any
	if err := decodeJSON(result.Stdout, &runs); err != nil {
		return nil, err
	}
	return runs, nil
}

func (a *KanbanExecAdapter) Agents(ctx context.Context) (map[string]any, string, CommandResult, bool, error) {
	return a.cli.Agents(ctx)
}

func (a *KanbanExecAdapter) CronJobs() ([]map[string]any, string, error) {
	return a.cli.CronJobs()
}

func (a *KanbanExecAdapter) CreateTask(ctx context.Context, req KanbanCreateTaskRequest) (map[string]any, error) {
	runner := a.cli.Runner
	if runner == nil {
		runner = &ExecRunner{Bin: "hermes", Timeout: 30}
	}
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
	result := runner.Run(ctx, "hermes", args, extraEnv)
	if !result.IsOK() {
		return nil, &CLIError{Message: result.Stderr}
	}
	var task map[string]any
	if err := decodeJSON(result.Stdout, &task); err != nil {
		return nil, err
	}
	return task, nil
}

// KanbanBoards calls hermes kanban boards list --json.
func (h *HermesClient) KanbanBoards(ctx context.Context) ([]map[string]any, error) {
	runner := h.Runner
	if runner == nil {
		runner = &ExecRunner{Bin: "hermes", Timeout: 30}
	}
	result := runner.Run(ctx, "hermes", []string{"kanban", "boards", "list", "--json"}, nil)
	if !result.IsOK() {
		return nil, &CLIError{Message: result.Stderr}
	}
	var boards []map[string]any
	if err := decodeJSON(result.Stdout, &boards); err != nil {
		return nil, err
	}
	return boards, nil
}

// CLIError is a simple error wrapping a CLI stderr/stdout message.
type CLIError struct {
	Message string
}

func (e *CLIError) Error() string { return e.Message }
