package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"reflect"
	"sort"
	"strings"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"gorm.io/gorm/schema"

	"github.com/itscwf/itscwf-fabrica-new/internal/middleware"
	"github.com/itscwf/itscwf-fabrica-new/internal/models"
)

// entity e implementada por todos os modelos via models.Base: da aos handlers
// CRUD genericos acesso as colunas compartilhadas (chave, timestamps e o
// marcador de soft delete em TEXT).
type entity interface {
	ResetAudit()
	MetaID() int64
	SetMetaID(int64)
	CreatedStamp() models.Timestamp
	StampCreated(models.Timestamp)
	TouchNew(models.Timestamp)
	Touch(models.Timestamp)
	MarkDeleted(models.Timestamp)
}

// Resource e um recurso CRUD reutilizavel e tipado: paginacao, ordenacao,
// filtros, busca, soft delete e auditoria saem de graca ao registra-lo.
type Resource[T any, PT interface {
	*T
	entity
}] struct {
	// Name e o nome usado na trilha de auditoria ("projects").
	Name string
	// DB e a conexao.
	DB *gorm.DB
	// Allowed e a allow-list de campos gravaveis (JSON). Qualquer outra chave
	// do payload e ignorada, protegendo colunas geridas pelo servidor.
	Allowed map[string]bool
	// Search lista as colunas usadas pelo ?q= (LIKE case-insensitive).
	Search []string
	// Filters mapeia query param -> coluna para igualdade exata.
	Filters map[string]string
	// TimeColumn habilita ?from=/?to= naquela coluna.
	TimeColumn string
	// ResolveFilter normaliza o valor de um filtro antes da comparacao (ex.:
	// aceitar tanto o id interno quanto o job_id textual do Hermes).
	ResolveFilter map[string]func(value string) any
	// KeyColumn habilita busca por chave natural (/projects/:slug).
	KeyColumn string
	// Preload lista relacoes carregadas no Show (e no List com ?expand=1).
	Preload []string
	// Validate roda antes de criar/atualizar (422/409).
	Validate func(obj PT) error
	// Recorder grava a trilha de auditoria.
	Recorder *Recorder
	// ReadOnly registra apenas as rotas GET (ex.: activity_log).
	ReadOnly bool
	// NotSoftDelete desliga o filtro deleted_at (tabelas sem a coluna).
	NotSoftDelete bool
	// NilWhenZero lista colunas anulaveis cujo zero em Go deve virar NULL no
	// banco em vez de 0/"" — tipicamente FKs opcionais: repo_id=0 viola a
	// FOREIGN KEY do schema canonico (nao existe repos.id = 0) e o POST
	// devolveria 409 em vez de criar a linha sem repositorio.
	NilWhenZero []string
	// DefaultOrder e o ORDER BY usado quando ?sort nao vem.
	DefaultOrder string
}

// Register monta as rotas CRUD num grupo de rotas.
func (r *Resource[T, PT]) Register(rg *gin.RouterGroup) {
	rg.GET("", r.List)
	rg.GET("/:id", r.Show)
	if r.ReadOnly {
		return
	}
	rg.POST("", r.Create)
	rg.PUT("/:id", r.Update)
	rg.PATCH("/:id", r.Update)
	rg.DELETE("/:id", r.Destroy)
}

// List atende GET /<recurso>.
func (r *Resource[T, PT]) List(c *gin.Context) {
	p := parsePage(c)
	query := r.query(c)

	var total int64
	if err := query.Count(&total).Error; err != nil {
		failDB(c, err)
		return
	}

	items := make([]T, 0, p.PerPage)
	if err := query.Order(r.orderBy(c)).Limit(p.PerPage).Offset(p.Offset).Find(&items).Error; err != nil {
		failDB(c, err)
		return
	}
	if len(items) == 0 {
		items = []T{}
	}
	listOK(c, items, total, p)
}

// Show atende GET /<recurso>/:id (id numerico ou chave natural).
func (r *Resource[T, PT]) Show(c *gin.Context) {
	obj, err := r.findOne(c)
	if err != nil {
		failDB(c, err)
		return
	}
	ok(c, http.StatusOK, obj)
}

