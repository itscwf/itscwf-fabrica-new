// Package models reune as entidades persistidas da Fabrica ITSCWF.
//
// O mapeamento espelha scripts/schema_init.sql, que e a referencia canonica do
// banco itscwf_fabrica.db: nomes de coluna em portugues nos casos historicos
// (titulo, inicio, proximo_tc...), timestamps em TEXT ISO-8601 UTC e
// deleted_at em TEXT para soft delete.
//
// Os nomes dos campos JSON seguem o contrato consumido pelo frontend
// (frontend/src/api/types.ts), por isso alguns campos expõem um nome em ingles
// apontando para uma coluna canonica diferente (ex.: local_path -> primary_path).
package models

import (
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/schema"
)

// Timestamp e um time.Time persistido como TEXT ISO-8601 (UTC), que e o formato
// usado pelo schema canonico. Sem isso o driver SQLite devolveria string e o
// scan em time.Time falharia.
type Timestamp struct {
	time.Time
}

// Layout e o formato canonico gravado no banco.
const Layout = "2006-01-02T15:04:05Z"

// Now devolve o instante atual em UTC.
func Now() Timestamp { return Timestamp{Time: time.Now().UTC().Truncate(time.Second)} }

// NewTimestamp normaliza um time.Time para UTC com precisao de segundo.
func NewTimestamp(t time.Time) Timestamp {
	return Timestamp{Time: t.UTC().Truncate(time.Second)}
}

// IsZero informa se o timestamp nao foi preenchido.
func (t Timestamp) IsZero() bool { return t.Time.IsZero() }

// GormDataType declara a coluna como texto.
func (t Timestamp) GormDataType() string { return "text" }

// GormDBDataType fixa TEXT para o dialeto SQLite.
func (t Timestamp) GormDBDataType(_ *gorm.DB, _ *schema.Field) string {
	return "TEXT"
}

// Scan implementa sql.Scanner aceitando texto, bytes, time.Time e numeros.
func (t *Timestamp) Scan(value any) error {
	switch typed := value.(type) {
	case nil:
		t.Time = time.Time{}
		return nil
	case time.Time:
		t.Time = typed.UTC()
		return nil
	case *time.Time:
		if typed == nil {
			t.Time = time.Time{}
			return nil
		}
		t.Time = typed.UTC()
		return nil
	case string:
		return t.parse(typed)
	case []byte:
		return t.parse(string(typed))
	case int64:
		t.Time = time.Unix(typed, 0).UTC()
		return nil
	default:
		return nil
	}
}

// Value implementa driver.Valuer: zero vira NULL.
func (t Timestamp) Value() (driverValue, error) {
	if t.Time.IsZero() {
		return nil, nil
	}
	return t.Time.UTC().Format(Layout), nil
}

// MarshalJSON emite o formato canonico (ou null quando vazio).
func (t Timestamp) MarshalJSON() ([]byte, error) {
	if t.Time.IsZero() {
		return []byte("null"), nil
	}
	return []byte(`"` + t.Time.UTC().Format(Layout) + `"`), nil
}

// UnmarshalJSON aceita RFC3339, o formato canonico, data pura e null.
func (t *Timestamp) UnmarshalJSON(raw []byte) error {
	text := string(raw)
	if text == "null" || text == `""` {
		t.Time = time.Time{}
		return nil
	}
	text = trimQuotes(text)
	return t.parse(text)
}

func (t *Timestamp) parse(raw string) error {
	trimmed := trimSpace(raw)
	if trimmed == "" {
		t.Time = time.Time{}
		return nil
	}
	layouts := []string{
		Layout,
		time.RFC3339Nano,
		time.RFC3339,
		"2006-01-02T15:04:05",
		"2006-01-02 15:04:05.999999999-07:00",
		"2006-01-02 15:04:05",
		"2006-01-02",
	}
	for _, layout := range layouts {
		if parsed, err := time.Parse(layout, trimmed); err == nil {
			t.Time = parsed.UTC()
			return nil
		}
	}
	return nil
}

func trimSpace(raw string) string {
	start, end := 0, len(raw)
	for start < end && (raw[start] == ' ' || raw[start] == '\t' || raw[start] == '\n' || raw[start] == '\r') {
		start++
	}
	for end > start && (raw[end-1] == ' ' || raw[end-1] == '\t' || raw[end-1] == '\n' || raw[end-1] == '\r') {
		end--
	}
	return raw[start:end]
}

func trimQuotes(raw string) string {
	if len(raw) >= 2 && raw[0] == '"' && raw[len(raw)-1] == '"' {
		return raw[1 : len(raw)-1]
	}
	return raw
}
