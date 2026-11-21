-- Additive fixes for Phase 4 reminder retention cleanup (followup to 011).
-- Forward-only; migrations 001-011 are untouched and no existing row is rewritten.
--
-- Fixes addressed (Phase 4 safety review followup):
--   * DELETE RETURNING INTO fails when multiple rows match: removed RETURNING
--     INTO and rely on GET DIAGNOSTICS for row counts.
--   * Old payloads on reschedule may never expire: added cleanup for payloads
--     where delivery_version < current job delivery_version (superseded versions).
--     These are no longer needed for retries because the worker only uses the
--     current version's idempotency key.
--   * Uncorrelated pending events remain indefinitely: added cleanup for
--     reminder_email_events with job_id IS NULL older than 7 days. These are
--     events that arrived before the provider mapping was persisted and were
--     never reconciled (e.g., the send failed before SetProviderEmailID ran).
--   * Unique index on (job_id, delivery_version) retained as a guard against
--     application bugs that might map one logical send to two provider email
--     ids. The worker's retry window (minutes) is well within Resend's 24-hour
--     idempotency key retention, so legitimate retries reuse the same key and
--     provider email id.
--
-- Retention policy (updated, documented, not enforced by schema):
--   * reminder_delivery_payloads: eligible for deletion when EITHER:
--       - The associated job reached a terminal state more than 7 days ago, OR
--       - The payload's delivery_version is less than the job's current
--         delivery_version (superseded by reschedule). The 7-day window for
--         terminal jobs covers Resend's 24-hour idempotency key retention plus
--         worker backoff cycles. Superseded versions are cleaned immediately
--         because they can never be retried.
--   * reminder_provider_emails: kept indefinitely as durable delivery evidence.
--   * reminder_email_events: pending rows older than 7 days are eligible for
--     deletion when EITHER:
--       - The correlated job is terminal, OR
--       - The job was rescheduled (delivery_version changed), OR
--       - The event has no job_id (uncorrelated; never reconciled).
--     Applied/ignored rows are kept as evidence.
--
-- Rollback: DROP FUNCTION public.cleanup_reminder_retention();
-- This replaces the cleanup function; no data columns are removed.

-- Cleanup function for reminder retention (replaces 011 version).
-- Deletes:
--   1. Delivery payloads for jobs that reached a terminal state more than
--      7 days ago (payload no longer needed for retries).
--   2. Delivery payloads for superseded versions (delivery_version < current
--      job delivery_version) regardless of job state (never retried).
--   3. Pending email events older than 7 days that will never apply to the
--      live job. An event is stuck pending if:
--      a) The job is terminal (no further state transitions allowed), OR
--      b) The job was rescheduled (delivery_version changed), so the
--         event's target version no longer matches the live version, OR
--      c) The event has no job_id (uncorrelated; mapping never persisted).
--   Returns the count of deleted rows for observability.
CREATE OR REPLACE FUNCTION public.cleanup_reminder_retention()
RETURNS TABLE(deleted_payloads int, deleted_pending_events int)
LANGUAGE plpgsql
AS $$
DECLARE
    payload_cutoff timestamptz := now() - interval '7 days';
    event_cutoff timestamptz := now() - interval '7 days';
    payload_count int;
    superseded_count int;
    event_count int;
BEGIN
    -- 1. Delete delivery payloads for terminal jobs older than 7 days.
    -- The payload is only needed while the job can still be retried under
    -- the same idempotency key. Once terminal, no further dispatches occur.
    DELETE FROM public.reminder_delivery_payloads dp
    USING public.reminder_jobs rj
    WHERE dp.job_id = rj.id
      AND rj.delivery_state IN ('delivered', 'exhausted', 'suppressed', 'permanent_failed')
      AND rj.updated_at < payload_cutoff;

    GET DIAGNOSTICS payload_count = ROW_COUNT;

    -- 2. Delete delivery payloads for superseded versions (rescheduled jobs).
    -- When a job is rescheduled, delivery_version is bumped and the old
    -- version's payload can never be used for retries (the worker only uses
    -- the current version's idempotency key). Clean these up immediately.
    DELETE FROM public.reminder_delivery_payloads dp
    USING public.reminder_jobs rj
    WHERE dp.job_id = rj.id
      AND dp.delivery_version < rj.delivery_version;

    GET DIAGNOSTICS superseded_count = ROW_COUNT;
    payload_count := payload_count + superseded_count;

    -- 3. Delete pending email events older than 7 days that will never
    --    apply to the live job. An event is stuck pending if:
    --    a) The job is terminal (no further state transitions allowed), OR
    --    b) The job was rescheduled (delivery_version changed), so the
    --       event's target version no longer matches the live version, OR
    --    c) The event has no job_id (uncorrelated; mapping never persisted).
    --    Applied/ignored events are kept as durable evidence.
    DELETE FROM public.reminder_email_events ee
    WHERE ee.resolution = 'pending'
      AND ee.received_at < event_cutoff
      AND (
          -- Case a/b: job exists and is terminal or rescheduled
          EXISTS (
              SELECT 1 FROM public.reminder_jobs rj
              WHERE rj.id = ee.job_id
                AND (
                    rj.delivery_state IN ('delivered', 'exhausted', 'suppressed', 'permanent_failed')
                    OR rj.delivery_version <> COALESCE(ee.delivery_version, 0)
                )
          )
          -- Case c: uncorrelated event (no job_id, never reconciled)
          OR ee.job_id IS NULL
      );

    GET DIAGNOSTICS event_count = ROW_COUNT;

    RETURN QUERY SELECT payload_count, event_count;
END;
$$;

-- Grant execute to the backend role (adjust role name as needed for your
-- deployment; the migration runner uses the schema owner).
GRANT EXECUTE ON FUNCTION public.cleanup_reminder_retention() TO CURRENT_USER;