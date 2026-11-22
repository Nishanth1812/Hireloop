-- Phase 5 account lifecycle: a durable deletion journal and a write guard.
--
-- The journal is service-only. It records the opaque identifiers needed to
-- finish a Clerk deletion and to erase a restored pre-deletion backup: the
-- verified Clerk subject, the optionally bound Supabase UUID, a pending or
-- completed state, and timestamps. It never stores email, task text or any
-- other account content, and it has no end-user RLS policy: only the backend,
-- which scopes every query by the verified session, may read or write it.
--
-- Tombstones are retained indefinitely until a backup/PITR retention horizon
-- is defined and enforced. Clearing a tombstone is never how a deleted Clerk
-- subject becomes usable again; a new account is a new Clerk subject.
--
-- Write guard: a trigger on every owner-scoped table takes the same
-- transaction-scoped advisory lock as the deletion path, then rejects the
-- write when a tombstone matches either owner identifier. A write already in
-- flight holds the lock until it commits, so deletion waits for it and then
-- erases its rows; a write that starts after deletion begins blocks until the
-- tombstone is visible and then fails. This is a serialized invariant, not a
-- racy preflight check, and it covers writes that bypass the HTTP layer.
--
-- Rollback: the down migration refuses to run while the journal is non-empty,
-- because dropping tombstones would reopen restoring pre-deletion backups.
-- On an empty journal it removes the guard triggers, function and table.

CREATE TABLE public.account_deletions (
    clerk_subject text PRIMARY KEY,
    supabase_user_id text,
    state text NOT NULL DEFAULT 'pending'
        CHECK (state IN ('pending', 'completed')),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    completed_at timestamptz
);

-- At most one live tombstone may name a given Supabase owner. The partial
-- index lets unbound subjects keep a NULL binding.
CREATE UNIQUE INDEX account_deletions_supabase_idx
    ON public.account_deletions (supabase_user_id)
    WHERE supabase_user_id IS NOT NULL;

ALTER TABLE public.account_deletions ENABLE ROW LEVEL SECURITY;

CREATE OR REPLACE FUNCTION public.account_deletion_guard()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
    owner text := NEW.user_id;
    blocked boolean;
BEGIN
    IF owner IS NULL THEN
        RETURN NEW;
    END IF;
    -- Serialize against the deletion transaction, which locks both owner
    -- identifiers before writing the tombstone and erasing data.
    PERFORM pg_advisory_xact_lock(hashtext(owner)::bigint);
    SELECT EXISTS (
        SELECT 1 FROM public.account_deletions d
        WHERE d.clerk_subject = owner OR d.supabase_user_id = owner
    ) INTO blocked;
    IF blocked THEN
        RAISE EXCEPTION 'account owner is deleted'
            USING ERRCODE = 'integrity_constraint_violation';
    END IF;
    RETURN NEW;
END $$;

CREATE TRIGGER account_deletion_guard
    BEFORE INSERT OR UPDATE ON public.applications
    FOR EACH ROW EXECUTE FUNCTION public.account_deletion_guard();

CREATE TRIGGER account_deletion_guard
    BEFORE INSERT OR UPDATE ON public.application_events
    FOR EACH ROW EXECUTE FUNCTION public.account_deletion_guard();

CREATE TRIGGER account_deletion_guard
    BEFORE INSERT OR UPDATE ON public.tasks
    FOR EACH ROW EXECUTE FUNCTION public.account_deletion_guard();

CREATE TRIGGER account_deletion_guard
    BEFORE INSERT OR UPDATE ON public.reminder_jobs
    FOR EACH ROW EXECUTE FUNCTION public.account_deletion_guard();

CREATE TRIGGER account_deletion_guard
    BEFORE INSERT OR UPDATE ON public.user_notification_settings
    FOR EACH ROW EXECUTE FUNCTION public.account_deletion_guard();

CREATE TRIGGER account_deletion_guard
    BEFORE INSERT OR UPDATE ON public.reminder_provider_emails
    FOR EACH ROW EXECUTE FUNCTION public.account_deletion_guard();
