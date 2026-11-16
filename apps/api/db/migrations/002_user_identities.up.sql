-- Reversible compatibility mapping for the Supabase Auth -> Clerk migration.
--
-- Existing applications.user_id (uuid FK to auth.users(id)) ownership is
-- preserved. Each Clerk subject is bound explicitly to one Supabase owner.
-- Linking is never inferred from matching email addresses, which prevents
-- insecure identity linking between providers.
--
-- This migration is part of the repository but must not be applied to a live
-- database until the operator has reviewed it and established the bindings.
-- Rollback: DROP TABLE public.user_identities;

CREATE TABLE public.user_identities (
    clerk_subject text PRIMARY KEY,
    supabase_user_id uuid NOT NULL REFERENCES auth.users(id) ON DELETE CASCADE,
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX user_identities_supabase_user_id_idx ON public.user_identities (supabase_user_id);