// Create atende POST /<recurso>.
func (r *Resource[T, PT]) Create(c *gin.Context) {
	obj := PT(new(T))
	if err := c.ShouldBindJSON(obj); err != nil {
		fail(c, http.StatusBadRequest, codeBadRequest, "corpo JSON invalido: "+err.Error())
		return
	}
	obj.ResetAudit()
	if r.Validate != nil {
		if err := r.Validate(obj); err != nil {
			failValidation(c, err)
			return
		}
	}
	obj.TouchNew(models.Now())
	query := r.DB
	if omit := nilWhenZeroColumns(r.DB, obj, r.NilWhenZero); len(omit) > 0 {
		query = query.Omit(omit...)
	}
	if err := query.Create(any(obj)).Error; err != nil {
		failDB(c, err)
		return
	}
	r.record(c, "create", obj)
	ok(c, http.StatusCreated, obj)
}

// Update atende PUT/PATCH /<recurso>/:id.
//
// A atualizacao e parcial (merge): a linha atual e serializada, as chaves
// permitidas do payload sao sobrepostas e o resultado e gravado, de modo que
// campos ausentes mantem o valor em vez de serem zerados.
func (r *Resource[T, PT]) Update(c *gin.Context) {
	current, err := r.findOne(c)
	if err != nil {
		failDB(c, err)
		return
	}

	payload := map[string]any{}
	if err := c.ShouldBindJSON(&payload); err != nil {
		fail(c, http.StatusBadRequest, codeBadRequest, "corpo JSON invalido: "+err.Error())
		return
	}

	patch := make(map[string]any, len(payload))
	ignored := make([]string, 0, len(payload))
	for key, value := range payload {
		switch key {
		case "id", "created_at", "updated_at", "deleted_at":
			continue // geridos pelo servidor
		}
		if r.Allowed[key] {
			patch[key] = value
		} else {
			ignored = append(ignored, key)
		}
	}
	if len(patch) == 0 {
		message := "nenhum campo atualizavel no payload"
		if len(ignored) > 0 {
			sort.Strings(ignored)
			message += " (ignorados: " + strings.Join(ignored, ", ") + ")"
		}
		fail(c, http.StatusBadRequest, codeBadRequest, message)
		return
	}

	next, err := mergeEntity(current, patch)
	if err != nil {
		fail(c, http.StatusBadRequest, codeBadRequest, err.Error())
		return
	}
	next.SetMetaID(current.MetaID())
	next.StampCreated(current.CreatedStamp())

	if r.Validate != nil {
		if err := r.Validate(next); err != nil {
			failValidation(c, err)
			return
		}
	}
	next.Touch(models.Now())
	save := r.DB
	// Save forca Select("*") quando nenhuma selecao foi definida, portanto o
	// jeito de manter uma FK anulavel de fora do UPDATE e passar a lista
	// explicita de colunas (Omit nao sobrevive ao Save).
	if omit := nilWhenZeroColumns(r.DB, next, r.NilWhenZero); len(omit) > 0 {
		if columns := modelColumnNames(r.DB, next); len(columns) > 0 {
			save = save.Select(omitColumns(columns, omit))
		}
	}
	if err := save.Omit(clause.Associations).Save(any(next)).Error; err != nil {
		failDB(c, err)
		return
	}
	// Zero explicito no payload (ex.: {"repo_id": 0}) limpa o vinculo: o Save
	// acima preservou a coluna para nao zerar um vinculo valido, entao o NULL e
	// aplicado aqui, separadamente.
	if cleared := explicitZeroColumns(patch, r.NilWhenZero); len(cleared) > 0 {
		updates := make(map[string]any, len(cleared))
		for _, column := range cleared {
			updates[column] = nil
		}
		if err := r.DB.Model(any(next)).Where("id = ?", next.MetaID()).Updates(updates).Error; err != nil {
			failDB(c, err)
			return
		}
		for _, column := range cleared {
			clearColumn(r.DB, next, column)
		}
	}
	r.record(c, "update", next)
	ok(c, http.StatusOK, next)
}

