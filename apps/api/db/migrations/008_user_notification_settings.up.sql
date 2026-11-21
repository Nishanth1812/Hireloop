-- User-level notification controls: one row per owner (a Clerk subject or a
-- bound owner) toggling whether reminders are dispatched at all.
--
-- Additive and forward-only: creates one new table; no existing row changes.
-- A missing row means the default: reminders enabled.
--
-- Rollback: DROP TABLE public.user_notification_settings;
-- This discards notification preferences only.

CREATE TABLE public.user_notification_settings (
    user_id text PRIMARY KEY,
    reminders_enabled boolean NOT NULL DEFAULT true,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);

ALTER TABLE public.user_notification_settings ENABLE ROW LEVEL SECURITY;
