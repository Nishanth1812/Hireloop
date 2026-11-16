-- Reversible ownership schema evolution for the Supabase Auth -> Clerk migration.
--
-- Existing applications.user_id (uuid FK to auth.users(id)) ownership is
-- preserved: existing UUID values are stored unchanged as text. New Clerk
-- signups are stored with their Clerk subject (e.g. user_123...) as the
-- stable owner, without inserting any artificial auth.users row.
--
-- The foreign key to auth.users is dropped because Clerk subjects are not
-- auth.users rows. No Clerk subject is ever auto-linked from an email address;
-- Clerk -> Supabase bindings are established explicitly in
-- public.user_identities (migration 002).
--
-- This migration must not be applied to a live database until the operator has
-- reviewed it. Existing user data is assumed unknown; nothing is backfilled.
--
-- Rollback: only safe before any Clerk subject has been stored as an owner.
--   ALTER TABLE public.applications ALTER COLUMN user_id TYPE uuid
--     USING user_id::uuid;
--   ALTER TABLE public.applications ADD CONSTRAINT applications_user_id_fkey
--     FOREIGN KEY (user_id) REFERENCES auth.users(id) ON DELETE CASCADE;

DO $$
DECLARE
    constraint_name text;
BEGIN
    SELECT conname INTO constraint_name
    FROM pg_constraint
    WHERE conrelid = 'public.applications'::regclass
      AND contype = 'f'
      AND pg_get_constraintdef(oid) LIKE '%user_id%';
    IF constraint_name IS NOT NULL THEN
        EXECUTE format('ALTER TABLE public.applications DROP CONSTRAINT %I', constraint_name);
    END IF;
END $$;

ALTER TABLE public.applications ALTER COLUMN user_id TYPE text USING user_id::text;