// Destroy atende DELETE /<recurso>/:id. Soft delete por padrao (deleted_at);
// permanente com ?hard=true e papel admin.
func (r *Resource[T, PT]) Destroy(c *gin.Context) {
	obj, err := r.findOne(c)
	if err != nil {
		failDB(c, err)
		return
	}
	if queryBool(c, "hard", false) {
		if !middleware.IsAdmin(c) {
			fail(c, http.StatusForbidden, codeForbidden, "hard delete exige papel admin")
			return
		}
		if err := r.DB.Delete(any(obj)).Error; err != nil {
			failDB(c, err)
			return
		}
		r.record(c, "hard_delete", obj)
		c.JSON(http.StatusOK, gin.H{"data": gin.H{"id": obj.MetaID(), "deleted": "hard"}})
		return
	}
	stamp := models.Now()
	obj.MarkDeleted(stamp)
	if err := r.DB.Model(any(obj)).Where("id = ?", obj.MetaID()).
		Updates(map[string]any{"deleted_at": stamp, "updated_at": stamp}).Error; err != nil {
		failDB(c, err)
		return
	}
	r.record(c, "delete", obj)
	c.JSON(http.StatusOK, gin.H{"data": gin.H{"id": obj.MetaID(), "deleted": "soft"}})
}

// findOne carrega a linha apontada pelo parametro :id.
func (r *Resource[T, PT]) findOne(c *gin.Context) (PT, error) {
	raw := c.Param("id")
	query := r.alive(r.DB.Model(new(T)), c)
	for _, preload := range r.Preload {
		query = query.Preload(preload)
	}

	if id, isNumeric := numericID(raw); isNumeric {
		query = query.Where("id = ?", id)
	} else if r.KeyColumn != "" {
		query = query.Where(r.KeyColumn+" = ?", raw)
	} else {
		return nil, gorm.ErrRecordNotFound
	}

	obj := PT(new(T))
	if err := query.First(obj).Error; err != nil {
		return nil, err
	}
	return obj, nil
}

// query monta a consulta de listagem com filtros, busca e soft delete.
func (r *Resource[T, PT]) query(c *gin.Context) *gorm.DB {
	query := r.alive(r.DB.Model(new(T)), c)
	if len(r.Preload) > 0 && queryBool(c, "expand", false) {
		for _, preload := range r.Preload {
			query = query.Preload(preload)
		}
	}
	for param, column := range r.Filters {
		if value := c.Query(param); value != "" {
			var resolved any = value
			if resolver, ok := r.ResolveFilter[param]; ok && resolver != nil {
				resolved = resolver(value)
			}
			query = query.Where(column+" = ?", resolved)
		}
	}
	if r.TimeColumn != "" {
		if from, hasFrom := parseTimeParam(c.Query("from")); hasFrom {
			query = query.Where(r.TimeColumn+" >= ?", models.NewTimestamp(from))
		}
		if to, hasTo := parseTimeParam(c.Query("to")); hasTo {
			query = query.Where(r.TimeColumn+" <= ?", models.NewTimestamp(to))
		}
	}
	if term := strings.TrimSpace(c.Query("q")); term != "" && len(r.Search) > 0 {
		like := "%" + strings.ToLower(term) + "%"
		conditions := make([]string, 0, len(r.Search))
		args := make([]any, 0, len(r.Search))
		for _, column := range r.Search {
			conditions = append(conditions, "LOWER("+column+") LIKE ?")
			args = append(args, like)
		}
		query = query.Where("("+strings.Join(conditions, " OR ")+")", args...)
	}
	return query
}

// alive aplica o filtro de soft delete, exceto com ?include_deleted=1.
func (r *Resource[T, PT]) alive(query *gorm.DB, c *gin.Context) *gorm.DB {
	if r.NotSoftDelete || queryBool(c, "include_deleted", false) {
		return query
	}
	return query.Where("deleted_at IS NULL")
}

// orderBy resolve ?sort=coluna|-coluna com ?order=asc|desc como alternativa.
func (r *Resource[T, PT]) orderBy(c *gin.Context) string {
	column := strings.TrimSpace(c.Query("sort"))
	direction := strings.ToLower(strings.TrimSpace(c.Query("order")))
	if column == "" {
		if r.DefaultOrder != "" {
			return r.DefaultOrder
		}
		return "id desc"
	}
	desc := false
	if strings.HasPrefix(column, "-") {
		desc = true
		column = strings.TrimPrefix(column, "-")
	} else if strings.HasPrefix(column, "+") {
		column = strings.TrimPrefix(column, "+")
	}
	if direction == "desc" {
		desc = true
	} else if direction == "asc" {
		desc = false
	}
	resolved := r.resolveColumn(column)
	if resolved == "" {
		return r.defaultOrderOrIDDesc()
	}
	if desc {
		return resolved + " desc"
	}
	return resolved + " asc"
}

