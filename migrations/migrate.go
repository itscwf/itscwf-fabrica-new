// Package migrations carrega, versiona e aplica o schema do banco da Fabrica
// ITSCWF (itscwf_fabrica.db).
//
// Os arquivos migrations/NNNN_nome.sql sao a FONTE UNICA DA VERDADE do modelo
// de dados: sao embutidos no binario via embed.FS e aplicados em ordem
// crescente de versao, cada um dentro da sua propria transacao. O historico
// fica em schema_migrations com o checksum sha256 do arquivo — se um arquivo
// ja aplicado for editado depois, Up falha com erro de "drift" em vez de
// aplicar um schema divergente.
//
// O mesmo diretorio e consumido pelo scripts/migrate.py (que le
// migrations/*.sql antes do fallback scripts/schema_init.sql), entao o DDL
// vale tanto para o servidor Go quanto para a importacao dos JSONs legados.
//
// Uso tipico (cmd/server, sobre a conexao que o GORM ja abriu):
//
//	raw, _ := gormDB.DB()
//	applied, err := migrations.Up(raw)   // aplica o schema canonico
//	if err != nil { ... }
//	db.Migrate(gormDB)                   // depois, as colunas aditivas da API
//
// Ou, quando o chamador tem apenas o caminho do banco:
//
//	conn, err := migrations.OpenDB(dbPath) // WAL + foreign_keys + busy_timeout
//	defer conn.Close()
//	applied, err := migrations.Up(conn)
//
// Requisito do driver: Exec de varias instrucoes separadas por ";" num unico
// comando (mattn/go-sqlite3, modernc.org/sqlite e glebarez/sqlite atendem).
package migrations

import (
	"crypto/sha256"
	"database/sql"
	"embed"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

//go:embed *.sql
var embedded embed.FS

// SchemaMigrationsTable guarda o historico das migrations aplicadas.
const SchemaMigrationsTable = "schema_migrations"

// apiBookkeepingVersion e o namespace das versoes gravadas pela migracao da
// API (internal/db), fora da sequencia de arquivos. Existe para o historico de
// dois escritores conviver: a API registra o que fez, e o seed de arquivos usa
// 1..N.
const apiBookkeepingVersion = 9000

// snapshotExcludeMarker marca (no proprio arquivo SQL) a migration que nao
// entra no espelho scripts/schema_init.sql. Mesmo literal de
// scripts/gen_schema_snapshot.py.
const snapshotExcludeMarker = "snapshot: exclude"

// RequiredTables sao as tabelas que o banco precisa ter para a API, a
// importacao (scripts/migrate.py) e o dashboard funcionarem. Mesma lista de
// REQUIRED_TABLES em scripts/migrate.py.
var RequiredTables = []string{
	"projects", "repos", "worktrees",
	"eca_registry", "eca_steps", "eca_dependencies",
	"qa_cycles", "qa_cycle_sources", "qa_testcases", "qa_executions",
	"cron_jobs", "cron_executions",
	"agents", "providers", "servers",
	"activity_log",
}

// Migration e um arquivo SQL versionado embutido no binario.
type Migration struct {
	Version  int    // 1, 2, 3... (prefixo do nome do arquivo)
	Name     string // parte textual do nome: "schema", "seed"
	Filename string // "0001_schema.sql"
	SQL      string // conteudo integral do arquivo
	Checksum string // sha256 (hex) do conteudo — detecta edicao pos-aplicacao
}

// AppliedMigration e uma linha de schema_migrations.
type AppliedMigration struct {
	Version   int
	Name      string
	Filename  string
	Checksum  string
	AppliedAt string
}

// filenameRE casa o padrao exigido: 4 digitos + underscore + nome .sql
var filenameRE = regexp.MustCompile(`^(\d{4})_([a-z0-9_]+)\.sql$`)

// Load le, valida e ordena as migrations embutidas.
// Retorna erro se houver nome fora do padrao NNNN_nome.sql ou versao duplicada.
func Load() ([]Migration, error) {
	entries, err := fs.ReadDir(embedded, ".")
	if err != nil {
		return nil, fmt.Errorf("migrations: lendo embed.FS: %w", err)
	}

	out := make([]Migration, 0, len(entries))
	seen := make(map[int]string, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sql") {
			continue
		}
		filename := entry.Name()
		parts := filenameRE.FindStringSubmatch(filename)
		if parts == nil {
			return nil, fmt.Errorf("migrations: nome invalido %q (esperado NNNN_nome.sql)", filename)
		}
		raw, err := embedded.ReadFile(filename)
		if err != nil {
			return nil, fmt.Errorf("migrations: lendo %s: %w", filename, err)
		}
		version, err := strconv.Atoi(parts[1])
		if err != nil {
			return nil, fmt.Errorf("migrations: versao invalida em %q: %w", filename, err)
		}
		if prev, dup := seen[version]; dup {
			return nil, fmt.Errorf("migrations: versao %d duplicada (%s e %s)", version, prev, filename)
		}
		seen[version] = filename

		sum := sha256.Sum256(raw)
		out = append(out, Migration{
			Version:  version,
			Name:     parts[2],
			Filename: filename,
			SQL:      string(raw),
			Checksum: hex.EncodeToString(sum[:]),
		})
	}

	if len(out) == 0 {
		return nil, errors.New("migrations: nenhum arquivo .sql encontrado no pacote")
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Version < out[j].Version })
	return out, nil
}

