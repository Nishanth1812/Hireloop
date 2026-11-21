-- Additive retention and cleanup wiring for Phase 4 reminders.
-- Forward-only; migrations 001-010 are untouched and no existing row is rewritten.
--
-- Why this migration exists (Phase 4 safety review findings):
--   * reminder_delivery_payloads stores recipient, subject, and body on the first
--     attempt of a (job, version). These contain PII (verified primary email) and
--     the task title. They are only needed for retries under the same idempotency
--     key while the job is non-terminal. After a terminal state, the payload is
--     no longer required and should be removed to limit PII retention.
--   * reminder_provider_emails maps a provider email id to the job and version
--     that sent it. The primary key on provider_email_id is correct (one email id
--     belongs to exactly one send), but a unique constraint on (job_id,
--     delivery_version) prevents accidental duplicate history rows for the same
--     logical send.
--   * reminder_email_events accumulates pending events that arrive before the
--     provider mapping is persisted. If the job is suppressed or rescheduled
--     before the mapping write, those pending events never reconcile. A bounded
--     cleanup removes stale pending rows while preserving applied/ignored
--     evidence for audit.
--
-- Retention policy (documented, not enforced by schema):
--   * reminder_delivery_payloads: eligible for deletion 7 days after the
--     associated job reaches a terminal state (delivered, exhausted,
--     suppressed, permanent_failed). The 7-day window covers the Resend
--     24-hour idempotency key retention plus worker backoff cycles.
--   * reminder_provider_emails: kept indefinitely as durable delivery evidence.
--   * reminder_email_events: pending rows older than 7 days for jobs that are
--     terminal or have been rescheduled (delivery_version changed) are
--     eligible for deletion. Applied/ignored rows are kept as evidence.
--
-- Rollback: DROP FUNCTION public.cleanup_reminder_retention();
-- DROP INDEX reminder_provider_emails_job_version_idx;
-- This adds a cleanup function and one index; no data columns are removed.

-- Prevent duplicate history rows for the same logical send (job + version).
-- The provider_email_id PK already ensures one row per provider email id;
-- this index guards against application bugs that might try to insert the
-- same job/version twice with different provider email ids (which should
-- never happen in correct operation).
CREATE UNIQUE INDEX reminder_provider_emails_job_version_idx
    ON public.reminder_provider_emails (job_id, delivery_version);

-- Cleanup function for reminder retention.
-- Deletes:
--   1. Delivery payloads for jobs that reached a terminal state more than
--      7 days ago (payload no longer needed for retries).
--   2. Pending email events older than 7 days where the correlated job is
--      terminal or has been rescheduled (delivery_version changed), so the
--      event will never be applied to the live job.
-- Returns the count of deleted rows for observability.
CREATE OR REPLACE FUNCTION public.cleanup_reminder_retention()
RETURNS TABLE(deleted_payloads int, deleted_pending_events int)
LANGUAGE plpgsql
AS $$
DECLARE
    payload_cutoff timestamptz := now() - interval '7 days';
    event_cutoff timestamptz := now() - interval '7 days';
    payload_count int;
    event_count int;
BEGIN
    -- 1. Delete delivery payloads for terminal jobs older than 7 days.
    -- The payload is only needed while the job can still be retried under
    -- the same idempotency key. Once terminal, no further dispatches occur.
    DELETE FROM public.reminder_delivery_payloads dp
    USING public.reminder_jobs rj
    WHERE dp.job_id = rj.id
      AND rj.delivery_state IN ('delivered', 'exhausted', 'suppressed', 'permanent_failed')
      AND rj.updated_at < payload_cutoff
    RETURNING 1 INTO payload_count;

    GET DIAGNOSTICS payload_count = ROW_COUNT;

    -- 2. Delete pending email events older than 7 days that will never
    --    apply to the live job. An event is stuck pending if:
    --    a) The job is terminal (no further state transitions allowed), OR
    --    b) The job was rescheduled (delivery_version changed), so the
    --       event's target version no longer matches the live version.
    --    Applied/ignored events are kept as durable evidence.
    DELETE FROM public.reminder_email_events ee
    WHERE ee.resolution = 'pending'
      AND ee.received_at < event_cutoff
      AND EXISTS (
          SELECT 1 FROM public.reminder_jobs rj
          WHERE rj.id = ee.job_id
            AND (
                rj.delivery_state IN ('delivered', 'exhausted', 'suppressed', 'permanent_failed')
                OR rj.delivery_version <> COALESCE(ee.delivery_version, 0)
            )
      );

    GET DIAGNOSTICS event_count = ROW_COUNT;

    RETURN QUERY SELECT payload_count, event_count;
END;
$$;

-- Grant execute to the backend role (adjust role name as needed for your
-- deployment; the migration runner uses the schema owner).
GRANT EXECUTE ON FUNCTION public.cleanup_reminder_retention() TO CURRENT_USER;