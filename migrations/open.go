package migrations

import (
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	// Driver SQLite (mesmo do gorm.io/driver/sqlite) — necessário para quem usa
	// OpenDB/EnsureSchema sem passar pelo GORM.
	_ "github.com/mattn/go-sqlite3"
)

// Options controla a conexao aberta por OpenDB.
type Options struct {
	// Path do arquivo SQLite (ou ":memory:").
	Path string
	// BusyTimeout espera por lock antes de devolver SQLITE_BUSY (default 5s).
	BusyTimeout time.Duration
	// ReadOnly abre em modo somente leitura (uri mode=ro).
	ReadOnly bool
}

// OpenDB abre o banco com os mesmos pragmas do servidor: foreign_keys ON, WAL e
// busy_timeout. Uma unica conexao, como no internal/db: SQLite aceita um unico
// escritor e conexoes concorrentes geram SQLITE_BUSY.
func OpenDB(opts Options) (*sql.DB, error) {
	path := strings.TrimSpace(opts.Path)
	if path == "" {
		return nil, fmt.Errorf("migrations: caminho do banco nao pode ser vazio")
	}
	if opts.BusyTimeout <= 0 {
		opts.BusyTimeout = 5 * time.Second
	}
	if path != ":memory:" && !strings.HasPrefix(path, "file:") {
		if dir := filepath.Dir(path); dir != "" && dir != "." {
			if err := os.MkdirAll(dir, 0o755); err != nil {
				return nil, fmt.Errorf("migrations: criar diretorio %q: %w", dir, err)
			}
		}
	}

	params := url.Values{}
	params.Set("_foreign_keys", "on")
	params.Set("_busy_timeout", fmt.Sprintf("%d", opts.BusyTimeout.Milliseconds()))
	params.Set("_loc", "auto")
	if opts.ReadOnly {
		params.Set("mode", "ro")
	}

	var dsn string
	switch {
	case path == ":memory:" || strings.HasPrefix(path, "file::memory:"):
		params.Set("cache", "shared")
		dsn = "file::memory:?" + params.Encode()
	case strings.HasPrefix(path, "file:"):
		dsn = path + "?" + params.Encode()
	default:
		if !opts.ReadOnly {
			params.Set("_journal_mode", "WAL")
			params.Set("_synchronous", "NORMAL")
		}
		dsn = "file:" + path + "?" + params.Encode()
	}

	db, err := sql.Open("sqlite3", dsn)
	if err != nil {
		return nil, fmt.Errorf("migrations: abrir %q: %w", path, err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	db.SetConnMaxLifetime(time.Hour)
	if err := db.Ping(); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("migrations: ping %q: %w", path, err)
	}
	return db, nil
}

// EnsureSchema e o atalho de quem so tem o caminho do banco: abre, aplica as
// migrations pendentes, valida as tabelas obrigatorias e devolve a conexao
// aberta (o chamador fecha).
//
// E o mesmo fluxo que o cmd/server executa no boot; aqui existe para scripts,
// CI e para o modo --migrate-only.
func EnsureSchema(path string) (*sql.DB, []string, error) {
	db, err := OpenDB(Options{Path: path})
	if err != nil {
		return nil, nil, err
	}
	applied, err := Up(db)
	if err != nil {
		_ = db.Close()
		return nil, applied, err
	}
	missing, err := MissingTables(db)
	if err != nil {
		_ = db.Close()
		return nil, applied, err
	}
	if len(missing) > 0 {
		_ = db.Close()
		return nil, applied, fmt.Errorf("migrations: schema incompativel — tabelas ausentes: %s", strings.Join(missing, ", "))
	}
	return db, applied, nil
}