// Up aplica todas as migrations pendentes, em ordem, cada uma em uma
// transacao. Devolve os nomes dos arquivos efetivamente aplicados (vazio se o
// banco ja estava atualizado). Reaplicar e no-op; um arquivo ja aplicado que
// mudou de conteudo gera erro de drift.
func Up(db *sql.DB) ([]string, error) {
	if db == nil {
		return nil, errors.New("migrations: db nil")
	}
	all, err := Load()
	if err != nil {
		return nil, err
	}
	if err := ensureMigrationsTable(db); err != nil {
		return nil, err
	}
	applied, err := Applied(db)
	if err != nil {
		return nil, err
	}

	appliedNow := make([]string, 0, len(all))
	for _, m := range all {
		if prev, ok := applied[m.Version]; ok {
			if prev.Checksum != m.Checksum {
				return appliedNow, fmt.Errorf(
					"migrations: drift detectado em %s (aplicada em %s com checksum %s, arquivo atual tem %s) — crie uma nova migration em vez de editar uma ja aplicada",
					m.Filename, prev.AppliedAt, short(prev.Checksum), short(m.Checksum))
			}
			continue
		}
		if err := applyOne(db, m); err != nil {
			return appliedNow, err
		}
		appliedNow = append(appliedNow, m.Filename)
	}
	return appliedNow, nil
}

// applyOne executa uma migration e registra a versao na mesma transacao: ou o
// schema e o historico avancam juntos, ou nada muda.
func applyOne(db *sql.DB, m Migration) error {
	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("migrations: begin %s: %w", m.Filename, err)
	}
	defer func() { _ = tx.Rollback() }() // no-op depois do Commit

	if _, err := tx.Exec(m.SQL); err != nil {
		return fmt.Errorf("migrations: aplicando %s: %w", m.Filename, err)
	}
	if _, err := tx.Exec(
		"INSERT INTO "+SchemaMigrationsTable+" (version, name, filename, checksum) VALUES (?, ?, ?, ?)",
		m.Version, m.Name, m.Filename, m.Checksum,
	); err != nil {
		return fmt.Errorf("migrations: registrando %s: %w", m.Filename, err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("migrations: commit %s: %w", m.Filename, err)
	}
	return nil
}

