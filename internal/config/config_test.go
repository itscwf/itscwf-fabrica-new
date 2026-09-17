package config

import (
	"os"
	"path/filepath"
	"testing"
)

func clearEnv(t *testing.T) {
	t.Helper()
	for _, key := range []string{
		"ENVIRONMENT", "PORT", "DB_PATH", "LOG_LEVEL", "CORS_ORIGINS",
		"HTTP_READ_TIMEOUT", "HTTP_WRITE_TIMEOUT", "HTTP_SHUTDOWN_TIMEOUT",
		"FABRICA_SERVER_ENV", "FABRICA_SERVER_PORT", "FABRICA_PORT",
		"FABRICA_DB_PATH", "FABRICA_DATABASE_PATH", "FABRICA_LOG_LEVEL", "FABRICA_CORS_ORIGINS",
		"JWT_SECRET", "FABRICA_JWT_SECRET", "JWT_ISSUER", "FABRICA_JWT_ISSUER",
		"TOKEN_TTL_HOURS", "FABRICA_TOKEN_TTL_HOURS",
		"ADMIN_USER", "FABRICA_ADMIN_USER", "ADMIN_PASSWORD", "FABRICA_ADMIN_PASSWORD",
		"ADMIN_PASSWORD_HASH", "FABRICA_ADMIN_PASSWORD_HASH",
		"HERMES_BIN", "FABRICA_HERMES_BIN", "HERMES_CRON_JOBS", "FABRICA_HERMES_CRON_JOBS",
		"HERMES_KANBAN_BOARD", "FABRICA_HERMES_BOARD", "HERMES_KANBAN_WORKSPACE",
	} {
		os.Unsetenv(key)
	}
}

func TestLoadDefaultsWithoutFile(t *testing.T) {
	clearEnv(t)
	cfg, err := Load(filepath.Join(t.TempDir(), "missing.yaml"))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.Port != DefaultPort {
		t.Fatalf("porta padrao = %q", cfg.Port)
	}
	if cfg.DBPath != "data/itscwf_fabrica.db" {
		t.Fatalf("DB_PATH padrao = %q", cfg.DBPath)
	}
	if !cfg.IsDevelopment() {
		t.Fatalf("ENVIRONMENT padrao = %q", cfg.Env)
	}
	if cfg.Auth.AdminUser != "admin" {
		t.Fatalf("admin padrao = %q", cfg.Auth.AdminUser)
	}
	if !cfg.EphemeralSecret || len(cfg.Auth.JWTSecret) < 16 {
		t.Fatalf("esperado segredo JWT efemero, veio %q (ephemeral=%v)", cfg.Auth.JWTSecret, cfg.EphemeralSecret)
	}
	if cfg.Auth.TokenTTL.Hours() != 12 {
		t.Fatalf("ttl padrao = %v", cfg.Auth.TokenTTL)
	}
}

func TestLoadHonoursEnvFromCompose(t *testing.T) {
	clearEnv(t)
	t.Setenv("ENVIRONMENT", "production")
	t.Setenv("PORT", "9099")
	t.Setenv("DB_PATH", "/app/data/itscwf_fabrica.db")
	t.Setenv("LOG_LEVEL", "warn")
	t.Setenv("CORS_ORIGINS", "http://localhost:3005,http://a.test")
	t.Setenv("JWT_SECRET", "segredo-de-teste-com-32-caracteres")
	t.Setenv("ADMIN_USER", "edward")
	t.Setenv("ADMIN_PASSWORD", "senha-forte")
	t.Setenv("HERMES_CRON_JOBS", "/hermes/cron/jobs.json")
	t.Setenv("HERMES_KANBAN_BOARD", "itscwf_fabrica")

	cfg, err := Load("")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if !cfg.IsProduction() || cfg.GinMode() != "release" {
		t.Fatalf("ambiente = %q gin = %q", cfg.Env, cfg.GinMode())
	}
	if cfg.Port != "9099" || cfg.Addr() != ":9099" {
		t.Fatalf("porta = %q addr = %q", cfg.Port, cfg.Addr())
	}
	if cfg.DBPath != "/app/data/itscwf_fabrica.db" || cfg.Database.Path != "/app/data/itscwf_fabrica.db" {
		t.Fatalf("DB_PATH = %q", cfg.DBPath)
	}
	if len(cfg.CORSOrigins) != 2 || cfg.CORSOrigins[0] != "http://localhost:3005" {
		t.Fatalf("CORS_ORIGINS = %v", cfg.CORSOrigins)
	}
	if cfg.Auth.AdminUser != "edward" || cfg.Auth.AdminPassword != "senha-forte" {
		t.Fatalf("auth = %+v", cfg.Auth)
	}
	if cfg.Hermes.CronJobsPath != "/hermes/cron/jobs.json" || cfg.Hermes.KanbanBoard != "itscwf_fabrica" {
		t.Fatalf("hermes = %+v", cfg.Hermes)
	}
	if cfg.EphemeralSecret {
		t.Fatalf("segredo explicito nao pode ser efemero")
	}
}

