-- Adds the optional manual-capture fields source and address to applications.
--
-- Both are nullable so existing rows stay valid and readable without a
-- backfill: the API reads them with COALESCE(..., '') and treats an empty
-- string as "not recorded". No value is invented for existing applications.
--
-- Length bounds mirror the Go validation (<= 200 characters after trimming)
-- so the database rejects the same oversized input the API does.
--
-- Rollback: the columns are additive and unused by older code paths, so this
-- migration is safe to reverse before any source/address values matter:
--   ALTER TABLE public.applications DROP COLUMN IF EXISTS address;
--   ALTER TABLE public.applications DROP COLUMN IF EXISTS source;

ALTER TABLE public.applications
    ADD COLUMN source text
        CHECK (source IS NULL OR length(trim(source)) <= 200);

ALTER TABLE public.applications
    ADD COLUMN address text
        CHECK (address IS NULL OR length(trim(address)) <= 200);