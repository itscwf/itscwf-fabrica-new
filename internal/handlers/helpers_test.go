package handlers

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"github.com/itscwf/itscwf-fabrica-new/internal/config"
	"github.com/itscwf/itscwf-fabrica-new/internal/db"
	"github.com/itscwf/itscwf-fabrica-new/internal/middleware"
	"github.com/itscwf/itscwf-fabrica-new/pkg/fabrica"
)

// fakeCall records one Hermes CLI invocation.
type fakeCall struct {
	Name string
	Args []string
	Env  map[string]string
}

// fakeRunner replaces the real Hermes CLI in tests — full implementation
// (Run + fabrica.KanbanService) lives in fake_runner_test.go.

// testEnv bundles the router, database and credentials used by the API tests.
type testEnv struct {
	t      *testing.T
	Router *gin.Engine
	DB     *gorm.DB
	Cfg    *config.Config
	Tokens *fabrica.TokenManager
	AuthMW *middleware.JWTAuth
	Runner *fakeRunner
	Token  string
	Dir    string
}

func newTestEnv(t *testing.T) *testEnv {
	return newTestEnvWith(t, nil)
}

// newTestEnvWith permite preparar o banco antes da migracao da API — usado
// para subir o router sobre o schema canonico (scripts/schema_init.sql) e
// provar que o CRUD funciona onde o deploy roda, nao so no schema gerado
// pelos modelos.
func newTestEnvWith(t *testing.T, prepare func(*gorm.DB) error) *testEnv {
	t.Helper()
	gin.SetMode(gin.TestMode)

	dir := t.TempDir()
	cfg := &config.Config{
		Env:         config.EnvDevelopment,
		Port:        "8082",
		DBPath:      filepath.Join(dir, "fabrica.db"),
		LogLevel:    "error",
		CORSOrigins: []string{"*"},
		Auth: config.AuthConfig{
			JWTSecret:     "test-secret-0123456789abcdef",
			Issuer:        "itscwf-fabrica-test",
			TokenTTL:      time.Hour,
			TokenTTLHours: 1,
			AdminUser:     "admin",
			AdminPassword: "s3cret",
		},
		Database: config.DatabaseConfig{Path: filepath.Join(dir, "fabrica.db"), BusyTimeout: 5 * time.Second, MaxOpenConns: 1},
		Hermes: config.HermesConfig{
			Bin:          "hermes",
			CronJobsPath: filepath.Join(dir, "jobs.json"),
			KanbanBoard:  "itscwf_fabrica",
			TimeoutSecs:  5,
			CacheTTLSecs: 0, // cache desligado, exceto onde o teste liga
			WorkDir:      dir,
		},
	}

	conn, err := db.Open(db.Options{Path: cfg.Database.Path, BusyTimeout: 5 * time.Second, MaxOpenConns: 1, LogLevel: "silent"})
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	t.Cleanup(func() { _ = db.Close(conn) })
	if prepare != nil {
		if err := prepare(conn); err != nil {
			t.Fatalf("prepare database: %v", err)
		}
	}
	if err := db.Migrate(conn); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	tokens, err := fabrica.NewTokenManager(cfg.Auth.JWTSecret, cfg.Auth.Issuer, time.Hour)
	if err != nil {
		t.Fatalf("token manager: %v", err)
	}
	token, _, err := tokens.Issue(cfg.Auth.AdminUser, middleware.RoleAdmin)
	if err != nil {
		t.Fatalf("issue token: %v", err)
	}

	runner := &fakeRunner{}
	client := &fabrica.HermesClient{
		Runner:       runner,
		CronJobsPath: cfg.Hermes.CronJobsPath,
		DefaultBoard: cfg.Hermes.KanbanBoard,
	}

	authMW := middleware.NewJWTAuth(tokens)
	router := NewRouter(Deps{
		Config:   cfg,
		DB:       conn,
		Logger:   slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelError})),
		Hermes:   client,
		Kanban:   runner,
		Auth:     authMW,
		Recorder: NewRecorder(conn),
	})

	return &testEnv{t: t, Router: router, DB: conn, Cfg: cfg, Tokens: tokens, AuthMW: authMW, Runner: runner, Token: token, Dir: dir}
}

