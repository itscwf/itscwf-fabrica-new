// Package models: entidades do banco itscwf_fabrica.db (schema canonico em
// scripts/schema_init.sql). Ver timestamp.go para o tipo de data usado.
package models

import (
	"database/sql/driver"

	"gorm.io/gorm"
)

// driverValue evita importar database/sql/driver em todo arquivo.
type driverValue = driver.Value

// RefString e um identificador textual que aceita numero ou string no JSON.
// A coluna canonica job_id guarda o id textual do jobs.json ("e054c4e9"), mas
// os clientes costumam enviar o id interno (1, 2, 3) — os dois funcionam.
type RefString string

// UnmarshalJSON aceita "abc", 12 e null.
func (r *RefString) UnmarshalJSON(raw []byte) error {
	text := trimSpace(string(raw))
	if text == "null" || text == `""` {
		*r = ""
		return nil
	}
	*r = RefString(trimQuotes(text))
	return nil
}

// MarshalJSON sempre emite string.
func (r RefString) MarshalJSON() ([]byte, error) {
	return []byte(`"` + string(r) + `"`), nil
}

// Scan implementa sql.Scanner.
func (r *RefString) Scan(value any) error {
	switch typed := value.(type) {
	case nil:
		*r = ""
	case string:
		*r = RefString(typed)
	case []byte:
		*r = RefString(string(typed))
	case int64:
		*r = RefString(itoaLocal(typed))
	case float64:
		*r = RefString(itoaLocal(int64(typed)))
	default:
		*r = RefString("")
	}
	return nil
}

// Value implementa driver.Valuer.
func (r RefString) Value() (driverValue, error) {
	if r == "" {
		return nil, nil
	}
	return string(r), nil
}

func itoaLocal(value int64) string {
	if value == 0 {
		return "0"
	}
	negative := value < 0
	if negative {
		value = -value
	}
	buf := [20]byte{}
	index := len(buf)
	for value > 0 {
		index--
		buf[index] = byte('0' + value%10)
		value /= 10
	}
	if negative {
		index--
		buf[index] = '-'
	}
	return string(buf[index:])
}

// Base carrega a chave primaria, os timestamps de auditoria e o marcador de
// soft delete compartilhados por todas as entidades.
//
// created_at/updated_at/deleted_at sao geridos pela aplicacao (nao pelo GORM):
// o schema canonico grava TEXT ISO-8601 e a API e a unica escritora.
type Base struct {
	ID        int64      `gorm:"primaryKey" json:"id"`
	CreatedAt Timestamp  `gorm:"column:created_at" json:"created_at"`
	UpdatedAt Timestamp  `gorm:"column:updated_at" json:"updated_at"`
	DeletedAt *Timestamp `gorm:"column:deleted_at;index" json:"deleted_at,omitempty"`
}

// ResetAudit limpa os campos geridos pelo servidor: nenhum payload de cliente
// pode forjar id, timestamps ou ressuscitar um registro removido.
func (b *Base) ResetAudit() {
	b.ID = 0
	b.CreatedAt = Timestamp{}
	b.UpdatedAt = Timestamp{}
	b.DeletedAt = nil
}

// MetaID/SetMetaID expõem a chave via interface (handlers CRUD genericos).
func (b *Base) MetaID() int64      { return b.ID }
func (b *Base) SetMetaID(id int64) { b.ID = id }

// CreatedStamp/StampCreated preservam created_at numa atualizacao.
func (b *Base) CreatedStamp() Timestamp  { return b.CreatedAt }
func (b *Base) StampCreated(t Timestamp) { b.CreatedAt = t }

// TouchNew marca a criacao (created_at + updated_at).
func (b *Base) TouchNew(t Timestamp) {
	b.CreatedAt = t
	b.UpdatedAt = t
	b.DeletedAt = nil
}

// Touch atualiza apenas updated_at.
func (b *Base) Touch(t Timestamp) { b.UpdatedAt = t }

// MarkDeleted marca o soft delete.
func (b *Base) MarkDeleted(t Timestamp) { b.DeletedAt = &t }

// NotDeleted informa se a linha esta viva.
func (b *Base) NotDeleted() bool { return b.DeletedAt == nil || b.DeletedAt.IsZero() }

// ---------------------------------------------------------------------------
// Projetos / Repos / Worktrees
// ---------------------------------------------------------------------------

