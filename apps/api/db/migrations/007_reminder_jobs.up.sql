-- Owner-scoped durable reminder jobs for confirmed instant deadlines.
--
-- Additive and forward-only: this creates one new table and changes no
-- existing table or row. Existing applications, tasks and user rows are
-- untouched, and older code paths keep working unchanged. Date-only
-- deadlines get no reminder (the product rule is that they never invent a
-- time or alarm), so only exact-instant deadlines have jobs.
--
-- Delivery states are explicit; a job never claims to be delivered unless an
-- adapter confirms it:
--   scheduled   due_at reached; waiting for a worker to pick it up
--   leased      a worker holds the lease (lease_token, lease_until) and is
--               dispatching
--   accepted    the adapter accepted the send request; delivery unconfirmed
--   delivered   the adapter confirmed delivery
--   uncertain   ambiguous timeout: the send may or may not have happened
--   failed      retryable failure; retry_at carries the backoff schedule
--   exhausted   retry limit reached; terminal until the task is rescheduled
--   suppressed  stale or disabled: the reminder must not fire
--
-- Identity and retries: the job id is the notification identity the provider
-- sees, and retries keep the same job row, so every dispatch uses the same
-- notification id and provider callbacks can be deduplicated by callback_key.
--
-- Stale suppression: the tasks module reactivates or suppresses this table
-- when a task is created, completed, cancelled or rescheduled, and the
-- applications module marks jobs for a deleted application's tasks. The
-- worker re-checks that the task still exists before dispatching, so a
-- deleted account's jobs never fire even when deletion bypasses the API.
--
-- Rollback: DROP TABLE public.reminder_jobs;
-- This discards reminder jobs only. Export reminder data first if any must
-- be kept; do not reverse once reminders are in use.

CREATE TABLE public.reminder_jobs (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    -- Owner is text to match applications.user_id: a Clerk subject owns new
    -- records, an explicitly bound owner owns old ones. Worker queries and
    -- every store method filter by this owner.
    user_id text NOT NULL,
    task_id uuid NOT NULL,
    -- When the reminder is due. For instant-deadline tasks this is the stored
    -- task.due_at.
    due_at timestamptz NOT NULL,
    delivery_state text NOT NULL DEFAULT 'scheduled'
        CHECK (delivery_state IN (
            'scheduled', 'leased', 'accepted', 'delivered', 'uncertain',
            'failed', 'exhausted', 'suppressed')),
    lease_token text,
    lease_until timestamptz,
    retry_count int NOT NULL DEFAULT 0,
    retry_at timestamptz,
    callback_key text,
    delivery_note text,
    suppress_reason text
        CHECK (suppress_reason IS NULL OR suppress_reason IN (
            'completed', 'cancelled', 'deleted', 'rescheduled', 'disabled')),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);

-- One job per task; the worker polls due jobs in due order.
CREATE UNIQUE INDEX reminder_jobs_task_idx ON public.reminder_jobs (task_id);
CREATE INDEX reminder_jobs_claim_idx ON public.reminder_jobs (due_at);

ALTER TABLE public.reminder_jobs ENABLE ROW LEVEL SECURITY;
