-- Keep a one-way fingerprint of known provider email ids after account erasure.
-- Verified delayed callbacks can then be rejected without retaining the id.
CREATE TABLE public.account_deletion_provider_hashes (
    provider_email_hash text PRIMARY KEY
        CHECK (provider_email_hash ~ '^[0-9a-f]{64}$'),
    clerk_subject text NOT NULL
        REFERENCES public.account_deletions (clerk_subject) ON DELETE CASCADE,
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX account_deletion_provider_hashes_subject_idx
    ON public.account_deletion_provider_hashes (clerk_subject);

ALTER TABLE public.account_deletion_provider_hashes ENABLE ROW LEVEL SECURITY;

-- Rollback removes only suppression fingerprints. Reapplying after rollback
-- permits old provider callbacks to be recorded again for retained tombstones.
-- Keep migration 017 and its deletion journal intact.