// ensureMigrationsTable cria o historico no formato canonico e, quando a
// tabela ja existe num formato antigo (a API criava so version/name/applied_at),
// acrescenta as colunas que faltam. Sem isso, um banco que ja subiu pelo
// servidor antigo recusaria as migrations.
func ensureMigrationsTable(db *sql.DB) error {
	const ddl = `
CREATE TABLE IF NOT EXISTS ` + SchemaMigrationsTable + ` (
    version    INTEGER PRIMARY KEY,
    name       TEXT NOT NULL,
    filename   TEXT NOT NULL DEFAULT '',
    checksum   TEXT NOT NULL DEFAULT '',
    applied_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ','now'))
);`
	if _, err := db.Exec(ddl); err != nil {
		return fmt.Errorf("migrations: criando %s: %w", SchemaMigrationsTable, err)
	}
	// Formato legado: acrescenta as colunas ausentes com default.
	for _, column := range []string{"filename", "checksum", "applied_at"} {
		exists, err := DBHasColumn(db, SchemaMigrationsTable, column)
		if err != nil {
			return err
		}
		if exists {
			continue
		}
		stmt := "ALTER TABLE " + SchemaMigrationsTable + " ADD COLUMN " + column + " TEXT NOT NULL DEFAULT ''"
		if _, err := db.Exec(stmt); err != nil {
			return fmt.Errorf("migrations: atualizando %s.%s: %w", SchemaMigrationsTable, column, err)
		}
	}
	return nil
}

// Applied devolve o historico de migrations aplicadas, indexado por versao.
// Retorna mapa vazio (sem erro) se o historico ainda nao existe.
func Applied(db *sql.DB) (map[int]AppliedMigration, error) {
	if db == nil {
		return nil, errors.New("migrations: db nil")
	}
	rows, err := db.Query(
		"SELECT version, name, filename, checksum, applied_at FROM " + SchemaMigrationsTable + " ORDER BY version")
	if err != nil {
		if isMissingTable(err) {
			return map[int]AppliedMigration{}, nil
		}
		return nil, fmt.Errorf("migrations: lendo %s: %w", SchemaMigrationsTable, err)
	}
	defer rows.Close()

	out := make(map[int]AppliedMigration)
	for rows.Next() {
		var a AppliedMigration
		if err := rows.Scan(&a.Version, &a.Name, &a.Filename, &a.Checksum, &a.AppliedAt); err != nil {
			return nil, fmt.Errorf("migrations: scan %s: %w", SchemaMigrationsTable, err)
		}
		out[a.Version] = a
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("migrations: iterando %s: %w", SchemaMigrationsTable, err)
	}
	return out, nil
}

// CurrentVersion devolve a maior versao de ARQUIVO aplicada (0 se nenhuma).
// As versoes de bookkeeping da API (>= 9000) sao ignoradas.
func CurrentVersion(db *sql.DB) (int, error) {
	if db == nil {
		return 0, errors.New("migrations: db nil")
	}
	var version sql.NullInt64
	err := db.QueryRow(
		"SELECT MAX(version) FROM "+SchemaMigrationsTable+" WHERE version < ?", apiBookkeepingVersion).Scan(&version)
	if err != nil {
		if isMissingTable(err) {
			return 0, nil
		}
		return 0, fmt.Errorf("migrations: lendo versao: %w", err)
	}
	if !version.Valid {
		return 0, nil
	}
	return int(version.Int64), nil
}

// Pending devolve as migrations ainda nao aplicadas, em ordem de versao.
func Pending(db *sql.DB) ([]Migration, error) {
	all, err := Load()
	if err != nil {
		return nil, err
	}
	applied, err := Applied(db)
	if err != nil {
		return nil, err
	}
	out := make([]Migration, 0, len(all))
	for _, m := range all {
		if _, ok := applied[m.Version]; !ok {
			out = append(out, m)
		}
	}
	return out, nil
}

// Verify confere se as migrations aplicadas batem com os arquivos embutidos
// (mesmos checksums) e se nao ha buracos na sequencia de versoes. Pensado para
// CI/health check: nao altera nada.
//
// Versoes de bookkeeping da API (>= 9000) sao ignoradas de proposito: quem
// escreve nelas e internal/db, nao o embed.FS.
func Verify(db *sql.DB) error {
	all, err := Load()
	if err != nil {
		return err
	}
	applied, err := Applied(db)
	if err != nil {
		return err
	}
	byVersion := make(map[int]Migration, len(all))
	for _, m := range all {
		byVersion[m.Version] = m
	}
	for version, a := range applied {
		if version >= apiBookkeepingVersion {
			continue
		}
		m, ok := byVersion[version]
		if !ok {
			return fmt.Errorf("migrations: versao %d (%s) aplicada no banco mas ausente no binario", version, a.Filename)
		}
		if m.Checksum != a.Checksum {
			return fmt.Errorf("migrations: checksum divergente em %s (banco %s != binario %s)",
				a.Filename, short(a.Checksum), short(m.Checksum))
		}
	}
	return nil
}

