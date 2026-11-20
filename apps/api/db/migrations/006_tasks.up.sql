-- Owner-scoped tasks with explicit deadline precision.
--
-- Additive and forward-only: this creates one new table and changes no existing
-- table or row. Existing applications and their data are untouched, and older
-- code paths keep working unchanged.
--
-- Deadline model (the smallest that preserves the product's semantics):
--   * 'none'    - an action with no deadline. No date, time or zone is invented.
--   * 'date'    - a calendar day only. No time, no zone. A date-only deadline
--                 is never silently turned into an end-of-day instant.
--   * 'instant' - a UTC instant plus the original IANA zone the user chose.
--                 The client sends the wall-clock time in that zone; the API
--                 resolves it to an instant and keeps the zone for display.
-- The CHECK makes the variants mutually exclusive, so a date-only deadline can
-- never acquire a time or zone, and an instant can never lose its zone.
--
-- Rollback: DROP TABLE public.tasks;
-- This discards task data created since the migration but does not affect
-- applications or any other existing record. Export task data first if any of
-- it must be kept. Do not reverse once tasks are in use.

CREATE TABLE public.tasks (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    -- Owner is text to match applications.user_id after migration 003: a Clerk
    -- subject owns new records, an explicitly bound Supabase id owns old ones.
    user_id text NOT NULL,
    application_id uuid NOT NULL REFERENCES public.applications(id) ON DELETE CASCADE,
    title text NOT NULL CHECK (length(trim(title)) BETWEEN 1 AND 200),
    status text NOT NULL DEFAULT 'open' CHECK (status IN ('open', 'done', 'cancelled')),
    due_precision text NOT NULL DEFAULT 'none' CHECK (due_precision IN ('none', 'date', 'instant')),
    due_date date,
    due_at timestamptz,
    time_zone text CHECK (time_zone IS NULL OR length(time_zone) BETWEEN 1 AND 64),
    created_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT tasks_deadline_shape CHECK (
        (due_precision = 'none'    AND due_date IS NULL     AND due_at IS NULL AND time_zone IS NULL) OR
        (due_precision = 'date'    AND due_date IS NOT NULL AND due_at IS NULL AND time_zone IS NULL) OR
        (due_precision = 'instant' AND due_date IS NULL     AND due_at IS NOT NULL AND time_zone IS NOT NULL)
    )
);

-- Open actions first, soonest deadline first; tasks without a deadline last.
-- The sort key is UTC only as a deterministic ordering device; it is never
-- shown to a user as a date-only deadline's time or zone.
CREATE INDEX tasks_owner_status_due_idx ON public.tasks (
    user_id,
    status,
    COALESCE(due_date, (due_at AT TIME ZONE 'UTC')::date),
    due_at
);

ALTER TABLE public.tasks ENABLE ROW LEVEL SECURITY;