// Project e um projeto gerenciado pela fabrica.
type Project struct {
	Base
	Slug        string `gorm:"column:slug;not null" json:"slug"`
	Name        string `gorm:"column:name;not null" json:"name"`
	Description string `gorm:"column:description" json:"description"`
	Status      string `gorm:"column:status;default:active" json:"status"`
	BoardSlug   string `gorm:"column:board_slug" json:"board_slug"`
	LocalPath   string `gorm:"column:primary_path" json:"local_path"`
	RepoURL     string `gorm:"column:repo_url" json:"repo_url"`
	Icon        string `gorm:"column:icon" json:"icon"`
	Color       string `gorm:"column:color" json:"color"`
	Archived    bool   `gorm:"column:archived;default:false" json:"archived"`

	// Colunas aditivas da API (nao existem no schema canonico original).
	TechStack    string     `gorm:"column:tech_stack" json:"tech_stack"`
	LastActivity *Timestamp `gorm:"column:last_activity" json:"last_activity"`
	KanbanBoard  string     `gorm:"column:kanban_board" json:"kanban_board"`

	Repos     []Repo     `gorm:"foreignKey:ProjectID" json:"repos,omitempty"`
	Worktrees []Worktree `gorm:"foreignKey:ProjectID" json:"worktrees,omitempty"`
	Cycles    []QACycle  `gorm:"foreignKey:ProjectID" json:"qa_cycles,omitempty"`
}

// AfterFind preenche last_activity com updated_at quando a coluna esta vazia,
// para o dashboard nunca exibir "sem atividade" num projeto que ja teve mudanca.
func (p *Project) AfterFind(*gorm.DB) error {
	if p.LastActivity == nil && !p.UpdatedAt.IsZero() {
		activity := p.UpdatedAt
		p.LastActivity = &activity
	}
	return nil
}

// Repo e um repositorio git ligado a um projeto.
type Repo struct {
	Base
	ProjectID int64  `gorm:"column:project_id;index" json:"project_id"`
	Name      string `gorm:"column:name;not null" json:"name"`
	URL       string `gorm:"column:url;not null" json:"url"`
	Host      string `gorm:"column:host" json:"host"`

	// branch/last_commit sao os nomes consumidos pelo frontend; as colunas
	// canonicas correspondentes sao default_branch/head_commit.
	Branch     string `gorm:"column:default_branch" json:"branch"`
	HeadBranch string `gorm:"column:head_branch" json:"head_branch"`
	LastCommit string `gorm:"column:head_commit" json:"last_commit"`

	LocalPath string `gorm:"column:local_path" json:"local_path"`
	IsPrimary bool   `gorm:"column:is_primary;default:true" json:"is_primary"`

	// Colunas aditivas da API.
	Status       string     `gorm:"column:status" json:"status"`
	LastActivity *Timestamp `gorm:"column:last_activity" json:"last_activity"`
}

// Worktree e um checkout local (worktree de task, clone de QA, etc.).
type Worktree struct {
	Base
	RepoID    int64  `gorm:"column:repo_id;index" json:"repo_id"`
	ProjectID int64  `gorm:"column:project_id;index" json:"project_id"`
	LocalPath string `gorm:"column:local_path" json:"local_path"`
	Branch    string `gorm:"column:branch" json:"branch"`
	GitDir    string `gorm:"column:git_dir" json:"git_dir"`
	Kind      string `gorm:"column:kind;default:worktree" json:"kind"`
	TaskID    string `gorm:"column:task_id;index" json:"task_id"`

	// Colunas aditivas da API.
	Name         string     `gorm:"column:name" json:"name"`
	LastCommit   string     `gorm:"column:last_commit" json:"last_commit"`
	LastActivity *Timestamp `gorm:"column:last_activity" json:"last_activity"`
	Status       string     `gorm:"column:status" json:"status"`
}

// ---------------------------------------------------------------------------
// ECA (Evolucao Continua Autonoma)
// ---------------------------------------------------------------------------

