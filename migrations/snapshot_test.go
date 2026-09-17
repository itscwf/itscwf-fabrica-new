package migrations

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// snapshotPath acha scripts/schema_init.sql subindo da pasta do pacote
// (migrations/) ate a raiz do modulo.
func snapshotPath(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	for {
		candidate := filepath.Join(dir, "scripts", "schema_init.sql")
		if _, err := os.Stat(candidate); err == nil {
			return candidate
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatalf("scripts/schema_init.sql nao encontrado a partir de %s", dir)
		}
		dir = parent
	}
}

// TestSchemaSnapshotInSync garante que o espelho scripts/schema_init.sql (lido
// pelos testes da API e pelo fallback do scripts/migrate.py) nao divirja das
// migrations, que sao a fonte unica da verdade.
//
// Regenere com: python3 scripts/gen_schema_snapshot.py
func TestSchemaSnapshotInSync(t *testing.T) {
	raw, err := os.ReadFile(snapshotPath(t))
	if err != nil {
		t.Fatalf("lendo espelho: %v", err)
	}
	snapshot := string(raw)

	all, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	previous := -1
	for _, m := range all {
		if SnapshotExcluded(m) {
			continue
		}
		body := strings.TrimRight(m.SQL, "\n")
		index := strings.Index(snapshot, body)
		if index < 0 {
			t.Errorf("espelho desatualizado: conteudo de %s ausente — rode python3 scripts/gen_schema_snapshot.py", m.Filename)
			continue
		}
		if index < previous {
			t.Errorf("espelho fora de ordem: %s aparece fora da sequencia", m.Filename)
		}
		if !strings.Contains(snapshot, m.Filename) {
			t.Errorf("espelho nao cita o arquivo %s", m.Filename)
		}
		previous = index
	}

	// Toda tabela do espelho precisa vir das migrations (nenhum DDL avulso).
	tables, err := Tables()
	if err != nil {
		t.Fatalf("Tables: %v", err)
	}
	for _, table := range tables {
		if !strings.Contains(snapshot, "CREATE TABLE IF NOT EXISTS "+table) {
			t.Errorf("espelho sem o CREATE TABLE de %s", table)
		}
	}
}