func (r *Resource[T, PT]) defaultOrderOrIDDesc() string {
	if r.DefaultOrder != "" {
		return r.DefaultOrder
	}
	return "id desc"
}

// sortableColumns devolve as colunas aceitas em ORDER BY.
func (r *Resource[T, PT]) sortableColumns() []string {
	columns := []string{"id", "created_at", "updated_at", "deleted_at"}
	for key := range r.Allowed {
		columns = append(columns, key)
	}
	for _, column := range r.Search {
		columns = append(columns, column)
	}
	for _, column := range r.Filters {
		columns = append(columns, column)
	}
	if r.TimeColumn != "" {
		columns = append(columns, r.TimeColumn)
	}
	sort.Strings(columns)
	return columns
}

// resolveColumn traduz o valor de ?sort= na coluna real do schema.
//
// Os nomes expostos no JSON nao sao sempre os nomes das colunas canonicas
// (local_path→primary_path, output→nota, script_path→script, hostname→name,
// last_seen→last_seen_at): usar o nome do JSON direto no ORDER BY gera
// "no such column" e devolvia 500. Aqui o nome e resolvido pelo modelo e o
// que nao corresponder a nenhuma coluna cai no ordenamento padrao.
func (r *Resource[T, PT]) resolveColumn(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return ""
	}
	statement := &gorm.Statement{DB: r.DB}
	if err := statement.Parse(new(T)); err != nil || statement.Schema == nil {
		return ""
	}
	for _, field := range statement.Schema.Fields {
		if strings.EqualFold(field.DBName, name) || strings.EqualFold(field.Name, name) {
			return field.DBName
		}
	}
	for key, column := range r.jsonColumnIndex(statement.Schema) {
		if key == strings.ToLower(name) {
			return column
		}
	}
	return ""
}

// jsonColumnIndex mapeia a chave JSON de cada campo para a coluna canonica.
func (r *Resource[T, PT]) jsonColumnIndex(sch *schema.Schema) map[string]string {
	index := make(map[string]string)
	var walk func(t reflect.Type)
	walk = func(t reflect.Type) {
		for i := 0; i < t.NumField(); i++ {
			field := t.Field(i)
			if field.Anonymous {
				nested := field.Type
				if nested.Kind() == reflect.Pointer {
					nested = nested.Elem()
				}
				if nested.Kind() == reflect.Struct {
					walk(nested)
				}
				continue
			}
			if field.PkgPath != "" {
				continue
			}
			tag, ok := field.Tag.Lookup("json")
			if !ok {
				continue
			}
			jsonName := strings.Split(tag, ",")[0]
			if jsonName == "" || jsonName == "-" {
				continue
			}
			if mapped := sch.LookUpField(field.Name); mapped != nil {
				index[strings.ToLower(jsonName)] = mapped.DBName
			}
		}
	}
	walk(reflect.TypeOf((*T)(nil)).Elem())
	return index
}

func (r *Resource[T, PT]) record(c *gin.Context, action string, obj PT) {
	if r.Recorder == nil {
		return
	}
	r.Recorder.Record(c, action, fmt.Sprintf("%s/%d", r.Name, obj.MetaID()), "", obj)
}

// mergeEntity serializa base, sobrepoe patch e decodifica de volta em um T novo.
func mergeEntity[T any, PT interface {
	*T
	entity
}](base PT, patch map[string]any) (PT, error) {
	raw, err := json.Marshal(base)
	if err != nil {
		return nil, fmt.Errorf("serializar recurso atual: %w", err)
	}
	merged := map[string]any{}
	if err := json.Unmarshal(raw, &merged); err != nil {
		return nil, fmt.Errorf("decodificar recurso atual: %w", err)
	}
	for key, value := range patch {
		merged[key] = value
	}
	encoded, err := json.Marshal(merged)
	if err != nil {
		return nil, fmt.Errorf("codificar recurso mesclado: %w", err)
	}
	next := PT(new(T))
	if err := json.Unmarshal(encoded, next); err != nil {
		return nil, fmt.Errorf("valor de campo invalido: %w", err)
	}
	return next, nil
}