// ECA e uma entrada do registro de ECA (tabela eca_registry).
type ECA struct {
	Base
	ECAID   string `gorm:"column:eca_id;not null" json:"eca_id"`
	Title   string `gorm:"column:titulo;not null" json:"title"`
	Summary string `gorm:"column:resumo" json:"summary"`
	Status  string `gorm:"column:status;default:created" json:"status"`
	Resp    string `gorm:"column:responsavel" json:"responsible"`
	Created string `gorm:"column:data_criacao" json:"date_created"`
	Updated string `gorm:"column:data_atualizacao" json:"date_updated"`
	// HermesTaskID e o card Kanban vinculado (coluna kanban_task).
	HermesTaskID  string `gorm:"column:kanban_task" json:"hermes_task_id"`
	KanbanBoard   string `gorm:"column:kanban_board" json:"kanban_board"`
	Repo          string `gorm:"column:repo" json:"repo"`
	ChangelogNote string `gorm:"column:changelog_entry" json:"changelog_entry"`
	Notes         string `gorm:"column:nota" json:"notes"`
	QAImpact      string `gorm:"column:impacto_qa" json:"qa_impact"`
	RawJSON       string `gorm:"column:raw_json" json:"-"`

	// Colunas aditivas da API.
	ProjectID int64 `gorm:"column:project_id;index" json:"project_id"`

	// Campos derivados das tabelas filhas (eca_steps / eca_dependencies).
	Steps        []string `gorm:"-" json:"steps,omitempty"`
	Dependencies []string `gorm:"-" json:"dependencies_list,omitempty"`
	// DependenciesText e o formato "ECA-007, ECA-011" que o frontend exibe.
	DependenciesText string `gorm:"-" json:"dependencies,omitempty"`
}

// TableName fixa o nome canonico da tabela.
func (ECA) TableName() string { return "eca_registry" }

// ECAStep e uma etapa ("etapas" do JSON legado) de uma ECA.
type ECAStep struct {
	ID        int64     `gorm:"column:id;primaryKey" json:"id"`
	ECAID     string    `gorm:"column:eca_id;index" json:"eca_id"`
	Order     int       `gorm:"column:ordem" json:"ordem"`
	Text      string    `gorm:"column:descricao" json:"descricao"`
	CreatedAt Timestamp `gorm:"column:created_at" json:"created_at"`
}

// TableName fixa o nome canonico da tabela.
func (ECAStep) TableName() string { return "eca_steps" }

// ECADependency liga uma ECA as suas dependencias.
type ECADependency struct {
	ID        int64  `gorm:"column:id;primaryKey" json:"id"`
	ECAID     string `gorm:"column:eca_id;index" json:"eca_id"`
	DependsOn string `gorm:"column:depends_on" json:"depends_on"`
}

// TableName fixa o nome canonico da tabela.
func (ECADependency) TableName() string { return "eca_dependencies" }

// ---------------------------------------------------------------------------
// QA
// ---------------------------------------------------------------------------

// QACycle e um ciclo de QA de um projeto (colunas canonicas em portugues).
type QACycle struct {
	Base
	ProjectID   int64  `gorm:"column:project_id;index" json:"project_id"`
	ProjectSlug string `gorm:"column:project_slug;not null" json:"project_slug"`
	Ciclo       int    `gorm:"column:ciclo" json:"ciclo"`
	Volta       int    `gorm:"column:volta;default:1" json:"volta"`
	Status      string `gorm:"column:status;default:em_andamento" json:"status"`

	StartedAt  *Timestamp `gorm:"column:inicio" json:"started_at"`
	FinishedAt *Timestamp `gorm:"column:fim" json:"finished_at"`
	PlannedEnd *Timestamp `gorm:"column:fim_previsto" json:"fim_previsto"`

	Build       string `gorm:"column:build" json:"build"`
	BuildCommit string `gorm:"column:build_commit" json:"build_commit"`
	BuildTested string `gorm:"column:build_testada" json:"build_tested"`

	TotalTCs      int    `gorm:"column:total_tcs" json:"total"`
	Passed        int    `gorm:"column:passou" json:"passed"`
	Failed        int    `gorm:"column:falhou" json:"failed"`
	NotApplicable int    `gorm:"column:na_aplicavel" json:"not_applicable"`
	NextTC        string `gorm:"column:proximo_tc" json:"next_tc"`

	Worktree     string `gorm:"column:worktree" json:"worktree"`
	WorktreePath string `gorm:"column:worktree_path" json:"worktree_path"`
	Blockers     string `gorm:"column:blockers" json:"blockers"`
	Notes        string `gorm:"column:notas" json:"notes"`
	SourceFile   string `gorm:"column:source_file" json:"source_file"`

	// ProjectName e derivado de project_slug para o dashboard.
	ProjectName string `gorm:"-" json:"project_name"`

	// Relacoes carregadas com ?expand=1.
	Project    *Project      `gorm:"foreignKey:ProjectID" json:"project,omitempty"`
	TestCases  []QATestCase  `gorm:"foreignKey:CycleID" json:"testcases,omitempty"`
	Executions []QAExecution `gorm:"foreignKey:CycleID" json:"executions,omitempty"`
}

// AfterFind preenche project_name a partir do slug. O objecto Project entra
// quando a consulta usa ?expand=1 (Preload("Project")).
func (c *QACycle) AfterFind(*gorm.DB) error {
	if c.ProjectName == "" {
		c.ProjectName = c.ProjectSlug
	}
	return nil
}

