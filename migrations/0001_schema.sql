-- ============================================================================
-- migrations/0001_schema.sql — schema CANONICO do banco itscwf_fabrica.db
-- Dialeto: SQLite 3 (WAL). Idempotente: tudo usa IF NOT EXISTS.
--
-- Esta e a fonte unica da verdade do modelo de dados. O filtro antigo
-- scripts/schema_init.sql e um ESPELHO gerado deste arquivo (mais 0002_*) —
-- nao edite o espelho: rode `python3 scripts/gen_schema_snapshot.py`.
--
-- Convencoes:
--   * Colunas de dominio tem `deleted_at` (soft delete) usado pela API REST.
--   * `created_at` / `updated_at` sao TEXT ISO-8601 (UTC) com default now.
--   * Chaves naturais tem indice UNIQUE para permitir UPSERT idempotente.
--   * As colunas marcadas como "coluna da API" foram criadas originalmente por
--     ALTER TABLE ADD COLUMN (internal/db/migrations.go) e estao declaradas
--     aqui para que banco novo e banco migrado tenham EXATAMENTE o mesmo
--     schema. Todas anulaveis, para o ALTER nunca quebrar tabela com dados.
--
-- Mapeamento dos nomes em ingles usados pelo frontend/JSON para as colunas
-- canonicas: ver docs/SCHEMA.md.
-- ============================================================================


PRAGMA foreign_keys = ON;

-- ---------------------------------------------------------------------------
-- Projetos / Repositórios / Worktrees
-- ---------------------------------------------------------------------------

CREATE TABLE IF NOT EXISTS projects (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    slug          TEXT    NOT NULL UNIQUE,
    name          TEXT    NOT NULL,
    description   TEXT,
    status        TEXT    NOT NULL DEFAULT 'active'
                          CHECK (status IN ('active','paused','archived','planning')),
    board_slug    TEXT,                       -- board Hermes/Kanban associado
    primary_path  TEXT,                       -- diretório principal do projeto
    repo_url      TEXT,                       -- URL git do repo principal (sem credenciais)
    icon          TEXT,
    color         TEXT,
    tech_stack    TEXT,                       -- stack tecnologica (UI /projects)
    last_activity TEXT,                       -- ultima atividade exibida no dashboard
    archived      INTEGER NOT NULL DEFAULT 0,
    created_at    TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ','now')),
    updated_at    TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ','now')),
    deleted_at    TEXT
);
CREATE INDEX IF NOT EXISTS idx_projects_board  ON projects(board_slug);
CREATE INDEX IF NOT EXISTS idx_projects_status ON projects(status);

CREATE TABLE IF NOT EXISTS repos (
    id                 INTEGER PRIMARY KEY AUTOINCREMENT,
    project_id         INTEGER REFERENCES projects(id) ON DELETE CASCADE,
    name               TEXT    NOT NULL,
    url                TEXT    NOT NULL UNIQUE,   -- credenciais removidas
    host               TEXT,                      -- github.com, etc.
    default_branch     TEXT,
    head_branch        TEXT,
    head_commit        TEXT,
    local_path         TEXT,                      -- clone canônico
    status             TEXT,                      -- status do repo na UI
    last_activity      TEXT,                      -- ultima atividade do repo
    is_primary         INTEGER NOT NULL DEFAULT 1,
    created_at         TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ','now')),
    updated_at         TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ','now')),
    deleted_at         TEXT
);
CREATE INDEX IF NOT EXISTS idx_repos_project ON repos(project_id);

CREATE TABLE IF NOT EXISTS worktrees (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    repo_id      INTEGER REFERENCES repos(id)    ON DELETE CASCADE,
    project_id   INTEGER REFERENCES projects(id) ON DELETE CASCADE,
    local_path   TEXT    NOT NULL UNIQUE,
    branch       TEXT,
    git_dir      TEXT,                            -- valor do .git (gitdir: ...)
    kind         TEXT    NOT NULL DEFAULT 'worktree'
                         CHECK (kind IN ('worktree','clone','qa','task','feature')),
    name         TEXT,                            -- rotulo do worktree na UI
    last_commit  TEXT,                            -- commit de HEAD (detalhe do projeto)
    last_activity TEXT,                           -- ultima atividade do worktree
    status       TEXT,                            -- status do worktree na UI
    task_id      TEXT,                            -- card Kanban que criou o worktree
    created_at   TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ','now')),
    updated_at   TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ','now')),
    deleted_at   TEXT
);
CREATE INDEX IF NOT EXISTS idx_worktrees_repo    ON worktrees(repo_id);
CREATE INDEX IF NOT EXISTS idx_worktrees_project ON worktrees(project_id);

