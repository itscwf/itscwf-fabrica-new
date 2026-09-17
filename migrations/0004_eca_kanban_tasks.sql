-- 0004_eca_kanban_tasks.sql
-- Tabela de vínculo entre ECAs e tasks do Hermes Kanban.
-- Mantida em sync pelo watchdog eca_kanban_sync.py.
-- ECA vai para "done" quando TODAS as suas tasks têm status "done".

CREATE TABLE IF NOT EXISTS eca_kanban_tasks (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    eca_id      TEXT    NOT NULL,
    task_id     TEXT    NOT NULL,
    title       TEXT    NOT NULL,
    assignee    TEXT    NOT NULL,
    status      TEXT    NOT NULL DEFAULT 'triage',
    phase       INTEGER NOT NULL,
    note        TEXT,
    created_at  TEXT    NOT NULL,
    updated_at  TEXT    NOT NULL,
    UNIQUE (eca_id, task_id)
);

CREATE INDEX IF NOT EXISTS idx_eca_kanban_tasks_eca_id ON eca_kanban_tasks(eca_id);