// numericID informa se o parametro de rota e uma chave primaria.
func numericID(raw string) (int64, bool) {
	if raw == "" {
		return 0, false
	}
	var value int64
	for _, r := range raw {
		if r < '0' || r > '9' {
			return 0, false
		}
		value = value*10 + int64(r-'0')
	}
	if value == 0 {
		return 0, false
	}
	return value, true
}

// requireText valida campos obrigatorios.
func requireText(pairs map[string]string) error {
	for value, label := range pairs {
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("%s e obrigatorio", label)
		}
	}
	return nil
}

// ---------------------------------------------------------------------------
// Colunas anulaveis (NilWhenZero)
// ---------------------------------------------------------------------------

// nilWhenZeroColumns devolve as colunas do schema (DB names) listadas em
// NilWhenZero que estao zeradas no modelo. O zero do Go nao pode virar 0 no
// banco quando a coluna e uma FK anulavel: 0 nao existe em repos.id e o INSERT
// morreria na FOREIGN KEY. Omitindo a coluna, o DEFAULT/NULL do schema vale.
func nilWhenZeroColumns[T any](db *gorm.DB, model *T, names []string) []string {
	if model == nil || len(names) == 0 {
		return nil
	}
	statement := &gorm.Statement{DB: db}
	if err := statement.Parse(model); err != nil || statement.Schema == nil {
		return nil
	}
	value := reflect.ValueOf(model).Elem()
	columns := make([]string, 0, len(names))
	for _, field := range statement.Schema.Fields {
		if !matchesColumn(field, names) {
			continue
		}
		if _, isZero := field.ValueOf(context.Background(), value); isZero {
			columns = append(columns, field.DBName)
		}
	}
	return columns
}

// modelColumnNames devolve todas as colunas gravaveis do modelo.
func modelColumnNames[T any](db *gorm.DB, model *T) []string {
	statement := &gorm.Statement{DB: db}
	if err := statement.Parse(model); err != nil || statement.Schema == nil {
		return nil
	}
	columns := make([]string, 0, len(statement.Schema.Fields))
	for _, field := range statement.Schema.Fields {
		columns = append(columns, field.DBName)
	}
	return columns
}

// matchesColumn aceita o nome da coluna (tag gorm) ou o campo Go.
func matchesColumn(field *schema.Field, names []string) bool {
	for _, name := range names {
		if strings.EqualFold(field.DBName, name) || strings.EqualFold(field.Name, name) {
			return true
		}
	}
	return false
}

// omitColumns remove de columns os nomes listados em drop.
func omitColumns(columns, drop []string) []string {
	if len(drop) == 0 {
		return columns
	}
	kept := make([]string, 0, len(columns))
	for _, column := range columns {
		skip := false
		for _, excluded := range drop {
			if strings.EqualFold(column, excluded) {
				skip = true
				break
			}
		}
		if !skip {
			kept = append(kept, column)
		}
	}
	return kept
}

// explicitZeroColumns devolve as colunas de Resource.NilWhenZero que o payload
// zerou de proposito ({"repo_id": 0}), pedindo a limpeza do vinculo (NULL).
func explicitZeroColumns(patch map[string]any, columns []string) []string {
	if len(patch) == 0 || len(columns) == 0 {
		return nil
	}
	cleared := make([]string, 0, len(columns))
	for _, column := range columns {
		value, present := patch[column]
		if !present || !isZeroJSONValue(value) {
			continue
		}
		cleared = append(cleared, column)
	}
	return cleared
}

// clearColumn zera o campo correspondente para a resposta refletir o NULL.
func clearColumn[T any](db *gorm.DB, model *T, column string) {
	statement := &gorm.Statement{DB: db}
	if err := statement.Parse(model); err != nil || statement.Schema == nil {
		return
	}
	value := reflect.ValueOf(model).Elem()
	for _, field := range statement.Schema.Fields {
		if !matchesColumn(field, []string{column}) {
			continue
		}
		_ = field.Set(context.Background(), value, reflect.Zero(field.FieldType).Interface())
		return
	}
}

// isZeroJSONValue reconhece null, 0, "" e false vindos do JSON.
func isZeroJSONValue(value any) bool {
	switch typed := value.(type) {
	case nil:
		return true
	case float64:
		return typed == 0
	case float32:
		return typed == 0
	case int:
		return typed == 0
	case int64:
		return typed == 0
	case string:
		return strings.TrimSpace(typed) == ""
	case bool:
		return !typed
	default:
		return false
	}
}