-- ---------------------------------------------------------------------------
-- ECA (Evolução Contínua Autônoma)
-- ---------------------------------------------------------------------------

CREATE TABLE IF NOT EXISTS eca_registry (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    eca_id          TEXT    NOT NULL UNIQUE,       -- ECA-011 ...
    titulo          TEXT    NOT NULL,
    resumo          TEXT,
    status          TEXT    NOT NULL DEFAULT 'created'
                            CHECK (status IN ('created','in_progress','blocked',
                                              'done','cancelled','paused')),
    responsavel     TEXT,
    data_criacao    TEXT,
    data_atualizacao TEXT,
    kanban_task     TEXT,
    kanban_board    TEXT,
    repo            TEXT,
    changelog_entry TEXT,
    nota            TEXT,
    impacto_qa      TEXT,
    project_id      INTEGER,                       -- projeto dono da ECA (sem FK: paridade com o ALTER da API)
    raw_json        TEXT,                           -- payload original para rastreabilidade
    created_at      TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ','now')),
    updated_at      TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ','now')),
    deleted_at      TEXT
);
CREATE INDEX IF NOT EXISTS idx_eca_status ON eca_registry(status);

CREATE TABLE IF NOT EXISTS eca_steps (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    eca_id     TEXT    NOT NULL REFERENCES eca_registry(eca_id) ON DELETE CASCADE,
    ordem      INTEGER NOT NULL,
    descricao  TEXT    NOT NULL,
    created_at TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ','now')),
    UNIQUE (eca_id, ordem)
);

CREATE TABLE IF NOT EXISTS eca_dependencies (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    eca_id     TEXT    NOT NULL REFERENCES eca_registry(eca_id) ON DELETE CASCADE,
    depends_on TEXT    NOT NULL,
    UNIQUE (eca_id, depends_on)
);

-- ---------------------------------------------------------------------------
-- QA — ciclos, testcases, execuções
-- ---------------------------------------------------------------------------

CREATE TABLE IF NOT EXISTS qa_cycles (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    project_id    INTEGER REFERENCES projects(id) ON DELETE CASCADE,
    project_slug  TEXT    NOT NULL,
    ciclo         INTEGER NOT NULL,
    volta         INTEGER NOT NULL DEFAULT 1,
    status        TEXT    NOT NULL DEFAULT 'em_andamento'
                          CHECK (status IN ('em_andamento','pronto','concluido',
                                            'encerrado','bloqueado','cancelado')),
    inicio        TEXT,
    fim           TEXT,
    fim_previsto  TEXT,
    build         TEXT,
    build_commit  TEXT,
    build_testada TEXT,
    total_tcs     INTEGER NOT NULL DEFAULT 0,
    passou        INTEGER NOT NULL DEFAULT 0,
    falhou        INTEGER NOT NULL DEFAULT 0,
    na_aplicavel  INTEGER NOT NULL DEFAULT 0,
    proximo_tc    TEXT,
    worktree      TEXT,
    worktree_path TEXT,
    blockers      TEXT,                             -- JSON array
    notas         TEXT,                             -- JSON array
    source_file   TEXT,                             -- JSON de origem (fonte do resumo canônico)
    created_at    TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ','now')),
    updated_at    TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ','now')),
    deleted_at    TEXT,
    -- Um ciclo por projeto: os múltiplos arquivos qa-state.json (voltas/worktrees)
    -- ficam registrados individualmente em qa_cycle_sources.
    UNIQUE (project_slug, ciclo)
);
CREATE INDEX IF NOT EXISTS idx_qa_cycles_project ON qa_cycles(project_id);

-- Fidelidade: cada arquivo qa-state.json encontrado gera uma linha aqui, mesmo
-- quando não é a fonte do resumo canônico (ex.: mesmo ciclo em worktrees diferentes).
CREATE TABLE IF NOT EXISTS qa_cycle_sources (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    cycle_id      INTEGER REFERENCES qa_cycles(id) ON DELETE CASCADE,
    project_slug  TEXT    NOT NULL,
    ciclo         INTEGER NOT NULL,
    volta         INTEGER,
    source_file   TEXT    NOT NULL UNIQUE,
    source_mtime  TEXT,
    status        TEXT,
    build         TEXT,
    build_commit  TEXT,
    build_testada TEXT,
    inicio        TEXT,
    fim           TEXT,
    total_tcs     INTEGER,
    executados    INTEGER,
    passou        INTEGER,
    falhou        INTEGER,
    na_aplicavel  INTEGER,
    worktree      TEXT,
    worktree_path TEXT,
    is_canonical  INTEGER NOT NULL DEFAULT 0,
    raw_json      TEXT,
    created_at    TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ','now')),
    updated_at    TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ','now'))
);

