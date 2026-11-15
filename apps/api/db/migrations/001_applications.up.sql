CREATE TABLE public.applications (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id uuid NOT NULL REFERENCES auth.users(id) ON DELETE CASCADE,
    company text NOT NULL CHECK (length(trim(company)) BETWEEN 1 AND 200),
    role text NOT NULL CHECK (length(trim(role)) BETWEEN 1 AND 200),
    status text NOT NULL CHECK (status IN ('saved', 'applied')),
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX applications_user_created_idx ON public.applications (user_id, created_at DESC, id DESC);

ALTER TABLE public.applications ENABLE ROW LEVEL SECURITY;
