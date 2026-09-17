package migrations

import (
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
)

// cardContract e o contrato do card T2 (t_8ed30014): cada tabela com as colunas
// exigidas. Onde o nome do card difere do nome canonico, o teste lista o nome
// CANONICO e o comentario aponta o nome do card — o mapeamento JSON fica em
// docs/SCHEMA.md.
var cardContract = map[string][]string{
	"projects":     {"id", "name", "slug", "repo_url", "primary_path", "tech_stack", "status", "description", "created_at", "updated_at"},
	"repos":        {"id", "project_id", "url", "default_branch", "head_commit", "last_activity"},
	"worktrees":    {"id", "project_id", "name", "branch", "local_path", "last_commit", "last_activity"},
	"eca_registry": {"id", "eca_id", "titulo", "status", "responsavel", "repo", "created_at", "project_id"},
	"qa_cycles":    {"id", "project_id", "build", "inicio", "fim", "status", "proximo_tc"},
	"qa_testcases": {"id", "cycle_id", "tc_id", "titulo", "modulo", "status"},
	"qa_executions": {"id", "execution_id", "tc_id", "status", "duration_ms", "nota",
		"data", "deleted_at"},
	"cron_jobs": {"id", "job_id", "name", "schedule", "provider", "model", "script", "enabled", "last_run_at", "last_status"},
	"cron_executions": {"id", "job_id", "started_at", "finished_at", "exit_code", "output",
		"error"},
	"agents":    {"id", "name", "provider", "model", "status", "last_seen_at"},
	"providers": {"id", "name", "base_url", "api_key_ref", "rate_limit", "status"},
	"servers":   {"id", "name", "host", "services", "status"},
	"activity_log": {"id", "ts", "actor", "kind", "ref", "message", "project_id",
		"deleted_at"},
}

func tempDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := OpenDB(Options{Path: filepath.Join(t.TempDir(), "fabrica.db")})
	if err != nil {
		t.Fatalf("OpenDB: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func TestLoadOrdenaEValidaChecksum(t *testing.T) {
	all, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(all) < 2 {
		t.Fatalf("esperava pelo menos 2 migrations, veio %d", len(all))
	}
	for i, m := range all {
		if i > 0 && m.Version <= all[i-1].Version {
			t.Fatalf("ordem invalida: %d depois de %d", m.Version, all[i-1].Version)
		}
		if len(m.Checksum) != 64 {
			t.Fatalf("%s: checksum com %d chars", m.Filename, len(m.Checksum))
		}
		if m.SQL == "" {
			t.Fatalf("%s: SQL vazio", m.Filename)
		}
	}
	if all[0].Filename != "0001_schema.sql" {
		t.Fatalf("primeira migration = %s", all[0].Filename)
	}
}

func TestUpCriaSchemaCompleto(t *testing.T) {
	db := tempDB(t)

	applied, err := Up(db)
	if err != nil {
		t.Fatalf("Up: %v", err)
	}
	if len(applied) == 0 {
		t.Fatal("nenhuma migration aplicada num banco novo")
	}

	missing, err := MissingTables(db)
	if err != nil {
		t.Fatalf("MissingTables: %v", err)
	}
	if len(missing) > 0 {
		t.Fatalf("tabelas obrigatorias ausentes: %v", missing)
	}

	tables, err := Tables()
	if err != nil {
		t.Fatalf("Tables: %v", err)
	}
	if len(tables) != len(RequiredTables) {
		t.Fatalf("schema tem %d tabelas de dominio (%v), esperado %d", len(tables), tables, len(RequiredTables))
	}

	// Contrato do card: tabela -> colunas obrigatorias.
	for table, columns := range cardContract {
		for _, column := range columns {
			ok, err := DBHasColumn(db, table, column)
			if err != nil {
				t.Fatalf("DBHasColumn(%s,%s): %v", table, column, err)
			}
			if !ok {
				t.Errorf("contrato do card: %s.%s ausente", table, column)
			}
		}
	}
}

func TestUpEEIdempotenteESeedNaoDuplica(t *testing.T) {
	db := tempDB(t)

	if _, err := Up(db); err != nil {
		t.Fatalf("Up 1: %v", err)
	}
	counts := map[string]int{}
	for _, table := range []string{"projects", "repos", "worktrees"} {
		var n int
		if err := db.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&n); err != nil {
			t.Fatalf("count %s: %v", table, err)
		}
		counts[table] = n
	}
	if counts["projects"] == 0 || counts["repos"] == 0 || counts["worktrees"] == 0 {
		t.Fatalf("seed nao populou as tabelas: %v", counts)
	}
	// FKs do seed precisam ter sido resolvidas.
	var orphans int
	if err := db.QueryRow(
		`SELECT COUNT(*) FROM worktrees w
		 LEFT JOIN projects p ON p.id = w.project_id
		 WHERE p.id IS NULL`).Scan(&orphans); err != nil {
		t.Fatalf("fk worktrees->projects: %v", err)
	}
	if orphans != 0 {
		t.Fatalf("%d worktrees do seed ficaram sem projeto", orphans)
	}

	applied, err := Up(db)
	if err != nil {
		t.Fatalf("Up 2: %v", err)
	}
	if len(applied) != 0 {
		t.Fatalf("segunda execucao reaplicou %v", applied)
	}
	for table, before := range counts {
		var after int
		if err := db.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&after); err != nil {
			t.Fatalf("count 2 %s: %v", table, err)
		}
		if after != before {
			t.Fatalf("%s: %d linhas antes, %d depois da segunda execucao", table, before, after)
		}
	}

	version, err := CurrentVersion(db)
	if err != nil {
		t.Fatalf("CurrentVersion: %v", err)
	}
	if version < 1 {
		t.Fatalf("CurrentVersion = %d", version)
	}
	if err := Verify(db); err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if pending, err := Pending(db); err != nil || len(pending) != 0 {
		t.Fatalf("Pending = %v (err %v)", pending, err)
	}
}

