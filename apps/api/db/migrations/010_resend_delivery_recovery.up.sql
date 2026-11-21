-- Additive recovery wiring for Phase 4 reminders. Forward-only; migrations
-- 001-009 are untouched and no existing row is rewritten.
--
-- Why this migration exists (security review findings):
--   * The webhook ledger insert used to commit before the job transition, so a
--     crash between them made a retry look like a replay and lost the event.
--     The ledger now carries the requested state and a resolution, so the
--     insert and the transition can be applied in one transaction, and a
--     pending event can be reconciled later.
--   * A provider callback can arrive before the worker persisted the provider
--     email id, and that write could silently fail. A durable provider-email
--     history plus pending-event resolution lets such events be recovered
--     instead of lost.
--   * A late event for a suppressed or rescheduled job must still record
--     honest provider evidence. The history maps the email id to the job and
--     version that actually sent it even after the live mapping is cleared,
--     so evidence can be attributed without reviving the job.
--   * The recipient and the full message body are persisted on the first
--     attempt of a (job, version), so Resend retries reuse an identical
--     payload under the same idempotency key even if the task title or the
--     Clerk verified email changes in between. No user-entered application
--     address is ever stored: the recipient is the verified primary email.
--
-- Rollback: ALTER TABLE public.reminder_email_events DROP COLUMN resolved_at,
-- DROP COLUMN delivery_version, DROP COLUMN target_state, DROP COLUMN resolution;
-- DROP TABLE public.reminder_delivery_payloads;
-- DROP TABLE public.reminder_provider_emails;
-- This discards recovery metadata and the first-attempt payload copies only;
-- reminder jobs and the event ledger itself are left intact.

ALTER TABLE public.reminder_email_events
    ADD COLUMN target_state text,
    ADD COLUMN delivery_version int CHECK (delivery_version IS NULL OR delivery_version >= 0),
    ADD COLUMN resolution text NOT NULL DEFAULT 'pending'
        CHECK (resolution IN ('pending', 'applied', 'ignored')),
    ADD COLUMN resolved_at timestamptz;

CREATE TABLE public.reminder_provider_emails (
    provider_email_id text PRIMARY KEY,
    job_id uuid NOT NULL REFERENCES public.reminder_jobs(id) ON DELETE CASCADE,
    user_id text NOT NULL,
    delivery_version int NOT NULL CHECK (delivery_version >= 0),
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX reminder_provider_emails_job_idx
    ON public.reminder_provider_emails (job_id, delivery_version);

CREATE TABLE public.reminder_delivery_payloads (
    job_id uuid NOT NULL REFERENCES public.reminder_jobs(id) ON DELETE CASCADE,
    delivery_version int NOT NULL CHECK (delivery_version >= 0),
    recipient text NOT NULL,
    subject text NOT NULL,
    text_body text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (job_id, delivery_version)
);

ALTER TABLE public.reminder_provider_emails ENABLE ROW LEVEL SECURITY;
ALTER TABLE public.reminder_delivery_payloads ENABLE ROW LEVEL SECURITY;
