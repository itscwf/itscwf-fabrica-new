package fabrica

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

// DefaultKanbanAPIURL is the fallback URL when HERMES_KANBAN_API_URL is not set.
// From Docker containers use host.docker.internal:8650.
const DefaultKanbanAPIURL = "http://host.docker.internal:8650"

// KanbanAPIURL returns the URL for the hermes-kanban-api server.
// It reads from the HERMES_KANBAN_API_URL env var first.
func KanbanAPIURL() string {
	if url := os.Getenv("HERMES_KANBAN_API_URL"); url != "" {
		return url
	}
	return DefaultKanbanAPIURL
}

// HTTPClient calls the hermes-kanban-api (localhost:8650) instead of spawning
// the Hermes CLI directly. This allows the Fabrica backend to run in Docker
// while the API runs on the host.
type HTTPClient struct {
	BaseURL string
	Client  *http.Client
	Board   string
}

// NewHTTPClient returns an HTTPClient that talks to the kanban API.
func NewHTTPClient(baseURL, defaultBoard string) *HTTPClient {
	if baseURL == "" {
		baseURL = "http://localhost:8650"
	}
	return &HTTPClient{
		BaseURL: baseURL,
		Client: &http.Client{
			Timeout: 30 * time.Second,
		},
		Board: defaultBoard,
	}
}

func (h *HTTPClient) do(ctx context.Context, method, path string, body any) (*http.Response, error) {
	var bodyReader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("marshal body: %w", err)
		}
		bodyReader = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, h.BaseURL+path, bodyReader)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	return h.Client.Do(req)
}

// APIResponse wraps the standard response shape from hermes-kanban-api.
type APIResponse struct {
	Data any `json:"data"`
	Meta struct {
		Command    string   `json:"command"`
		Args       []string `json:"args"`
		ExitCode   int      `json:"exit_code"`
		DurationMS int64    `json:"duration_ms"`
		Stdout     string   `json:"stdout"`
		Stderr     string   `json:"stderr"`
		Error      string   `json:"error,omitempty"`
		OK         bool     `json:"ok"`
	} `json:"meta"`
}

// ListBoards returns all boards.
func (h *HTTPClient) KanbanBoards(ctx context.Context) ([]map[string]any, error) {
	resp, err := h.do(ctx, "GET", "/api/v1/boards", nil)
	if err != nil {
		return nil, fmt.Errorf("GET /boards: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("GET /boards: HTTP %d", resp.StatusCode)
	}
	var r APIResponse
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		return nil, fmt.Errorf("decode: %w", err)
	}
	raw, ok := r.Data.([]any)
	if !ok {
		// Return empty slice instead of nil
		return []map[string]any{}, nil
	}
	result := make([]map[string]any, 0, len(raw))
	for _, item := range raw {
		if m, ok := item.(map[string]any); ok {
			result = append(result, m)
		}
	}
	return result, nil
}

// ListTasks returns tasks from the default board (or board query param).
func (h *HTTPClient) KanbanTasks(ctx context.Context, board, status, assignee string) ([]map[string]any, error) {
	path := "/api/v1/tasks?board=" + board
	if status != "" {
		path += "&status=" + status
	}
	if assignee != "" {
		path += "&assignee=" + assignee
	}
	resp, err := h.do(ctx, "GET", path, nil)
	if err != nil {
		return nil, fmt.Errorf("GET /tasks: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 500))
		return nil, fmt.Errorf("GET /tasks: HTTP %d — %s", resp.StatusCode, string(body))
	}
	var r APIResponse
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		return nil, fmt.Errorf("decode: %w", err)
	}
	raw, ok := r.Data.([]any)
	if !ok {
		return []map[string]any{}, nil
	}
	result := make([]map[string]any, 0, len(raw))
	for _, item := range raw {
		if m, ok := item.(map[string]any); ok {
			result = append(result, m)
		}
	}
	return result, nil
}

// GetTask returns a single task by ID.
func (h *HTTPClient) KanbanTask(ctx context.Context, taskID, board string) (map[string]any, error) {
	path := fmt.Sprintf("/api/v1/tasks/%s?board=%s", taskID, board)
	resp, err := h.do(ctx, "GET", path, nil)
	if err != nil {
		return nil, fmt.Errorf("GET /tasks/%s: %w", taskID, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("GET /tasks/%s: HTTP %d", taskID, resp.StatusCode)
	}
	var r APIResponse
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		return nil, fmt.Errorf("decode: %w", err)
	}
	// The data field contains the full task object
	data, ok := r.Data.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("unexpected data shape for task %s", taskID)
	}
	return data, nil
}

// TaskRuns returns run history for a task.
func (h *HTTPClient) KanbanRuns(ctx context.Context, taskID string) ([]map[string]any, error) {
	path := fmt.Sprintf("/api/v1/tasks/%s/runs", taskID)
	resp, err := h.do(ctx, "GET", path, nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var r APIResponse
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		return nil, err
	}
	raw, ok := r.Data.([]any)
	if !ok {
		return []map[string]any{}, nil
	}
	result := make([]map[string]any, 0, len(raw))
	for _, item := range raw {
		if m, ok := item.(map[string]any); ok {
			result = append(result, m)
		}
	}
	return result, nil
}

