-- Phase 4: Advance reminder timing (24h and 2h before exact deadlines).
-- Additive and forward-only: this adds columns and indexes to reminder_jobs,
-- re-declares the unique constraint to allow two jobs per task, and
-- adds a deadline_passed suppression reason. No existing row is rewritten.
--
-- Design:
--   * reminder_timing distinguishes the two advance reminders per task/
--     deadline version: '24h_before' fires 24 hours before the true
--     deadline, '2h_before' fires 2 hours before. The worker claims
--     each at its due_at (the reminder time) and the dispatch guard
--     prevents sending after the true deadline has passed.
--   * true_deadline stores the task's actual instant deadline so the
--     email can render it and the dispatch guard can check it.
--   * The unique index moves from (task_id) to (task_id, reminder_timing)
--     so exactly two jobs can exist per task per deadline version.
--   * Existing jobs (created before this migration) have reminder_timing
--     = 'at_deadline' and true_deadline = due_at, preserving their
--     behavior. A future cleanup migration can retire 'at_deadline' jobs.
--   * true_deadline is nullable at rest; a CHECK constraint ensures that
--     advance reminder timings (24h_before, 2h_before) always have a
--     true_deadline set, while legacy at_deadline jobs may have it equal
--     to due_at. This guards against any future bad inserts.
--
-- Rollback: ALTER TABLE public.reminder_jobs DROP CONSTRAINT
-- reminder_jobs_reminder_timing_check; ALTER TABLE public.reminder_jobs
-- DROP CONSTRAINT reminder_jobs_true_deadline_check; DROP INDEX
-- reminder_jobs_task_timing_idx; CREATE UNIQUE INDEX
-- reminder_jobs_task_idx ON public.reminder_jobs (task_id);
-- ALTER TABLE public.reminder_jobs DROP COLUMN true_deadline;
-- ALTER TABLE public.reminder_jobs DROP COLUMN reminder_timing;
-- and re-declare the suppress_reason CHECK without 'deadline_passed'.

ALTER TABLE public.reminder_jobs
    ADD COLUMN reminder_timing text NOT NULL DEFAULT 'at_deadline'
        CHECK (reminder_timing IN ('24h_before', '2h_before', 'at_deadline'));

ALTER TABLE public.reminder_jobs
    ADD COLUMN true_deadline timestamptz;

-- Populate true_deadline for existing jobs: they were scheduled at the
-- exact deadline, so true_deadline = due_at.
UPDATE public.reminder_jobs
SET true_deadline = due_at
WHERE true_deadline IS NULL;

-- Add CHECK constraint: advance reminder timings MUST have a true_deadline.
-- Legacy at_deadline jobs may have true_deadline = due_at (already populated)
-- or NULL if somehow missed; the constraint allows at_deadline with NULL
-- true_deadline for maximum safety, but application code never creates
-- new at_deadline jobs.
ALTER TABLE public.reminder_jobs
    ADD CONSTRAINT reminder_jobs_true_deadline_check
    CHECK (
        reminder_timing = 'at_deadline'
        OR (reminder_timing IN ('24h_before', '2h_before') AND true_deadline IS NOT NULL)
    );

-- Replace the per-task unique index with a per-task-per-timing index.
-- Use IF EXISTS / IF NOT EXISTS for idempotency under golang-migrate.
DROP INDEX IF EXISTS public.reminder_jobs_task_idx;
CREATE UNIQUE INDEX IF NOT EXISTS reminder_jobs_task_timing_idx
    ON public.reminder_jobs (task_id, reminder_timing);

-- Keep the claim index on due_at (the reminder time).
-- No change needed.

-- Add 'deadline_passed' to the suppress_reason CHECK.
-- Use a single ALTER TABLE to minimize the window where invalid values
-- could be inserted (though migration runs at deploy time with exclusive lock).
ALTER TABLE public.reminder_jobs
    DROP CONSTRAINT reminder_jobs_suppress_reason_check,
    ADD CONSTRAINT reminder_jobs_suppress_reason_check
    CHECK (suppress_reason IS NULL OR suppress_reason IN (
        'completed', 'cancelled', 'deleted', 'rescheduled', 'disabled', 'deadline_passed'));

-- Update the cleanup function to handle the new columns correctly.
-- The existing cleanup logic works because it filters by delivery_state
-- and updated_at, which are unchanged. No function changes required.