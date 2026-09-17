package db

import (
	"path/filepath"
	"testing"

	"github.com/itscwf/itscwf-fabrica-new/migrations"
)

// TestCanonicalSchemaCobreTodosOsRecursosDaAPI prova que o schema canonico
// publicado em migrations/*.sql ja contem TUDO que os modelos da API usam.
//
// E a defesa contra o modo de falha classico deste repo: a API criava as
// colunas que faltavam com ALTER TABLE em runtime, entao banco novo e banco
// migrado divergiam (projects.tech_stack nascia so em producao, e a coluna
// Stack do dashboard saia vazia em banco criado do zero). Se este teste
// falhar, falta declarar a coluna na migration 0001_schema.sql — nao basta
// confiar no additiveColumns.
func TestCanonicalSchemaCobreTodosOsRecursosDaAPI(t *testing.T) {
	path := filepath.Join(t.TempDir(), "canonical-api.db")

	raw, err := migrations.OpenDB(migrations.Options{Path: path})
	if err != nil {
		t.Fatalf("OpenDB: %v", err)
	}
	if _, err := migrations.Up(raw); err != nil {
		_ = raw.Close()
		t.Fatalf("Up: %v", err)
	}
	if missing, err := migrations.MissingTables(raw); err != nil {
		_ = raw.Close()
		t.Fatalf("MissingTables: %v", err)
	} else if len(missing) > 0 {
		_ = raw.Close()
		t.Fatalf("tabelas obrigatorias ausentes: %v", missing)
	}
	if err := raw.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	conn, err := Open(Options{Path: path, LogLevel: "silent"})
	if err != nil {
		t.Fatalf("open gorm: %v", err)
	}
	defer Close(conn)

	created, err := createMissingTables(conn)
	if err != nil {
		t.Fatalf("createMissingTables: %v", err)
	}
	if len(created) > 0 {
		t.Fatalf("schema canonico sem estas tabelas dos modelos: %v", created)
	}

	added, err := addMissingColumns(conn)
	if err != nil {
		t.Fatalf("addMissingColumns: %v", err)
	}
	if len(added) > 0 {
		t.Fatalf("schema canonico nao cobre a API — colunas que o ALTER precisou criar: %v", added)
	}
}

// TestTechStackDisponivelEmBancoNovo e o teste do achado do T4: a coluna
// projects.tech_stack (a que alimenta a coluna Stack do dashboard) tem que
// existir num banco criado apenas pelo schema canonico.
func TestTechStackDisponivelEmBancoNovo(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tech-stack.db")
	raw, err := migrations.OpenDB(migrations.Options{Path: path})
	if err != nil {
		t.Fatalf("OpenDB: %v", err)
	}
	if _, err := migrations.Up(raw); err != nil {
		_ = raw.Close()
		t.Fatalf("Up: %v", err)
	}
	ok, err := migrations.DBHasColumn(raw, "projects", "tech_stack")
	_ = raw.Close()
	if err != nil {
		t.Fatalf("DBHasColumn: %v", err)
	}
	if !ok {
		t.Fatal("projects.tech_stack ausente no schema canonico (achado do T4)")
	}
}