CREATE TABLE IF NOT EXISTS qa_testcases (
    id             INTEGER PRIMARY KEY AUTOINCREMENT,
    cycle_id       INTEGER NOT NULL REFERENCES qa_cycles(id) ON DELETE CASCADE,
    project_slug   TEXT    NOT NULL,
    tc_id          TEXT    NOT NULL,                -- TC-001, TC-021a ...
    titulo         TEXT,
    modulo         TEXT,
    prioridade     TEXT,
    status         TEXT    NOT NULL DEFAULT 'pending'
                           CHECK (status IN ('pending','PASS','FAIL','SKIP',
                                             'N/A','BLOCKED','running')),
    ultima_execucao TEXT,
    created_at     TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ','now')),
    updated_at     TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ','now')),
    deleted_at     TEXT,
    UNIQUE (cycle_id, tc_id)
);
CREATE INDEX IF NOT EXISTS idx_qa_testcases_project ON qa_testcases(project_slug);
CREATE INDEX IF NOT EXISTS idx_qa_testcases_status  ON qa_testcases(status);

CREATE TABLE IF NOT EXISTS qa_executions (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    cycle_id     INTEGER REFERENCES qa_cycles(id) ON DELETE CASCADE,
    testcase_id  INTEGER REFERENCES qa_testcases(id) ON DELETE SET NULL,
    project_slug TEXT    NOT NULL,
    tc_id        TEXT    NOT NULL,
    status       TEXT    NOT NULL,
    data         TEXT,
    commit_hash  TEXT,
    duration_ms  INTEGER,                          -- duracao da execucao em milissegundos
    execution_id TEXT,                             -- identificador legivel da execucao
    updated_at   TEXT,                             -- auditoria da API
    deleted_at   TEXT,                             -- soft delete
    nota         TEXT,
    created_at   TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ','now')),
    UNIQUE (cycle_id, tc_id, data, commit_hash)
);
CREATE INDEX IF NOT EXISTS idx_qa_exec_project ON qa_executions(project_slug);

-- ---------------------------------------------------------------------------
-- Cron — jobs e execuções
-- ---------------------------------------------------------------------------

CREATE TABLE IF NOT EXISTS cron_jobs (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    job_id          TEXT    NOT NULL UNIQUE,        -- id do jobs.json
    name            TEXT    NOT NULL,
    description     TEXT,
    schedule        TEXT,
    schedule_kind   TEXT,                           -- cron | interval | once
    enabled         INTEGER NOT NULL DEFAULT 1,
    state           TEXT    NOT NULL DEFAULT 'scheduled'
                            CHECK (state IN ('scheduled','paused','running',
                                             'disabled','error')),
    script          TEXT,
    no_agent        INTEGER NOT NULL DEFAULT 0,
    prompt          TEXT,
    skills          TEXT,                           -- JSON array
    model           TEXT,
    provider        TEXT,
    deliver         TEXT,
    workdir         TEXT,
    project_slug    TEXT,
    last_run_at     TEXT,
    last_status     TEXT,
    last_error      TEXT,
    last_delivery_error TEXT,
    next_run_at     TEXT,
    run_count       INTEGER NOT NULL DEFAULT 0,
    created_at      TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ','now')),
    updated_at      TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ','now')),
    deleted_at      TEXT
);
CREATE INDEX IF NOT EXISTS idx_cron_jobs_state   ON cron_jobs(state);
CREATE INDEX IF NOT EXISTS idx_cron_jobs_enabled ON cron_jobs(enabled);

CREATE TABLE IF NOT EXISTS cron_executions (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    exec_id         TEXT    NOT NULL UNIQUE,
    job_id          TEXT    NOT NULL,
    job_name        TEXT,
    source          TEXT,
    status          TEXT,
    claimed_at      TEXT,
    started_at      TEXT,
    finished_at     TEXT,
    scheduled_instant TEXT,
    delivery_outcome TEXT,
    output          TEXT,                           -- stdout/stderr do job (log viewer)
    exit_code       INTEGER,                        -- exit code do processo
    updated_at      TEXT,                           -- auditoria da API
    deleted_at      TEXT,                           -- soft delete
    duration_ms     INTEGER,
    error           TEXT,
    created_at      TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ','now'))
);
CREATE INDEX IF NOT EXISTS idx_cron_exec_job  ON cron_executions(job_id);
CREATE INDEX IF NOT EXISTS idx_cron_exec_when ON cron_executions(started_at);

