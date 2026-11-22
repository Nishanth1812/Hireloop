-- Reverse migration 017 only when the deletion journal is empty.
--
-- A non-empty journal is retained until every backup and PITR point that
-- could contain pre-deletion data is out of scope. Dropping it earlier would
-- make a later restore resurrect a deleted account, so refuse instead of
-- silently discarding that history.

DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM public.account_deletions) THEN
        RAISE EXCEPTION 'refusing to roll back account_deletions: deletion history is not empty';
    END IF;
END $$;

DROP TRIGGER IF EXISTS account_deletion_guard ON public.applications;
DROP TRIGGER IF EXISTS account_deletion_guard ON public.application_events;
DROP TRIGGER IF EXISTS account_deletion_guard ON public.tasks;
DROP TRIGGER IF EXISTS account_deletion_guard ON public.reminder_jobs;
DROP TRIGGER IF EXISTS account_deletion_guard ON public.user_notification_settings;
DROP TRIGGER IF EXISTS account_deletion_guard ON public.reminder_provider_emails;
DROP FUNCTION IF EXISTS public.account_deletion_guard();
DROP TABLE IF EXISTS public.account_deletions;
