-- Phase 5: approximate reminders for date-only deadlines.
--
-- Additive and forward-only: this adds one nullable column to tasks and two
-- nullable columns to reminder_jobs, widens two CHECK constraints and leaves
-- every existing row untouched. Exact-instant reminders keep their existing
-- timing, columns and behavior. Nothing is backfilled: an old date-only task
-- has no reminder_zone, so it gets no approximate reminder and no time or
-- cutoff is invented for it.
--
-- Design:
--   * tasks.reminder_zone is the browser's IANA zone captured when a date-only
--     task is created. It is deliberately NOT stored in tasks.time_zone: that
--     column belongs exclusively to an exact instant and keeps the deadline
--     shape and the .ics DATE export honest. A zone is allowed only when
--     due_precision = 'date'.
--   * reminder_jobs.due_day holds the task's calendar day for the two new
--     timings; reminder_jobs.reminder_zone is the zone used to pick the local
--     reminder hour. true_deadline stays NULL for them: there is no exact
--     cutoff to store or to guard against.
--   * 'day_before_approx' fires at 18:00 local on the day before the deadline,
--     'morning_of_approx' at 09:00 local on the deadline day. Both are
--     approximate by construction and never claim a guaranteed cutoff.
--   * Legacy 'at_deadline' jobs keep working: their true_deadline equals due_at
--     and both new columns stay NULL, which the widened CHECK still allows.
--
-- Rollback: ALTER TABLE public.tasks DROP CONSTRAINT
-- tasks_reminder_zone_shape_check; ALTER TABLE public.tasks DROP COLUMN
-- reminder_zone; ALTER TABLE public.reminder_jobs DROP CONSTRAINT
-- reminder_jobs_true_deadline_check, ADD CONSTRAINT
-- reminder_jobs_true_deadline_check CHECK (reminder_timing = 'at_deadline' OR
-- (reminder_timing IN ('24h_before','2h_before') AND true_deadline IS NOT NULL));
-- ALTER TABLE public.reminder_jobs DROP CONSTRAINT
-- reminder_jobs_reminder_timing_check, ADD CONSTRAINT
-- reminder_jobs_reminder_timing_check CHECK (reminder_timing IN
-- ('24h_before','2h_before','at_deadline')); ALTER TABLE public.reminder_jobs
-- DROP COLUMN due_day; ALTER TABLE public.reminder_jobs DROP COLUMN
-- reminder_zone. Drop only after deleting any rows that use the new timings.

ALTER TABLE public.tasks
    ADD COLUMN reminder_zone text CHECK (
        reminder_zone IS NULL OR (
            length(reminder_zone) BETWEEN 1 AND 64
            AND (reminder_zone = 'UTC' OR reminder_zone ~ '^[A-Za-z_][A-Za-z0-9_+-]*(/[A-Za-z0-9_+-]+)+$')
        )
    );

ALTER TABLE public.tasks
    ADD CONSTRAINT tasks_reminder_zone_shape_check
    CHECK (reminder_zone IS NULL OR due_precision = 'date');

ALTER TABLE public.reminder_jobs
    DROP CONSTRAINT IF EXISTS reminder_jobs_reminder_timing_check,
    ADD CONSTRAINT reminder_jobs_reminder_timing_check
    CHECK (reminder_timing IN (
        '24h_before', '2h_before', 'at_deadline',
        'day_before_approx', 'morning_of_approx'));

ALTER TABLE public.reminder_jobs ADD COLUMN due_day date;

ALTER TABLE public.reminder_jobs ADD COLUMN reminder_zone text CHECK (
    reminder_zone IS NULL OR length(reminder_zone) BETWEEN 1 AND 64
);

ALTER TABLE public.reminder_jobs
    DROP CONSTRAINT IF EXISTS reminder_jobs_true_deadline_check,
    ADD CONSTRAINT reminder_jobs_true_deadline_check CHECK (
        (reminder_timing IN ('24h_before', '2h_before')
            AND true_deadline IS NOT NULL AND due_day IS NULL AND reminder_zone IS NULL)
        OR (reminder_timing = 'at_deadline')
        OR (reminder_timing IN ('day_before_approx', 'morning_of_approx')
            AND due_day IS NOT NULL AND reminder_zone IS NOT NULL AND true_deadline IS NULL)
    );