-- ---------------------------------------------------------------------------
-- Agentes / Providers / Servers (inventário — populados pela API T3)
-- ---------------------------------------------------------------------------

CREATE TABLE IF NOT EXISTS agents (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    slug        TEXT    NOT NULL UNIQUE,
    name        TEXT    NOT NULL,
    profile     TEXT,
    model       TEXT,
    provider    TEXT,
    status      TEXT    NOT NULL DEFAULT 'offline'
                        CHECK (status IN ('online','offline','busy','error')),
    tools       TEXT,                                -- ferramentas habilitadas do perfil
    role        TEXT,
    last_seen_at TEXT,
    created_at  TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ','now')),
    updated_at  TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ','now')),
    deleted_at  TEXT
);

CREATE TABLE IF NOT EXISTS providers (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    name        TEXT    NOT NULL UNIQUE,
    kind        TEXT,
    base_url    TEXT,
    models      TEXT,                                -- JSON array
    enabled     INTEGER NOT NULL DEFAULT 1,
    api_key_ref TEXT,                                -- referencia (nunca o valor) da chave
    status      TEXT,                                -- status operacional do provider
    rate_limit  TEXT,
    created_at  TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ','now')),
    updated_at  TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ','now')),
    deleted_at  TEXT
);

CREATE TABLE IF NOT EXISTS servers (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    name        TEXT    NOT NULL UNIQUE,
    host        TEXT,
    kind        TEXT,                                -- lxc, vps, docker-host...
    environment TEXT,
    status      TEXT    NOT NULL DEFAULT 'unknown',
    services    TEXT,                                -- servicos rodando no host
    last_check  TEXT,                                -- ultima verificacao de saude
    notes       TEXT,
    created_at  TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ','now')),
    updated_at  TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ','now')),
    deleted_at  TEXT
);

-- ---------------------------------------------------------------------------
-- Activity log — feed unificado usado pelo dashboard (GET /api/v1/activity)
-- ---------------------------------------------------------------------------

CREATE TABLE IF NOT EXISTS activity_log (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    ts           TEXT    NOT NULL,
    kind         TEXT    NOT NULL,                  -- eca, qa, cron, project, task
    project_slug TEXT,
    actor        TEXT,
    message      TEXT    NOT NULL,
    project_id   INTEGER,                           -- projeto do evento (sem FK: paridade com o ALTER da API)
    updated_at   TEXT,                              -- auditoria da API
    deleted_at   TEXT,                              -- soft delete
    ref          TEXT,                              -- ECA-013, TC-045, job id ...
    created_at   TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ','now')),
    UNIQUE (ts, kind, ref, message)
);
CREATE INDEX IF NOT EXISTS idx_activity_ts      ON activity_log(ts DESC);
CREATE INDEX IF NOT EXISTS idx_activity_project ON activity_log(project_slug);

-- ---------------------------------------------------------------------------
-- Indices extras (mesmos que a API cria no boot, ver internal/db/additiveIndexes).
--
-- ATENCAO: nenhum indice aqui pode citar uma coluna ADITIVA da API
-- (projects.tech_stack/last_activity, activity_log.project_id,
-- eca_registry.project_id, qa_executions.execution_id, providers.status...).
-- Este arquivo e aplicado tambem sobre bancos ANTIGOS, criados pelo
-- scripts/schema_init.sql do T5: o CREATE TABLE IF NOT EXISTS nao altera a
-- tabela que ja existe, entao um indice sobre coluna inexistente quebraria a
-- migracao inteira ("no such column"). Os indices dessas colunas sao criados
-- pelo ALTER/CREATE INDEX IF NOT EXISTS da API, que roda depois.
-- ---------------------------------------------------------------------------
CREATE INDEX IF NOT EXISTS idx_repos_head_commit    ON repos(head_commit);
CREATE INDEX IF NOT EXISTS idx_worktrees_task       ON worktrees(task_id);
CREATE INDEX IF NOT EXISTS idx_qa_executions_tc     ON qa_executions(tc_id);
CREATE INDEX IF NOT EXISTS idx_agents_status        ON agents(status);
CREATE INDEX IF NOT EXISTS idx_activity_actor       ON activity_log(actor);
CREATE INDEX IF NOT EXISTS idx_activity_kind        ON activity_log(kind);
