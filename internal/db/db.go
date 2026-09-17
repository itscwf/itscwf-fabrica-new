// Package db abre e mantem a conexao SQLite da Fabrica ITSCWF (WAL mode) e
// aplica o schema do dominio.
package db

import (
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// MemoryDSN identifica um banco em memoria (util em testes).
const MemoryDSN = ":memory:"

// Options ajusta a conexao.
type Options struct {
	Path         string
	BusyTimeout  time.Duration
	MaxOpenConns int
	LogLevel     string
}

// Open cria (se necessario) e abre o banco SQLite em path, com WAL, foreign
// keys e busy timeout habilitados, e devolve uma conexao GORM.
func Open(opts Options) (*gorm.DB, error) {
	return OpenWith(opts)
}

// OpenPath e o atalho usado pelo servidor: Open({Path: path}).
func OpenPath(path string) (*gorm.DB, error) {
	return Open(Options{Path: path})
}

// OpenWith abre o banco com opcoes completas.
func OpenWith(opts Options) (*gorm.DB, error) {
	if strings.TrimSpace(opts.Path) == "" {
		return nil, fmt.Errorf("db: caminho do banco nao pode ser vazio")
	}
	if opts.BusyTimeout <= 0 {
		opts.BusyTimeout = 5 * time.Second
	}
	if opts.MaxOpenConns <= 0 {
		// SQLite aceita um unico escritor: uma conexao evita SQLITE_BUSY.
		opts.MaxOpenConns = 1
	}
	if opts.Path != MemoryDSN && !strings.HasPrefix(opts.Path, "file:") {
		if dir := filepath.Dir(opts.Path); dir != "" && dir != "." {
			if err := os.MkdirAll(dir, 0o755); err != nil {
				return nil, fmt.Errorf("db: criar diretorio %q: %w", dir, err)
			}
		}
	}

	cfg := &gorm.Config{
		Logger:  newLogger(opts.LogLevel),
		NowFunc: func() time.Time { return time.Now().UTC() },
	}
	conn, err := gorm.Open(sqlite.Open(buildDSN(opts)), cfg)
	if err != nil {
		return nil, fmt.Errorf("db: abrir %q: %w", opts.Path, err)
	}

	sqlDB, err := conn.DB()
	if err != nil {
		return nil, fmt.Errorf("db: handle de %q: %w", opts.Path, err)
	}
	sqlDB.SetMaxOpenConns(opts.MaxOpenConns)
	sqlDB.SetMaxIdleConns(opts.MaxOpenConns)
	sqlDB.SetConnMaxLifetime(time.Hour)

	if err := sqlDB.Ping(); err != nil {
		_ = sqlDB.Close()
		return nil, fmt.Errorf("db: ping %q: %w", opts.Path, err)
	}

	mode, err := JournalMode(conn)
	if err != nil {
		return nil, err
	}
	if !strings.EqualFold(mode, "wal") && opts.Path != MemoryDSN {
		if err := conn.Exec("PRAGMA journal_mode=WAL;").Error; err != nil {
			return nil, fmt.Errorf("db: habilitar WAL: %w", err)
		}
		if mode, err = JournalMode(conn); err != nil {
			return nil, err
		}
		if !strings.EqualFold(mode, "wal") {
			return nil, fmt.Errorf("db: journal_mode=%q, esperado wal", mode)
		}
	}
	return conn, nil
}

// Migrate vive em migrations.go: ele cria apenas tabelas ausentes e adiciona
// colunas aditivas, para nunca sobrescrever o schema canonico.

// JournalMode devolve o modo de journal ativo no SQLite.
func JournalMode(conn *gorm.DB) (string, error) {
	var mode string
	if err := conn.Raw("PRAGMA journal_mode;").Scan(&mode).Error; err != nil {
		return "", fmt.Errorf("db: ler journal_mode: %w", err)
	}
	return strings.TrimSpace(mode), nil
}

// Close fecha a conexao.
func Close(conn *gorm.DB) error {
	if conn == nil {
		return nil
	}
	sqlDB, err := conn.DB()
	if err != nil {
		return err
	}
	return sqlDB.Close()
}

// buildDSN monta o DSN do mattn/go-sqlite3 com os pragmas exigidos.
func buildDSN(opts Options) string {
	params := url.Values{}
	params.Set("_foreign_keys", "on")
	params.Set("_busy_timeout", fmt.Sprintf("%d", opts.BusyTimeout.Milliseconds()))
	params.Set("_loc", "auto")

	if opts.Path == MemoryDSN || strings.HasPrefix(opts.Path, "file::memory:") {
		params.Set("cache", "shared")
		return "file::memory:?" + params.Encode()
	}
	if strings.HasPrefix(opts.Path, "file:") {
		return opts.Path + "?" + params.Encode()
	}
	params.Set("_journal_mode", "WAL")
	params.Set("_synchronous", "NORMAL")
	return "file:" + opts.Path + "?" + params.Encode()
}

func newLogger(level string) logger.Interface {
	cfg := logger.Config{SlowThreshold: 300 * time.Millisecond, LogLevel: logger.Warn, Colorful: false}
	switch strings.ToLower(level) {
	case "silent":
		cfg.LogLevel = logger.Silent
	case "error":
		cfg.LogLevel = logger.Error
	case "warn", "":
		cfg.LogLevel = logger.Warn
	case "info", "debug":
		cfg.LogLevel = logger.Info
	}
	writer := &slogWriter{}
	return logger.New(writer, cfg)
}

// slogWriter encaminha os logs do GORM para o slog.
type slogWriter struct{}

func (slogWriter) Printf(format string, args ...any) {
	slog.Debug("gorm", "msg", fmt.Sprintf(format, args...))
}
