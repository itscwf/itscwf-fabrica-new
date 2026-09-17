package db

import (
	"path/filepath"
	"testing"
	"time"
)

func TestOpenEnablesWALAndCreatesSchema(t *testing.T) {
	path := filepath.Join(t.TempDir(), "fabrica.db")

	conn, err := Open(Options{Path: path, BusyTimeout: 5 * time.Second, MaxOpenConns: 1, LogLevel: "silent"})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer func() {
		if err := Close(conn); err != nil {
			t.Fatalf("close: %v", err)
		}
	}()

	mode, err := JournalMode(conn)
	if err != nil {
		t.Fatalf("journal mode: %v", err)
	}
	if mode != "wal" {
		t.Fatalf("journal_mode = %q, want wal", mode)
	}

	if err := Migrate(conn); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	// Idempotent: a second run must not fail.
	if err := Migrate(conn); err != nil {
		t.Fatalf("second migrate: %v", err)
	}

	want := []string{
		"projects", "repos", "worktrees", "eca_registry", "qa_cycles", "qa_testcases",
		"qa_executions", "cron_jobs", "cron_executions", "agents", "providers", "servers", "activity_log",
	}
	var names []string
	if err := conn.Raw("SELECT name FROM sqlite_master WHERE type = 'table' AND name NOT LIKE 'sqlite_%'").Scan(&names).Error; err != nil {
		t.Fatalf("list tables: %v", err)
	}
	present := map[string]bool{}
	for _, name := range names {
		present[name] = true
	}
	for _, table := range want {
		if !present[table] {
			t.Fatalf("table %q missing (have %v)", table, names)
		}
	}

	// Every table must carry the soft-delete column required by the spec.
	type columnInfo struct {
		Name string
	}
	for _, table := range want {
		rows := []columnInfo{}
		if err := conn.Raw("PRAGMA table_info(" + table + ")").Scan(&rows).Error; err != nil {
			t.Fatalf("table_info(%s): %v", table, err)
		}
		columns := make([]string, 0, len(rows))
		for _, row := range rows {
			columns = append(columns, row.Name)
		}
		if !contains(columns, "deleted_at") {
			t.Fatalf("table %s has no deleted_at column (columns: %v)", table, columns)
		}
	}
}

func TestOpenCreatesMissingDirectory(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "deeper", "fabrica.db")
	conn, err := Open(Options{Path: path, LogLevel: "silent"})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer Close(conn)
	if err := Migrate(conn); err != nil {
		t.Fatalf("migrate: %v", err)
	}
}

func TestOpenRejectsEmptyPath(t *testing.T) {
	if _, err := Open(Options{Path: "  "}); err == nil {
		t.Fatalf("expected an error for an empty path")
	}
}

func contains(values []string, needle string) bool {
	for _, value := range values {
		if value == needle {
			return true
		}
	}
	return false
}
