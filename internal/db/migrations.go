package db

import (
	"fmt"
	"log/slog"
	"strings"

	"gorm.io/gorm"

	"github.com/itscwf/itscwf-fabrica-new/internal/models"
)

// additiveColumn descreve uma coluna que a API precisa e que pode faltar num
// banco criado por uma versao anterior do DDL.
//
// Todas elas JA estao declaradas em migrations/0001_schema.sql (banco novo
// nasce completo). Esta lista existe para o banco ANTIGO, criado pelo
// scripts/schema_init.sql do T5: o CREATE TABLE IF NOT EXISTS nao altera tabela
// existente e o SQLite nao tem ADD COLUMN IF NOT EXISTS, entao quem completa
// essas colunas e o boot do servidor. Ver docs/SCHEMA.md.
type additiveColumn struct {
	Table  string
	Name   string
	DDL    string // tipo + constraints acrescentados ao ALTER TABLE
	Reason string
}

// additiveColumns e a lista completa das colunas aditivas da API. Todas sao
// anulaveis, portanto o ALTER TABLE nunca quebra uma tabela com dados.
var additiveColumns = []additiveColumn{
	{"projects", "kanban_board", "TEXT", "board Kanban vinculado ao projeto (1:1)"},
	{"projects", "tech_stack", "TEXT", "stack tecnologica (frontend ProjectsPage)"},
	{"projects", "last_activity", "TEXT", "ultima atividade exibida no dashboard"},

	{"repos", "status", "TEXT", "status do repo na UI"},
	{"repos", "last_activity", "TEXT", "ultima atividade do repo"},

	{"worktrees", "name", "TEXT", "rotulo do worktree na UI"},
	{"worktrees", "last_commit", "TEXT", "commit de HEAD (UI de detalhe do projeto)"},
	{"worktrees", "last_activity", "TEXT", "ultima atividade do worktree"},
	{"worktrees", "status", "TEXT", "status do worktree na UI"},

	{"eca_registry", "project_id", "INTEGER", "projeto dono da ECA (filtro ?project_id=)"},

	{"qa_executions", "duration_ms", "INTEGER", "duracao da execucao"},
	{"qa_executions", "execution_id", "TEXT", "identificador legivel da execucao"},
	{"qa_executions", "updated_at", "TEXT", "auditoria da API"},
	{"qa_executions", "deleted_at", "TEXT", "soft delete da API"},

	{"cron_executions", "exit_code", "INTEGER", "exit code do processo"},
	{"cron_executions", "output", "TEXT", "saida do job (log viewer)"},
	{"cron_executions", "updated_at", "TEXT", "auditoria da API"},
	{"cron_executions", "deleted_at", "TEXT", "soft delete da API"},

	{"agents", "tools", "TEXT", "ferramentas habilitadas do perfil"},
	{"providers", "api_key_ref", "TEXT", "referencia (nunca o valor) da chave"},
	{"providers", "status", "TEXT", "status operacional do provider"},
	{"servers", "services", "TEXT", "servicos rodando no host"},
	{"servers", "last_check", "TEXT", "ultima verificacao de saude"},

	{"activity_log", "project_id", "INTEGER", "projeto do evento (filtro do dashboard)"},
	{"activity_log", "updated_at", "TEXT", "auditoria da API"},
	{"activity_log", "deleted_at", "TEXT", "soft delete da API"},
}

// indexes da API criados de forma idempotente.
var additiveIndexes = []struct {
	Name  string
	Table string
	Cols  string
}{
	{"idx_api_qa_executions_cycle", "qa_executions", "cycle_id"},
	{"idx_api_qa_testcases_cycle", "qa_testcases", "cycle_id"},
	{"idx_api_activity_project", "activity_log", "project_id"},
	{"idx_api_worktrees_project", "worktrees", "project_id"},
	{"idx_api_cron_exec_status", "cron_executions", "status"},
	{"idx_api_qa_execution_id", "qa_executions", "execution_id"},
	{"idx_api_eca_project", "eca_registry", "project_id"},
	{"idx_api_providers_status", "providers", "status"},
}

// Migrate garante o schema do dominio.
//
// IMPORTANTE: o schema canonico (scripts/schema_init.sql) tem comentarios SQL
// dentro do CREATE TABLE, e o AutoMigrate do driver SQLite reconstroi a tabela
// interpretando esses comentarios como nomes de coluna (erro classico
// "table repos__temp has no column named etc"). Por isso a migracao da API
// NUNCA altera tabelas existentes pelo AutoMigrate:
//
//  1. tabelas ausentes sao criadas a partir dos modelos (banco novo);
//  2. tabelas existentes recebem apenas as colunas aditivas da API, via
//     ALTER TABLE ADD COLUMN condicionado a PRAGMA table_info;
//  3. indices adicionais entram com CREATE INDEX IF NOT EXISTS.
//
// O resultado e idempotente e nao destrutivo.
func Migrate(conn *gorm.DB) error {
	if conn == nil {
		return fmt.Errorf("db: handle nulo em Migrate")
	}

	if err := ensureMigrationsTable(conn); err != nil {
		return err
	}

	created, err := createMissingTables(conn)
	if err != nil {
		return err
	}
	if len(created) > 0 {
		slog.Info("db_tables_created", "tables", strings.Join(created, ","))
	}

	added, err := addMissingColumns(conn)
	if err != nil {
		return err
	}
	if len(added) > 0 {
		slog.Info("db_columns_added", "columns", strings.Join(added, ","))
	}

	if err := ensureIndexes(conn); err != nil {
		return err
	}
	return recordMigration(conn, "api_additive_columns", len(added))
}

