-- Adds the remaining agreed manual-capture fields to applications:
-- job posting URL, campus/off-campus channel, requisition (job) ID, the
-- date the candidate applied, and free-text notes.
--
-- Every column is nullable and additive, so existing rows stay valid and
-- readable with no backfill. The API reads them with COALESCE(..., '') and
-- treats an empty value as "not recorded"; no value is invented for existing
-- applications.
--
-- `applied_on` is date-only by design: the product plan forbids inventing a
-- time or zone for a date the user gave as a date. Anything that needs a time
-- belongs to a task deadline (Phase 3), not to this column.
--
-- Length bounds mirror the Go validation so the database rejects the same
-- oversized input the API does. `url` is bounded more tightly than a generic
-- text field because a posting link is short; `notes` is bounded more loosely
-- because candidates write prose there.
--
-- The channel CHECK keeps the two campus values closed; the API exposes the
-- same two values and rejects anything else, so the constraint is a backstop
-- against writes that bypass the API rather than a validation layer.
--
-- Rollback: the columns are additive and unused by older code paths, so this
-- migration is safe to reverse:
--   ALTER TABLE public.applications DROP COLUMN IF EXISTS notes;
--   ALTER TABLE public.applications DROP COLUMN IF EXISTS applied_on;
--   ALTER TABLE public.applications DROP COLUMN IF EXISTS job_id;
--   ALTER TABLE public.applications DROP COLUMN IF EXISTS channel;
--   ALTER TABLE public.applications DROP COLUMN IF EXISTS url;

ALTER TABLE public.applications
    ADD COLUMN url text
        CHECK (url IS NULL OR length(trim(url)) <= 2048);

ALTER TABLE public.applications
    ADD COLUMN channel text
        CHECK (channel IS NULL OR channel IN ('campus', 'off-campus'));

ALTER TABLE public.applications
    ADD COLUMN job_id text
        CHECK (job_id IS NULL OR length(trim(job_id)) <= 200);

ALTER TABLE public.applications
    ADD COLUMN applied_on date;

ALTER TABLE public.applications
    ADD COLUMN notes text
        CHECK (notes IS NULL OR length(trim(notes)) <= 2000);