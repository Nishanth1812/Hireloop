-- Add composite index on tasks(user_id, application_id) to support
-- SuppressByApplication's subquery efficiently.
-- This index makes the IN (SELECT id FROM tasks WHERE application_id = $2 AND user_id = $1)
-- pattern scale without a sequential scan on tasks.
--
-- Rollback: DROP INDEX IF EXISTS tasks_user_app_idx;

CREATE INDEX IF NOT EXISTS tasks_user_app_idx ON public.tasks (user_id, application_id);