// ensureMigrationsTable cria a tabela de controle usada pela migracao (T5).
func ensureMigrationsTable(conn *gorm.DB) error {
	const ddl = `
CREATE TABLE IF NOT EXISTS schema_migrations (
    version    INTEGER PRIMARY KEY,
    name       TEXT     NOT NULL,
    applied_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);`
	if err := conn.Exec(ddl).Error; err != nil {
		return fmt.Errorf("db: garantir schema_migrations: %w", err)
	}
	return nil
}

// createMissingTables cria apenas as tabelas que nao existem.
func createMissingTables(conn *gorm.DB) ([]string, error) {
	migrator := conn.Migrator()
	created := []string{}
	for _, model := range models.All() {
		table := tableNameOf(conn, model)
		if table == "" {
			continue
		}
		exists, err := TableExists(conn, table)
		if err != nil {
			return created, err
		}
		if exists {
			continue
		}
		if err := migrator.CreateTable(model); err != nil {
			return created, fmt.Errorf("db: criar tabela %s: %w", table, err)
		}
		created = append(created, table)
	}
	return created, nil
}

// addMissingColumns aplica as colunas aditivas quando ausentes.
func addMissingColumns(conn *gorm.DB) ([]string, error) {
	added := []string{}
	for _, column := range additiveColumns {
		exists, err := TableExists(conn, column.Table)
		if err != nil {
			return added, err
		}
		if !exists {
			continue
		}
		has, err := ColumnExists(conn, column.Table, column.Name)
		if err != nil {
			return added, err
		}
		if has {
			continue
		}
		statement := fmt.Sprintf("ALTER TABLE %s ADD COLUMN %s %s", column.Table, column.Name, column.DDL)
		if err := conn.Exec(statement).Error; err != nil {
			return added, fmt.Errorf("db: adicionar %s.%s: %w", column.Table, column.Name, err)
		}
		added = append(added, column.Table+"."+column.Name)
	}
	return added, nil
}

// ensureIndexes cria os indices adicionais da API.
func ensureIndexes(conn *gorm.DB) error {
	for _, index := range additiveIndexes {
		exists, err := TableExists(conn, index.Table)
		if err != nil {
			return err
		}
		if !exists {
			continue
		}
		statement := fmt.Sprintf("CREATE INDEX IF NOT EXISTS %s ON %s(%s)", index.Name, index.Table, index.Cols)
		if err := conn.Exec(statement).Error; err != nil {
			return fmt.Errorf("db: criar indice %s: %w", index.Name, err)
		}
	}
	return nil
}

// recordMigration registra a execucao na tabela de controle.
func recordMigration(conn *gorm.DB, name string, added int) error {
	statement := `INSERT INTO schema_migrations (version, name, applied_at)
		VALUES (?, ?, CURRENT_TIMESTAMP)
		ON CONFLICT(version) DO UPDATE SET name = excluded.name, applied_at = CURRENT_TIMESTAMP`
	if err := conn.Exec(statement, 9001, fmt.Sprintf("%s (%d colunas aditivas)", name, added)).Error; err != nil {
		// A tabela pode ter sido criada pelo T5 com outro formato: nao falhar a
		// subida por causa do bookkeeping.
		slog.Debug("db_migration_bookkeeping_skipped", "error", err)
	}
	return nil
}

// TableExists consulta sqlite_master (nao interpreta o DDL, portanto e imune a
// comentarios SQL no CREATE TABLE).
func TableExists(conn *gorm.DB, table string) (bool, error) {
	var count int64
	if err := conn.Raw("SELECT count(*) FROM sqlite_master WHERE type = 'table' AND name = ?", table).Scan(&count).Error; err != nil {
		return false, fmt.Errorf("db: consultar tabela %s: %w", table, err)
	}
	return count > 0, nil
}

// ColumnExists usa PRAGMA table_info.
func ColumnExists(conn *gorm.DB, table, column string) (bool, error) {
	if ok, err := TableExists(conn, table); err != nil || !ok {
		return false, err
	}
	type columnInfo struct {
		Name string
	}
	rows := []columnInfo{}
	if err := conn.Raw("PRAGMA table_info(" + table + ")").Scan(&rows).Error; err != nil {
		return false, fmt.Errorf("db: inspecionar %s: %w", table, err)
	}
	for _, row := range rows {
		if strings.EqualFold(row.Name, column) {
			return true, nil
		}
	}
	return false, nil
}

// tableNameOf resolve o nome da tabela a partir do modelo.
func tableNameOf(conn *gorm.DB, model any) string {
	statement := &gorm.Statement{DB: conn}
	if err := statement.Parse(model); err != nil {
		return ""
	}
	return statement.Schema.Table
}