// QATestCase e um caso de teste de um ciclo.
type QATestCase struct {
	Base
	CycleID int64  `gorm:"column:cycle_id;index;not null" json:"cycle_id"`
	Slug    string `gorm:"column:project_slug" json:"project_slug"`

	// tc_number/title/category/priority sao os nomes do frontend; as colunas
	// canonicas sao tc_id/titulo/modulo/prioridade.
	TCNumber string     `gorm:"column:tc_id;index" json:"tc_number"`
	Title    string     `gorm:"column:titulo" json:"title"`
	Category string     `gorm:"column:modulo;index" json:"category"`
	Priority string     `gorm:"column:prioridade" json:"priority"`
	Status   string     `gorm:"column:status;default:pending" json:"status"`
	LastRun  *Timestamp `gorm:"column:ultima_execucao" json:"last_execution"`
}

// TableName fixa o nome canonico da tabela.
func (QATestCase) TableName() string { return "qa_testcases" }

// QAExecution e o resultado de uma execucao de caso de teste.
type QAExecution struct {
	Base
	CycleID    int64  `gorm:"column:cycle_id;index" json:"cycle_id"`
	TestCaseID int64  `gorm:"column:testcase_id;index" json:"tc_id"`
	Slug       string `gorm:"column:project_slug" json:"project_slug"`

	// TCCode e o codigo canonico (TC-001) guardado na coluna tc_id.
	TCCode string     `gorm:"column:tc_id;index" json:"tc_code"`
	Result string     `gorm:"column:status" json:"result"`
	When   *Timestamp `gorm:"column:data" json:"executed_at"`
	Commit string     `gorm:"column:commit_hash" json:"commit_hash"`
	Output string     `gorm:"column:nota" json:"output"`

	// Colunas aditivas da API.
	DurationMS  int64  `gorm:"column:duration_ms" json:"duration_ms"`
	ExecutionID string `gorm:"column:execution_id;index" json:"execution_id"`
}

// TableName fixa o nome canonico da tabela.
func (QAExecution) TableName() string { return "qa_executions" }

// ---------------------------------------------------------------------------
// Cron
// ---------------------------------------------------------------------------

// CronJob espelha um job de cron do Hermes.
type CronJob struct {
	Base
	JobRef       RefString  `gorm:"column:job_id;not null;uniqueIndex" json:"job_id"`
	Name         string     `gorm:"column:name;not null" json:"name"`
	Description  string     `gorm:"column:description" json:"description"`
	Schedule     string     `gorm:"column:schedule" json:"schedule"`
	ScheduleKind string     `gorm:"column:schedule_kind" json:"schedule_kind"`
	Enabled      bool       `gorm:"column:enabled;default:true" json:"enabled"`
	State        string     `gorm:"column:state;default:scheduled" json:"state"`
	Script       string     `gorm:"column:script" json:"script_path"`
	NoAgent      bool       `gorm:"column:no_agent;default:false" json:"no_agent"`
	Prompt       string     `gorm:"column:prompt" json:"prompt"`
	Skills       string     `gorm:"column:skills" json:"skills"`
	Model        string     `gorm:"column:model" json:"model"`
	Provider     string     `gorm:"column:provider" json:"provider"`
	Deliver      string     `gorm:"column:deliver" json:"deliver"`
	Workdir      string     `gorm:"column:workdir" json:"workdir"`
	ProjectSlug  string     `gorm:"column:project_slug" json:"project_slug"`
	LastRun      *Timestamp `gorm:"column:last_run_at" json:"last_run"`
	LastStatus   string     `gorm:"column:last_status" json:"last_status"`
	LastError    string     `gorm:"column:last_error" json:"last_error"`
	NextRunAt    *Timestamp `gorm:"column:next_run_at" json:"next_run_at"`
	RunCount     int        `gorm:"column:run_count" json:"run_count"`
}

// CronExecution e uma execucao registrada de um job de cron.
type CronExecution struct {
	Base
	ExecID           string     `gorm:"column:exec_id;index" json:"exec_id"`
	JobRef           RefString  `gorm:"column:job_id;index" json:"job_id"`
	JobName          string     `gorm:"column:job_name" json:"job_name"`
	Source           string     `gorm:"column:source" json:"source"`
	Status           string     `gorm:"column:status;index" json:"status"`
	ClaimedAt        *Timestamp `gorm:"column:claimed_at" json:"claimed_at"`
	StartedAt        *Timestamp `gorm:"column:started_at" json:"started_at"`
	FinishedAt       *Timestamp `gorm:"column:finished_at" json:"finished_at"`
	ScheduledInstant *Timestamp `gorm:"column:scheduled_instant" json:"scheduled_instant"`
	DeliveryOutcome  string     `gorm:"column:delivery_outcome" json:"delivery_outcome"`
	DurationMS       int64      `gorm:"column:duration_ms" json:"duration_ms"`
	Error            string     `gorm:"column:error" json:"error"`

	// Colunas aditivas da API.
	ExitCode int    `gorm:"column:exit_code" json:"exit_code"`
	Output   string `gorm:"column:output" json:"output"`
}

