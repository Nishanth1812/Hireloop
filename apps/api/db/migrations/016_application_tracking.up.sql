-- Phase 6 application lifecycle schema.
--
-- The canonical stage field stays `applications.status`; its check is widened
-- from saved/applied to the full fixed pipeline plus the two terminal
-- outcomes. Existing rows already hold saved or applied, so they keep their
-- value and need no rewrite. No historical events are invented for them: a
-- missing event means "history was not recorded before this migration", which
-- is more honest than a fabricated timestamp.
--
-- The append-only event ledger records exactly two kinds of change: `created`
-- at application creation and `stage_changed` when the status actually moves.
-- Ordinary edits to contact fields or free-form notes do not produce events.
-- `from_status` is NULL for a creation event; `note` is optional and bounded
-- to the same 500 characters the API enforces. occurred_at is server-generated
-- so a client cannot backdate history.
--
-- RLS is enabled with no end-user policy: only the backend, which already
-- scopes every query by the verified owner, may read or write these rows.
-- The FK cascade means removing an application removes its history with it.
--
-- Rollback: remove the applications_status_check constraint and restore the
-- original two-value check, then remove the application_events table. That
-- second step is irreversible data loss: every recorded stage change is
-- discarded. Export the ledger before reversing this migration if any of it
-- must be kept, and do not reverse once the wider stages are in use.

DO $$
DECLARE
    constraint_name text;
BEGIN
    SELECT conname INTO constraint_name
    FROM pg_constraint
    WHERE conrelid = 'public.applications'::regclass
      AND contype = 'c'
      AND pg_get_constraintdef(oid) LIKE '%status%';
    IF constraint_name IS NOT NULL THEN
        EXECUTE format('ALTER TABLE public.applications DROP CONSTRAINT %I', constraint_name);
    END IF;
END $$;

ALTER TABLE public.applications ADD CONSTRAINT applications_status_check
    CHECK (status IN ('saved', 'applied', 'interviewing', 'offer', 'accepted', 'rejected', 'withdrawn'));

CREATE TABLE public.application_events (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id text NOT NULL,
    application_id uuid NOT NULL REFERENCES public.applications(id) ON DELETE CASCADE,
    event_type text NOT NULL CHECK (event_type IN ('created', 'stage_changed')),
    from_status text,
    to_status text,
    note text CHECK (note IS NULL OR length(trim(note)) <= 500),
    occurred_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX application_events_owner_app_time_idx
    ON public.application_events (user_id, application_id, occurred_at DESC, id DESC);

ALTER TABLE public.application_events ENABLE ROW LEVEL SECURITY;
