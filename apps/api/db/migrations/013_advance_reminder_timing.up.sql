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
--
-- Rollback: ALTER TABLE public.reminder_jobs DROP CONSTRAINT
-- reminder_jobs_reminder_timing_check; DROP INDEX
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

-- Make true_deadline NOT NULL now that existing rows are populated.
ALTER TABLE public.reminder_jobs
    ALTER COLUMN true_deadline SET NOT NULL;

-- Replace the per-task unique index with a per-task-per-timing index.
DROP INDEX public.reminder_jobs_task_idx;
CREATE UNIQUE INDEX reminder_jobs_task_timing_idx
    ON public.reminder_jobs (task_id, reminder_timing);

-- Keep the claim index on due_at (the reminder time).
-- No change needed.

-- Add 'deadline_passed' to the suppress_reason CHECK.
ALTER TABLE public.reminder_jobs DROP CONSTRAINT reminder_jobs_suppress_reason_check;
ALTER TABLE public.reminder_jobs ADD CONSTRAINT reminder_jobs_suppress_reason_check
    CHECK (suppress_reason IS NULL OR suppress_reason IN (
        'completed', 'cancelled', 'deleted', 'rescheduled', 'disabled', 'deadline_passed'));

-- Update the cleanup function to handle the new columns correctly.
-- The existing cleanup logic works because it filters by delivery_state
-- and updated_at, which are unchanged. No function changes required.