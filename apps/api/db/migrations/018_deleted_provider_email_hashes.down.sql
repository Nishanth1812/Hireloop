DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM public.account_deletion_provider_hashes) THEN
        RAISE EXCEPTION 'cannot drop provider callback suppression history while fingerprints remain';
    END IF;
END $$;

DROP TABLE IF EXISTS public.account_deletion_provider_hashes;
