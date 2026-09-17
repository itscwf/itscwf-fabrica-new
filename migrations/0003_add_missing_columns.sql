-- 0003_add_missing_columns.sql
-- projects.kanban_board: exigida pelo modelo Go (models.Project.KanbanBoard),
-- ausente no DDL 0001 (o drift-guard impede editar 0001 após aplicada).
--
-- O motor Go (applyOne) roda cada arquivo UMA vez e registra o checksum em
-- schema_migrations, então em operação normal este ALTER executa só na
-- primeira subida após este deploy. Bancos que já têm a coluna (o bootstrap
-- GORM a criou no ambiente de produção antes do drift-guard existir) falhariam
-- com "duplicate column"; para esses, a coluna vem de internal/db.additiveColumns,
-- que roda DEPOIS de migrations.Up e é tolerante a colunas existentes.
-- Conclusão: manter o ALTER simples e garantir que o bookkeeping deste banco
-- fique em sincronia (uma linha para 0003) é responsabilidade do deploy —
-- ver comentário no cmd/server/main.go.

ALTER TABLE projects ADD COLUMN kanban_board TEXT;