// request performs an HTTP call through the router. body may be nil.
func (e *testEnv) request(method, path string, body any, token string) *httptest.ResponseRecorder {
	e.t.Helper()
	var payload []byte
	if body != nil {
		switch typed := body.(type) {
		case string:
			payload = []byte(typed)
		case []byte:
			payload = typed
		default:
			encoded, err := json.Marshal(typed)
			if err != nil {
				e.t.Fatalf("marshal body: %v", err)
			}
			payload = encoded
		}
	}
	req := httptest.NewRequest(method, path, bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	e.Router.ServeHTTP(rec, req)
	return rec
}

// authed is request() with the admin token.
func (e *testEnv) authed(method, path string, body any) *httptest.ResponseRecorder {
	return e.request(method, path, body, e.Token)
}

func (e *testEnv) decode(rec *httptest.ResponseRecorder) map[string]any {
	e.t.Helper()
	out := map[string]any{}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		e.t.Fatalf("decode response %q: %v", rec.Body.String(), err)
	}
	return out
}

func (e *testEnv) object(rec *httptest.ResponseRecorder) map[string]any {
	e.t.Helper()
	body := e.decode(rec)
	data, ok := body["data"].(map[string]any)
	if !ok {
		e.t.Fatalf("expected object payload, got %v", body)
	}
	return data
}

func (e *testEnv) items(rec *httptest.ResponseRecorder) []any {
	e.t.Helper()
	body := e.decode(rec)
	data, ok := body["data"].([]any)
	if !ok {
		e.t.Fatalf("expected list payload, got %v", body)
	}
	return data
}

func (e *testEnv) errorCode(rec *httptest.ResponseRecorder) string {
	e.t.Helper()
	body := e.decode(rec)
	errObj, ok := body["error"].(map[string]any)
	if !ok {
		e.t.Fatalf("expected error payload, got %v", body)
	}
	code, _ := errObj["code"].(string)
	return code
}

func (e *testEnv) expectStatus(rec *httptest.ResponseRecorder, want int) {
	e.t.Helper()
	if rec.Code != want {
		e.t.Fatalf("status = %d, want %d (body: %s)", rec.Code, want, rec.Body.String())
	}
}

// seedProject creates a project through the API and returns its id.
func (e *testEnv) seedProject(name string) uint {
	e.t.Helper()
	rec := e.authed(http.MethodPost, "/api/v1/projects", map[string]any{"name": name})
	e.expectStatus(rec, http.StatusCreated)
	id, ok := e.object(rec)["id"].(float64)
	if !ok {
		e.t.Fatalf("project id missing: %s", rec.Body.String())
	}
	return uint(id)
}

// seedCycle creates a QA cycle for a project and returns its id.
func (e *testEnv) seedCycle(projectID uint, build string) uint {
	e.t.Helper()
	rec := e.authed(http.MethodPost, "/api/v1/qa/cycles", map[string]any{"project_id": projectID, "build": build})
	e.expectStatus(rec, http.StatusCreated)
	id, ok := e.object(rec)["id"].(float64)
	if !ok {
		e.t.Fatalf("cycle id missing: %s", rec.Body.String())
	}
	return uint(id)
}

func idOf(t *testing.T, rec *httptest.ResponseRecorder) uint {
	t.Helper()
	body := map[string]any{}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	data, _ := body["data"].(map[string]any)
	id, _ := data["id"].(float64)
	if id == 0 {
		t.Fatalf("no id in payload: %s", rec.Body.String())
	}
	return uint(id)
}

// fabricaNewManager builds a token manager. A negative TTL yields tokens that
// are already expired, which is how the expiry test works.
func fabricaNewManager(t *testing.T, secret string, ttl time.Duration) (*fabrica.TokenManager, error) {
	t.Helper()
	return fabrica.NewTokenManager(secret, "itscwf-fabrica-test", ttl)
}