func TestUpDetectaDriftEmMigrationAplicada(t *testing.T) {
	db := tempDB(t)
	if _, err := Up(db); err != nil {
		t.Fatalf("Up: %v", err)
	}
	if _, err := db.Exec(
		"UPDATE " + SchemaMigrationsTable + " SET checksum = 'deadbeef' WHERE version = 1"); err != nil {
		t.Fatalf("update checksum: %v", err)
	}
	_, err := Up(db)
	if err == nil {
		t.Fatal("Up aceitou um arquivo aplicado com checksum diferente")
	}
	if !strings.Contains(err.Error(), "drift") {
		t.Fatalf("erro inesperado: %v", err)
	}
	if err := Verify(db); err == nil {
		t.Fatal("Verify aceitou checksum divergente")
	}
}

func TestUpSobreHistoricoLegadoDaAPI(t *testing.T) {
	db := tempDB(t)
	// Formato antigo criado por internal/db (sem filename/checksum).
	if _, err := db.Exec(`CREATE TABLE ` + SchemaMigrationsTable + ` (
		version INTEGER PRIMARY KEY, name TEXT NOT NULL, applied_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP)`); err != nil {
		t.Fatalf("ddl legado: %v", err)
	}
	if _, err := db.Exec("INSERT INTO " + SchemaMigrationsTable + " (version, name) VALUES (9001, 'api_additive_columns')"); err != nil {
		t.Fatalf("insert legado: %v", err)
	}

	applied, err := Up(db)
	if err != nil {
		t.Fatalf("Up sobre historico legado: %v", err)
	}
	if len(applied) == 0 {
		t.Fatal("migrations nao aplicadas sobre o historico legado")
	}
	// A linha da API convive com as versoes de arquivo.
	var rows int
	if err := db.QueryRow("SELECT COUNT(*) FROM " + SchemaMigrationsTable + " WHERE version = 9001").Scan(&rows); err != nil {
		t.Fatalf("count 9001: %v", err)
	}
	if rows != 1 {
		t.Fatalf("linha de bookkeeping da API sumiu")
	}
	version, err := CurrentVersion(db)
	if err != nil {
		t.Fatalf("CurrentVersion: %v", err)
	}
	all, _ := Load()
	if version != all[len(all)-1].Version {
		t.Fatalf("CurrentVersion = %d, esperado %d", version, all[len(all)-1].Version)
	}
	if err := Verify(db); err != nil {
		t.Fatalf("Verify: %v", err)
	}
}

func TestUpEmBancoJaPopuladoPeloMigratePy(t *testing.T) {
	db := tempDB(t)
	// Simula o banco do T5: schema aplicado e dados importados, sem historico.
	all, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if _, err := db.Exec(all[0].SQL); err != nil {
		t.Fatalf("aplicando schema cru: %v", err)
	}
	if _, err := db.Exec(
		"INSERT INTO projects (id, slug, name, status, primary_path) VALUES (1, 'ed2ti-website-new', 'Ed2ti Website New', 'active', '/root/projetos/itscwf/ed2ti-website-new')"); err != nil {
		t.Fatalf("insert projeto existente: %v", err)
	}

	if _, err := Up(db); err != nil {
		t.Fatalf("Up sobre banco ja populado: %v", err)
	}
	var name, path string
	if err := db.QueryRow("SELECT name, primary_path FROM projects WHERE id = 1").Scan(&name, &path); err != nil {
		t.Fatalf("select: %v", err)
	}
	if name != "Ed2ti Website New" || path != "/root/projetos/itscwf/ed2ti-website-new" {
		t.Fatalf("seed sobrescreveu dado existente: %q %q", name, path)
	}
	if err := Verify(db); err != nil {
		t.Fatalf("Verify: %v", err)
	}
}

func TestMissingTablesListaAusentes(t *testing.T) {
	db := tempDB(t)
	missing, err := MissingTables(db)
	if err != nil {
		t.Fatalf("MissingTables: %v", err)
	}
	if len(missing) != len(RequiredTables) {
		t.Fatalf("banco vazio deveria ter %d tabelas ausentes, veio %d", len(RequiredTables), len(missing))
	}
}