func TestLoadYAMLAndEnvPrecedence(t *testing.T) {
	clearEnv(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	yaml := `
server:
  port: 7000
  log_level: debug
database:
  path: /tmp/from-yaml.db
auth:
  jwt_secret: yaml-secret-0123456789abcdef
  admin_user: yaml-admin
  admin_password: yaml-pass
  token_ttl_hours: 4
hermes:
  cron_jobs_path: /tmp/jobs.json
  kanban_board: outro_board
  cache_ttl_seconds: 0
cors:
  allowed_origins:
    - http://localhost:5173
`
	if err := os.WriteFile(path, []byte(yaml), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.Port != "7000" || cfg.DBPath != "/tmp/from-yaml.db" || cfg.LogLevel != "debug" {
		t.Fatalf("yaml nao aplicado: %+v", cfg)
	}
	if cfg.Auth.AdminUser != "yaml-admin" || cfg.Auth.TokenTTLHours != 4 {
		t.Fatalf("auth yaml = %+v", cfg.Auth)
	}
	if cfg.Hermes.KanbanBoard != "outro_board" || cfg.Hermes.CacheTTLSecs != 0 {
		t.Fatalf("hermes yaml = %+v", cfg.Hermes)
	}
	if len(cfg.CORSOrigins) != 1 || cfg.CORSOrigins[0] != "http://localhost:5173" {
		t.Fatalf("cors yaml = %v", cfg.CORSOrigins)
	}

	// env vence o arquivo
	t.Setenv("PORT", "8082")
	t.Setenv("DB_PATH", "/tmp/from-env.db")
	cfg, err = Load(path)
	if err != nil {
		t.Fatalf("load com env: %v", err)
	}
	if cfg.Port != "8082" || cfg.DBPath != "/tmp/from-env.db" {
		t.Fatalf("env nao venceu: porta=%q db=%q", cfg.Port, cfg.DBPath)
	}
}

func TestLoadRejectsInvalidConfiguration(t *testing.T) {
	clearEnv(t)

	t.Setenv("JWT_SECRET", "curto")
	if _, err := Load(""); err == nil {
		t.Fatalf("esperado erro para segredo curto")
	}

	clearEnv(t)
	t.Setenv("PORT", "abc")
	if _, err := Load(""); err == nil {
		t.Fatalf("esperado erro para porta invalida")
	}

	clearEnv(t)
	t.Setenv("ENVIRONMENT", "producao")
	if _, err := Load(""); err == nil {
		t.Fatalf("esperado erro para ambiente invalido")
	}

	clearEnv(t)
	t.Setenv("ENVIRONMENT", "production")
	if _, err := Load(""); err == nil {
		t.Fatalf("producao exige JWT_SECRET explicito")
	}

	clearEnv(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte("server:\n  mode: release\n"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := Load(path); err == nil {
		t.Fatalf("modo release exige JWT_SECRET explicito")
	}
}