// Tables lista as tabelas de dominio do schema canonico (fora o historico).
// Derivada dos arquivos embutidos: nao toca no banco.
func Tables() ([]string, error) {
	all, err := Load()
	if err != nil {
		return nil, err
	}
	re := regexp.MustCompile(`(?im)^\s*CREATE\s+TABLE\s+(?:IF\s+NOT\s+EXISTS\s+)?([a-z_][a-z0-9_]*)`)
	var (
		out  []string
		seen = map[string]bool{SchemaMigrationsTable: true}
	)
	for _, m := range all {
		for _, match := range re.FindAllStringSubmatch(m.SQL, -1) {
			name := strings.ToLower(match[1])
			if seen[name] {
				continue
			}
			seen[name] = true
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out, nil
}

// SnapshotExcluded informa se a migration fica fora do espelho
// scripts/schema_init.sql. Migrations de dados marcam o proprio arquivo com
// "-- snapshot: exclude"; o espelho e lido pelos testes da API e pelo fallback
// do scripts/migrate.py, que precisam de DDL, nao de dados operacionais.
func SnapshotExcluded(m Migration) bool {
	return strings.Contains(m.SQL, snapshotExcludeMarker)
}

// MissingTables devolve as RequiredTables ausentes no banco. Vazio = ok.
func MissingTables(db *sql.DB) ([]string, error) {
	if db == nil {
		return nil, errors.New("migrations: db nil")
	}
	rows, err := db.Query("SELECT name FROM sqlite_master WHERE type = 'table'")
	if err != nil {
		return nil, fmt.Errorf("migrations: lendo sqlite_master: %w", err)
	}
	defer rows.Close()
	present := map[string]bool{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, fmt.Errorf("migrations: scan sqlite_master: %w", err)
		}
		present[name] = true
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("migrations: iterando sqlite_master: %w", err)
	}
	missing := []string{}
	for _, table := range RequiredTables {
		if !present[table] {
			missing = append(missing, table)
		}
	}
	return missing, nil
}

// DBHasColumn responde se a tabela tem a coluna (PRAGMA table_info).
func DBHasColumn(db *sql.DB, table, column string) (bool, error) {
	rows, err := db.Query("PRAGMA table_info(" + table + ")")
	if err != nil {
		return false, fmt.Errorf("migrations: inspecionando %s: %w", table, err)
	}
	defer rows.Close()
	columns, err := rows.Columns()
	if err != nil {
		return false, fmt.Errorf("migrations: colunas de %s: %w", table, err)
	}
	nameIndex := -1
	for i, name := range columns {
		if strings.EqualFold(name, "name") {
			nameIndex = i
		}
	}
	if nameIndex < 0 {
		return false, fmt.Errorf("migrations: PRAGMA table_info(%s) sem coluna name", table)
	}
	for rows.Next() {
		values := make([]any, len(columns))
		pointers := make([]any, len(columns))
		for i := range values {
			pointers[i] = &values[i]
		}
		if err := rows.Scan(pointers...); err != nil {
			return false, fmt.Errorf("migrations: scan table_info(%s): %w", table, err)
		}
		name, _ := values[nameIndex].(string)
		if strings.EqualFold(name, column) {
			return true, nil
		}
	}
	if err := rows.Err(); err != nil {
		return false, fmt.Errorf("migrations: iterando table_info(%s): %w", table, err)
	}
	return false, nil
}

func short(checksum string) string {
	if len(checksum) > 12 {
		return checksum[:12]
	}
	return checksum
}

func isMissingTable(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(strings.ToLower(err.Error()), "no such table")
}
