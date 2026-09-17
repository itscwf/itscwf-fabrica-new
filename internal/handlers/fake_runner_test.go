package handlers

import (
	"context"
	"fmt"
	"sync"

	"github.com/itscwf/itscwf-fabrica-new/pkg/fabrica"
)

// fakeRunner replaces the real Hermes CLI in tests. It implements both the
// legacy Run-based surface and the full fabrica.KanbanService so the router
// can be wired with it directly (every KanbanService method funnels through
// Run, keeping the per-test `handler` functions in charge of the CLI output).
type fakeRunner struct {
	mu      sync.Mutex
	calls   []fakeCall
	handler func(name string, args []string, env map[string]string) fabrica.CommandResult
}

func (f *fakeRunner) Run(_ context.Context, name string, args []string, env map[string]string) fabrica.CommandResult {
	f.mu.Lock()
	f.calls = append(f.calls, fakeCall{Name: name, Args: append([]string{}, args...), Env: env})
	f.mu.Unlock()
	if f.handler != nil {
		return f.handler(name, args, env)
	}
	return fabrica.CommandResult{Command: name, Args: args, ExitCode: 0}
}

func (f *fakeRunner) KanbanBoards(ctx context.Context) ([]map[string]any, error) {
	res := f.Run(ctx, "hermes", []string{"kanban", "boards", "--json"}, nil)
	return decodeKanbanList(res)
}

func (f *fakeRunner) KanbanTasks(ctx context.Context, board, status, assignee string) ([]map[string]any, error) {
	args := []string{"kanban", "list", "--json"}
	if board != "" {
		args = append(args, "--board", board)
	}
	if status != "" {
		args = append(args, "--status", status)
	}
	if assignee != "" {
		args = append(args, "--assignee", assignee)
	}
	res := f.Run(ctx, "hermes", args, map[string]string{"HERMES_KANBAN_BOARD": board})
	if res.ExitCode != 0 {
		return nil, fmt.Errorf("%s", res.Stderr)
	}
	return decodeKanbanList(res)
}

func (f *fakeRunner) KanbanTask(ctx context.Context, taskID, board string) (map[string]any, error) {
	res := f.Run(ctx, "hermes", []string{"kanban", "show", "--id", taskID, "--board", board, "--json"}, nil)
	if res.ExitCode != 0 {
		return nil, fmt.Errorf("%s", res.Stderr)
	}
	var out map[string]any
	if err := fabrica.DecodeJSON(res.Stdout, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func (f *fakeRunner) KanbanRuns(ctx context.Context, taskID string) ([]map[string]any, error) {
	res := f.Run(ctx, "hermes", []string{"kanban", "runs", "--id", taskID, "--json"}, nil)
	return decodeKanbanList(res)
}

func (f *fakeRunner) Agents(ctx context.Context) (map[string]any, string, fabrica.CommandResult, bool, error) {
	res := f.Run(ctx, "hermes", []string{"status"}, nil)
	parsed := fabrica.ParseStatusOutput(res.Stdout)
	return parsed, res.Stdout, res, false, nil
}

func (f *fakeRunner) CronJobs() ([]map[string]any, string, error) {
	res := f.Run(context.Background(), "hermes", []string{"cron", "list"}, nil)
	if res.ExitCode != 0 {
		return nil, res.Stdout, fmt.Errorf("hermes cron list failed: %s", res.Stderr)
	}
	jobs, err := fabrica.ParseCronList(res.Stdout)
	return jobs, "itscwf_fabrica (hermes-kanban-api)", err
}

func (f *fakeRunner) CreateTask(ctx context.Context, req fabrica.KanbanCreateTaskRequest) (map[string]any, error) {
	res := f.Run(ctx, "hermes", []string{"kanban", "create", "--json", "--title", req.Title}, nil)
	if res.ExitCode != 0 {
		return nil, fmt.Errorf("%s", res.Stderr)
	}
	var out map[string]any
	if err := fabrica.DecodeJSON(res.Stdout, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func decodeKanbanList(res fabrica.CommandResult) ([]map[string]any, error) {
	var out []map[string]any
	if err := fabrica.DecodeJSON(res.Stdout, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func (f *fakeRunner) lastCall() (fakeCall, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.calls) == 0 {
		return fakeCall{}, false
	}
	return f.calls[len(f.calls)-1], true
}
