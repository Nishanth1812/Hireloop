-- Additive Resend wiring for the Phase 4 scheduler: versioned delivery
-- states, a persistent provider email mapping, and an event ledger.
--
-- Additive and forward-only: this adds two columns to reminder_jobs and one
-- table, and re-declares the existing delivery_state CHECK with the same
-- values plus the new 'permanent_failed' state (widening an enum requires
-- the constraint to be re-declared; no existing row changes). Existing
-- jobs keep their stored state and get delivery_version 0 until the next
-- Schedule reactivation bumps it to 1.
--
-- Delivery versioning and the provider mapping:
--   delivery_version  Bumped on every Schedule reactivation. Together with
--                     provider_email_id it identifies one logical send: the
--                     worker's idempotency key ("job:<id>:v<version>") and
--                     the byte-stable payload are built from it, so a
--                     rescheduled job gets a new idempotency key and a new
--                     provider email id, while retries of one dispatch keep
--                     the same key and payload.
--   provider_email_id The Resend email id the worker received from the
--                     provider for the current version. Cleared on every
--                     reactivation and suppression, so a provider event for
--                     an old version never correlates to the rescheduled
--                     job: old callbacks must not overwrite rescheduled
--                     jobs.
--   provider_version  The delivery version provider_email_id belongs to.
--
-- Event ledger: reminder_email_events records every verified Resend webhook
-- event keyed by the provider's svix-id, which is its replay identity. The
-- unique provider_event_id is the persistent dedup: an exact replay inserts
-- no row and is ignored, robust across reschedules. job_id is the job the
-- event correlated to when first seen; NULL when the event arrived before
-- the provider email id was recorded.
--
-- Documented behavior:
--   * Inherent suppress/send race: a suppress that lands after the worker
--     started a dispatch cannot recall the email; it only stops future
--     dispatches. Delivery evidence is therefore monotonic: once a state
--     with evidence (delivered, exhausted, suppressed, permanent_failed, or
--     a lease on a non-terminal job) is recorded, weaker transitions cannot
--     overwrite it.
--   * Resend keeps an idempotency key for 24 hours: reusing the same key
--     with the same payload inside that window returns the original result,
--     which is safe because the payload is byte-stable per version. A
--     version is never reused after a reactivation, so a resend after the
--     window gets a fresh key and a fresh provider email id.
--
-- Rollback: DROP TABLE public.reminder_email_events; ALTER TABLE
-- public.reminder_jobs DROP COLUMN provider_version; ALTER TABLE
-- public.reminder_jobs DROP COLUMN provider_email_id; ALTER TABLE
-- public.reminder_jobs DROP COLUMN delivery_version; and re-declare the
-- original delivery_state CHECK. This discards only the correlation
-- columns and the event ledger.

ALTER TABLE public.reminder_jobs DROP CONSTRAINT reminder_jobs_delivery_state_check;
ALTER TABLE public.reminder_jobs ADD CONSTRAINT reminder_jobs_delivery_state_check
    CHECK (delivery_state IN (
        'scheduled', 'leased', 'accepted', 'delivered', 'uncertain',
        'failed', 'exhausted', 'suppressed', 'permanent_failed'));

ALTER TABLE public.reminder_jobs
    ADD COLUMN delivery_version int NOT NULL DEFAULT 0
        CHECK (delivery_version >= 0);
ALTER TABLE public.reminder_jobs
    ADD COLUMN provider_email_id text;
ALTER TABLE public.reminder_jobs
    ADD COLUMN provider_version int
        CHECK (provider_version IS NULL OR provider_version >= 0);

CREATE INDEX reminder_jobs_provider_email_id_idx
    ON public.reminder_jobs (provider_email_id);

CREATE TABLE public.reminder_email_events (
    provider_event_id text PRIMARY KEY,
    provider_email_id text NOT NULL,
    job_id uuid,
    event_type text NOT NULL,
    received_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX reminder_email_events_provider_email_id_idx
    ON public.reminder_email_events (provider_email_id);

ALTER TABLE public.reminder_email_events ENABLE ROW LEVEL SECURITY;