// Agents returns Hermes agent status (hermes status output as raw string).
func (h *HTTPClient) Agents(ctx context.Context) (map[string]any, string, CommandResult, bool, error) {
	resp, err := h.do(ctx, "GET", "/api/v1/agents", nil)
	if err != nil {
		return nil, "", CommandResult{}, false, fmt.Errorf("GET /agents: %w", err)
	}
	defer resp.Body.Close()

	var r APIResponse
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		return nil, "", CommandResult{}, false, fmt.Errorf("decode: %w", err)
	}

	rawStr, ok := r.Data.(string)
	if !ok {
		return nil, "", CommandResult{}, false, fmt.Errorf("expected string data from /agents")
	}

	// Parse the TUI output into structured data
	data := parseStatusOutput(rawStr)
	meta := r.Meta

	return data, rawStr, CommandResult{
		Command:    "hermes status",
		Args:       []string{"status"},
		ExitCode:   meta.ExitCode,
		DurationMS: meta.DurationMS,
		Stdout:     rawStr,
	}, false, nil
}

// CronJobs returns the list of cron jobs from hermes cron list.
func (h *HTTPClient) CronJobs() ([]map[string]any, string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	resp, err := h.do(ctx, "GET", "/api/v1/crons", nil)
	if err != nil {
		return nil, "", fmt.Errorf("GET /crons: %w", err)
	}
	defer resp.Body.Close()

	var r APIResponse
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		return nil, "", fmt.Errorf("decode: %w", err)
	}

	rawStr, ok := r.Data.(string)
	if !ok {
		return nil, "", fmt.Errorf("expected string data from /crons")
	}

	jobs, err := parseCronList(rawStr)
	if err != nil {
		return nil, rawStr, err
	}
	return jobs, h.Board + " (hermes-kanban-api)", nil
}

// CreateTask POSTs to /api/v1/tasks to create a new task.
func (h *HTTPClient) CreateTask(ctx context.Context, req KanbanCreateTaskRequest) (map[string]any, error) {
	payload := map[string]string{
		"title": req.Title,
	}
	if req.Body != "" {
		payload["body"] = req.Body
	}
	if req.Assignee != "" {
		payload["assignee"] = req.Assignee
	}
	if req.Project != "" {
		payload["project"] = req.Project
	}
	if req.Workspace != "" {
		payload["workspace"] = req.Workspace
	}
	resp, err := h.do(ctx, "POST", "/api/v1/tasks", payload)
	if err != nil {
		return nil, fmt.Errorf("POST /tasks: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 500))
		return nil, fmt.Errorf("POST /tasks: HTTP %d — %s", resp.StatusCode, string(body))
	}
	var r APIResponse
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		return nil, fmt.Errorf("decode: %w", err)
	}
	data, ok := r.Data.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("unexpected data shape for new task")
	}
	return data, nil
}

// parseStatusOutput extracts structured data from hermes status TUI output.
func parseStatusOutput(raw string) map[string]any {
	// Return raw for now — the TUI output is not machine-parseable
	// The raw string is returned as the second return value
	return map[string]any{"raw": raw}
}

// ParseStatusOutput exposes the TUI status parser to other packages (tests).
func ParseStatusOutput(raw string) map[string]any { return parseStatusOutput(raw) }

// ParseCronList exposes the cron list parser to other packages (tests).
func ParseCronList(raw string) ([]map[string]any, error) { return parseCronList(raw) }

// parseCronList parses "hermes cron list" output into structured data.
// Handles TUI box-drawing characters (unicode category So/Sm/Sk).
// isHex returns true if s consists only of hex characters (0-9, a-f).
func isHex(s string) bool {
	for _, c := range s {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')) {
			return false
		}
	}
	return len(s) > 0
}

func parseCronList(raw string) ([]map[string]any, error) {
	var jobs []map[string]any
	lines := strings.Split(raw, "\n")
	var current map[string]any

	stripUnicode := func(s string) string {
		var out strings.Builder
		for _, r := range s {
			// Keep printable ASCII, numbers, colons, spaces, dashes, brackets, parens
			if r == ' ' || r == '	' || r == ':' || r == '-' || r == '/' ||
				(r >= '0' && r <= '9') || (r >= 'a' && r <= 'z') ||
				(r >= 'A' && r <= 'Z') || r == '.' || r == '_' ||
				r == '[' || r == ']' || r == '(' || r == ')' ||
				r == '*' || r == '+' || r == ',' || r == '?' || r == '#' ||
				r == '"' || r == '@' ||
				r == '∞' || r == '•' || r == '✗' || r == '✓' || r == '◉' {
				out.WriteRune(r)
			}
		}
		return out.String()
	}

	for _, rawLine := range lines {
		line := strings.TrimSpace(stripUnicode(rawLine))
		if line == "" || strings.Contains(line, "Scheduled Jobs") {
			continue
		}

		// Job ID: 12-char hex at start of line (may have leading space and [status] after)
		trimmed := strings.TrimSpace(line)
		if len(trimmed) >= 12 {
			id := trimmed[:12]
			if isHex(id) {
				if current != nil {
					jobs = append(jobs, current)
				}
				current = map[string]any{"id": id}
				continue
			}
		}
		if current != nil {
			colon := strings.IndexByte(line, ':')
			if colon > 0 {
				key := strings.TrimSpace(line[:colon])
				val := strings.TrimSpace(line[colon+1:])
				switch key {
				case "Name":
					current["name"] = val
				case "Schedule":
					current["schedule"] = val
				case "Last run":
					// "Last run:  ...  ok" or "Last run:  ...  error: ..."
					if parts := strings.SplitN(val, "  ", 2); len(parts) >= 2 {
						current["last_run"] = strings.TrimSpace(parts[0])
						statusRaw := strings.TrimSpace(parts[1])
						if strings.HasPrefix(statusRaw, "error") {
							current["last_status"] = "error"
						} else if statusRaw == "ok" {
							current["last_status"] = "completed"
						} else {
							current["last_status"] = statusRaw
						}
					} else {
						current["last_run"] = val
					}
				case "Execution":
					current["last_execution"] = val
				default:
					current[key] = val
				}
			}
		}
	}
	if current != nil {
		jobs = append(jobs, current)
	}
	return jobs, nil
}
