package db

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/itscwf/itscwf-fabrica-new/internal/models"
)

// TestCanonicalSchemaCompatibility garante que os modelos leem e escrevem o
// formato gravado por scripts/schema_init.sql + scripts/migrate.py:
// timestamps TEXT ISO-8601 e soft delete em deleted_at.
func TestCanonicalSchemaCompatibility(t *testing.T) {
	path := filepath.Join(t.TempDir(), "canonical.db")
	conn, err := Open(Options{Path: path, LogLevel: "silent"})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer Close(conn)

	// Tabela no formato canonico (TEXT + DEFAULT strftime), como no schema.
	ddl := `
CREATE TABLE projects (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    slug          TEXT    NOT NULL UNIQUE,
    name          TEXT    NOT NULL,
    description   TEXT,
    status        TEXT    NOT NULL DEFAULT 'active',
    board_slug    TEXT,
    primary_path  TEXT,
    repo_url      TEXT,
    icon          TEXT,
    color         TEXT,
    archived      INTEGER NOT NULL DEFAULT 0,
    created_at    TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ','now')),
    updated_at    TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ','now')),
    deleted_at    TEXT
);`
	if err := conn.Exec(ddl).Error; err != nil {
		t.Fatalf("ddl: %v", err)
	}

	// A migracao da API precisa adicionar as colunas aditivas (tech_stack,
	// last_activity) sem tocar nas colunas canonicas.
	if err := Migrate(conn); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	for _, column := range []string{"tech_stack", "last_activity"} {
		ok, err := ColumnExists(conn, "projects", column)
		if err != nil {
			t.Fatalf("column exists: %v", err)
		}
		if !ok {
			t.Fatalf("coluna aditiva projects.%s nao foi criada", column)
		}
	}
	// Idempotente: rodar de novo nao duplica nem falha.
	if err := Migrate(conn); err != nil {
		t.Fatalf("second migrate: %v", err)
	}
	if err := conn.Exec(`INSERT INTO projects (slug, name, status, primary_path, deleted_at)
		VALUES ('small-ngo','Small NGO','active','/root/projetos/itscwf/small-ngo','2026-09-15T15:59:49Z')`).Error; err != nil {
		t.Fatalf("insert: %v", err)
	}
	if err := conn.Exec(`INSERT INTO projects (slug, name) VALUES ('ed2ti-website','Ed2ti Website')`).Error; err != nil {
		t.Fatalf("insert 2: %v", err)
	}

	// Leitura: o driver devolve TEXT e o tipo models.Timestamp precisa parsear.
	var parsed models.Project
	if err := conn.Where("slug = ?", "ed2ti-website").First(&parsed).Error; err != nil {
		t.Fatalf("find: %v", err)
	}
	if parsed.CreatedAt.IsZero() {
		t.Fatalf("created_at TEXT nao foi convertido: %+v", parsed)
	}
	if parsed.LocalPath != "" {
		t.Fatalf("primary_path deveria estar vazio: %q", parsed.LocalPath)
	}
	if parsed.DeletedAt != nil {
		t.Fatalf("deleted_at deveria ser NULL: %v", parsed.DeletedAt)
	}
	if difference := time.Since(parsed.CreatedAt.Time); difference > time.Hour || difference < -time.Hour {
		t.Fatalf("created_at fora do esperado: %v", parsed.CreatedAt.Time)
	}

	// Soft delete explicito gravado pela API (TEXT) precisa ser lido de volta.
	var softDeleted models.Project
	if err := conn.Where("slug = ?", "small-ngo").First(&softDeleted).Error; err != nil {
		t.Fatalf("find soft: %v", err)
	}
	if softDeleted.DeletedAt == nil || softDeleted.DeletedAt.IsZero() {
		t.Fatalf("deleted_at informado nao foi lido: %+v", softDeleted.DeletedAt)
	}
	if softDeleted.DeletedAt.Time.UTC().Format(models.Layout) != "2026-09-15T15:59:49Z" {
		t.Fatalf("deleted_at = %v", softDeleted.DeletedAt.Time)
	}
	if softDeleted.LocalPath != "/root/projetos/itscwf/small-ngo" {
		t.Fatalf("primary_path = %q", softDeleted.LocalPath)
	}

	// Escrita: created_at passa a ser gravado no formato canonico.
	created := models.Project{
		Slug: "novo-projeto", Name: "Novo Projeto", Status: "active",
		Archived: false,
	}
	created.TouchNew(models.NewTimestamp(time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC)))
	if err := conn.Create(&created).Error; err != nil {
		t.Fatalf("create: %v", err)
	}
	var raw string
	if err := conn.Raw("SELECT created_at FROM projects WHERE slug = ?", "novo-projeto").Scan(&raw).Error; err != nil {
		t.Fatalf("raw select: %v", err)
	}
	if raw != "2026-09-15T12:00:00Z" {
		t.Fatalf("created_at gravado = %q, esperado 2026-09-15T12:00:00Z", raw)
	}
}

// TestMigrateKeepsCanonicalTables verifica que o AutoMigrate adiciona apenas as
// colunas aditivas da API e nao remove as tabelas canonicas existentes.
func TestMigrateKeepsCanonicalTables(t *testing.T) {
	path := filepath.Join(t.TempDir(), "canonical-migrate.db")
	conn, err := Open(Options{Path: path, LogLevel: "silent"})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer Close(conn)

	for _, ddl := range []string{
		`CREATE TABLE eca_steps (id INTEGER PRIMARY KEY AUTOINCREMENT, eca_id TEXT NOT NULL, ordem INTEGER NOT NULL, descricao TEXT NOT NULL, created_at TEXT)`,
		`CREATE TABLE schema_migrations (version INTEGER PRIMARY KEY, name TEXT NOT NULL, applied_at DATETIME)`,
		`CREATE TABLE qa_cycle_sources (id INTEGER PRIMARY KEY AUTOINCREMENT, source_file TEXT NOT NULL UNIQUE)`,
	} {
		if err := conn.Exec(ddl).Error; err != nil {
			t.Fatalf("ddl: %v", err)
		}
	}

	if err := Migrate(conn); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	var names []string
	if err := conn.Raw("SELECT name FROM sqlite_master WHERE type = 'table'").Scan(&names).Error; err != nil {
		t.Fatalf("list: %v", err)
	}
	present := map[string]bool{}
	for _, name := range names {
		present[name] = true
	}
	for _, table := range []string{"eca_steps", "schema_migrations", "qa_cycle_sources", "projects", "qa_cycles", "cron_jobs", "activity_log"} {
		if !present[table] {
			t.Fatalf("tabela %q sumiu/nao existe (tabelas: %v)", table, names)
		}
	}
}