// ---------------------------------------------------------------------------
// Inventario: agentes, providers, servidores
// ---------------------------------------------------------------------------

// Agent e um perfil/agente Hermes conhecido pela fabrica.
type Agent struct {
	Base
	Slug     string     `gorm:"column:slug;not null" json:"slug"`
	Name     string     `gorm:"column:name;not null" json:"name"`
	Profile  string     `gorm:"column:profile" json:"profile"`
	Model    string     `gorm:"column:model" json:"model"`
	Provider string     `gorm:"column:provider" json:"provider"`
	Status   string     `gorm:"column:status;default:offline" json:"status"`
	Role     string     `gorm:"column:role" json:"role"`
	Tools    string     `gorm:"column:tools" json:"tools"`
	LastSeen *Timestamp `gorm:"column:last_seen_at" json:"last_seen"`
}

// Provider e um provider de LLM configurado (a API nunca guarda a chave, so a
// referencia de onde o segredo vive).
type Provider struct {
	Base
	Name     string `gorm:"column:name;not null" json:"name"`
	Kind     string `gorm:"column:kind" json:"kind"`
	Endpoint string `gorm:"column:base_url" json:"endpoint"`
	Models   string `gorm:"column:models" json:"models"`
	Enabled  bool   `gorm:"column:enabled;default:true" json:"enabled"`
	Limits   string `gorm:"column:rate_limit" json:"limits"`

	// Colunas aditivas da API.
	APIKeyRef string `gorm:"column:api_key_ref" json:"api_key_ref"`
	Status    string `gorm:"column:status;default:active" json:"status"`
}

// Server e um host do inventario da fabrica.
type Server struct {
	Base
	Name        string `gorm:"column:name;not null" json:"hostname"`
	Host        string `gorm:"column:host" json:"ip"`
	Kind        string `gorm:"column:kind" json:"kind"`
	Environment string `gorm:"column:environment" json:"environment"`
	Status      string `gorm:"column:status;default:unknown" json:"status"`
	Notes       string `gorm:"column:notes" json:"notes"`

	// Colunas aditivas da API.
	Services  string     `gorm:"column:services" json:"services"`
	LastCheck *Timestamp `gorm:"column:last_check" json:"last_check"`
}

// ---------------------------------------------------------------------------
// Trilha de auditoria
// ---------------------------------------------------------------------------

// ActivityLog e o feed de atividade do dashboard (tabela activity_log).
type ActivityLog struct {
	Base
	Timestamp   Timestamp `gorm:"column:ts;index" json:"timestamp"`
	Kind        string    `gorm:"column:kind;index" json:"action"`
	ProjectSlug string    `gorm:"column:project_slug;index" json:"project_name"`
	ProjectID   int64     `gorm:"column:project_id;index" json:"project_id"`
	Actor       string    `gorm:"column:actor;index" json:"actor"`
	Message     string    `gorm:"column:message" json:"details"`
	Ref         string    `gorm:"column:ref;index" json:"target"`

	// MessageText repete a mensagem no campo "message" para os clientes que o
	// consomem; DetailObj transporta o payload estruturado quando houver.
	MessageText string `gorm:"-" json:"message"`
	DetailObj   string `gorm:"-" json:"detail,omitempty"`
}

// TableName fixa o nome canonico da tabela.
func (ActivityLog) TableName() string { return "activity_log" }

// AfterFind espelha message/details.
func (a *ActivityLog) AfterFind(*gorm.DB) error {
	if a.MessageText == "" {
		a.MessageText = a.Message
	}
	return nil
}

// All devolve todos os modelos na ordem de criacao das tabelas.
func All() []any {
	return []any{
		&Project{},
		&Repo{},
		&Worktree{},
		&ECA{},
		&ECAStep{},
		&ECADependency{},
		&QACycle{},
		&QATestCase{},
		&QAExecution{},
		&CronJob{},
		&CronExecution{},
		&Agent{},
		&Provider{},
		&Server{},
		&ActivityLog{},
	}
}
