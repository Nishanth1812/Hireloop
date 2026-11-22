# Hireloop

Hireloop is a job application and deadline tracker under active local development. It is a modular monolith with a Go API backed by PostgreSQL (Supabase) and a Next.js web app; Clerk provides sign-in. This is a working prototype, not a released product.

## Features

- **Clerk authentication.** Sign in with Google through Clerk. The API verifies the session JWT against Clerk's JWKS (signature, issuer, expiry, and optional `azp` authorized parties) and scopes every row to the verified owner, never to a value from the request body.
- **Account export and deletion.** Download a complete, owner-scoped JSON archive of applications, their stage history, tasks and notification settings (`GET /api/account/export`), or erase the account (`DELETE /api/account`). Deletion writes a durable tombstone, erases every Hireloop-owned row for both the Clerk subject and any bound Supabase owner, bans and deletes that exact Clerk user through Clerk's Backend API, and blocks tombstoned subjects on every protected route — including already-issued JWTs. Clerk outages leave a durable `pending` record that the worker retries; `204` is returned only after Clerk confirmation, otherwise `202`. Tombstones retain only opaque identifiers and are never cleared to reuse a deleted subject. See [docs/backup-restore-and-deletion.md](docs/backup-restore-and-deletion.md) and [docs/phase-5-checklist.md](docs/phase-5-checklist.md). A reminder already handed to the provider before deletion cannot be recalled.
- **Application tracker.** Manually create, search/filter, update and delete applications across `saved`, `applied`, `interviewing`, `offer`, `accepted`, `rejected` and `withdrawn` stages. Each application has a detail page with its recorded stage history and linked tasks; stage changes can include a short note. Required fields are `company` and `role`. Optional fields are `source`, `address`, `url`, `channel` (`campus` or `off-campus`), `job_id`, `applied_on` (date-only) and `notes`.
- **Today — open actions.** Each application can carry tasks with an `open` / `done` / `cancelled` lifecycle. The default Today view lists open actions soonest deadline first, with deadline-free actions last; a `filter=all` view also shows finished actions. Deadlines have an explicit `due_precision`: `none`, a date-only `date`, or an exact `instant` given as a local wall time plus a full IANA `time_zone`, resolved to a UTC instant. Date-only deadlines are displayed as a day with "cutoff time unspecified" — no time is invented. A local time that does not exist (spring-forward gap) or is ambiguous (fall-back overlap, including 30-minute shifts such as `Australia/Lord_Howe`) is rejected with 400 instead of silently choosing one; abbreviations like `IST` and server-local `Local` are never accepted.
- **Deadlines.** Today shows the stored deadline on each task. Date-only deadlines still have no invented time or cutoff: the browser's IANA zone is captured on creation solely for two **approximate** email reminders (the day before at 18:00 and the morning of at 09:00 local). Both are labelled approximate and are **not** a guaranteed cutoff; they use calendar-day arithmetic and the zone's local-noon offset so a DST change keeps the intended day and hour. A task with no usable browser zone, an old date-only task, or a past date gets no approximate reminder. Exact deadlines keep their 24 h and 2 h advance reminders. See [docs/date-only-reminders.md](docs/date-only-reminders.md).
- **Per-task calendar export.** Each task can be exported as a `.ics` calendar entry (`GET /api/tasks/{id}/ics`): a date-only deadline becomes `DUE;VALUE=DATE` with no time or zone, an exact deadline becomes a UTC `DUE` value preserving the stored instant, and a task without a deadline has no `DUE`. The reminder zone is deliberately not exported as a `TZID`, alarm or trigger: it is an approximate nudge, not part of the deadline. Exports are owner-scoped, and invalid or empty deadline fields are rejected rather than exported as invented facts.
- **Durable reminders.** Confirmed instant deadlines get eligible reminder jobs in PostgreSQL (`reminder_jobs`) for 24 hours and 2 hours before the deadline; date-only deadlines with a captured browser zone get the two approximate local nudges described above. Approximate jobs store the due day and zone but no `true_deadline`: the worker's guard compares the local calendar day, so a late job on the due day still fires and one on a later day is suppressed. Every job kind honours completion, cancellation, deletion, rescheduling, the per-owner `reminders_enabled` preference, leases, retries, explicit delivery states (`scheduled`, `leased`, `accepted`, `delivered`, `uncertain`, `failed`, `exhausted`, `suppressed`, `permanent_failed`) and provider callback dedup. Jobs survive worker restarts and are re-claimed when leases expire with the same notification identity. No delivery is implied until a provider confirms it. Without a configured dispatcher, the worker leaves jobs scheduled.
  - **Resend dispatcher.** The worker dispatches through a Resend adapter that resolves the owner's verified primary email (never a user-entered address) on the first attempt of a version, persists the complete payload (recipient, subject, body) as a byte-stable snapshot, and reuses it on every retry so the Resend idempotency key (`job:<id>:v<version>`) is never reused with a different body. The provider email id Resend returns is persisted atomically with a durable history entry and any webhooks that arrived before the mapping are reconciled.
  - **Provider callbacks & webhook ledger.** Every verified Resend webhook event is recorded in `reminder_email_events` keyed by the provider's svix-id (replay identity), with the requested state transition and a resolution (`pending`/`applied`/`ignored`). The ledger insert and the job transition commit in one transaction so a crash cannot lose an event. Events arriving before the provider mapping are kept `pending` and reconciled when the mapping lands. Late events for suppressed or rescheduled jobs are recorded as honest evidence (`ignored`) through the durable provider-email history and never revive the job.
  - **Retention & cleanup.** First-attempt payloads contain the recipient and task title. They are eligible for deletion seven days after a job reaches a terminal state or when a newer deadline supersedes them. Old pending webhook events are also removed when they cannot be correlated or applied; applied and ignored evidence is retained. The worker runs cleanup hourly, even when sending is disabled, so the worker must be running for cleanup to occur. Resend retains idempotency keys for 24 hours; retries after that period are treated as uncertain rather than sent again.

## Repository layout

- `apps/api/` — Go 1.25 HTTP API: `cmd/api` (startup), `internal/` (Clerk verification, identity mapping, applications, tasks, HTTP handlers), `db/migrations/` (ordered SQL migrations embedded into the binary).
- `apps/web/` — Next.js 16 app: `src/app` (screens, components, tests), `src/lib` (typed API client).
- `apps/web/.env.example` — tracked template for web environment variables.
- The root `.env`, `apps/web/.env.local`, and local planning documents are ignored by Git; they are not part of the tracked source.

## Prerequisites

- Bun 1.4+ and Go 1.25+.
- A Clerk instance (Frontend API origin and JWKS URL) for sign-in.
- Optional: a PostgreSQL/Supabase **session-pooler** connection string for persistence. The API starts without it for local checks, but application and task routes then have no storage and return 503.

## Configuration

Variables are loaded per file; there is no global dotenv.

- Root `.env` (ignored, read by the API): `DATABASE_URL`, `CLERK_ISSUER`, `CLERK_JWKS_URL`, `CLERK_SECRET_KEY` (server-side Backend API key for account export/deletion and worker retries), optional `CLERK_AUTHORIZED_PARTIES` (comma-separated allowed origins), optional `HIRELOOP_API_ADDR` (default `127.0.0.1:8080`). The worker also reads `CLERK_SECRET_KEY` from its environment; without it pending deletions stay blocked and are reported as unavailable rather than silently dropped.
- `apps/web/.env.local` (ignored, read by the web app and by `bun run start`): `NEXT_PUBLIC_CLERK_PUBLISHABLE_KEY`, optional `HIRELOOP_API_URL` (default `http://127.0.0.1:8080`).
- `HIRELOOP_TEST_DATABASE_URL` — test only, never a runtime setting (see [Tests](#tests-and-data-safety)).

`CLERK_ISSUER` and `CLERK_JWKS_URL` must be set together, both over HTTPS and sharing a host; `CLERK_JWKS_URL` points at the instance's `/.well-known/jwks.json`. Without Clerk configuration, authenticated requests fail closed with 503. Next.js rewrites `/api/*` to `HIRELOOP_API_URL`, so browser calls keep the app's origin; when changing the port, set `HIRELOOP_API_ADDR` and `HIRELOOP_API_URL` to the same address.

Never commit a `.env` file, Clerk secret key, service-role key, database password or access token.

## Start and checks

From `apps/api`:

```sh
bun run start     # loads the root .env and apps/web/.env.local, then runs the Go API
go test ./...
go vet ./...
```

The reminder worker runs from `apps/api/cmd/worker` against the same `DATABASE_URL`; until a mail provider is integrated it uses the null dispatcher, so no delivery is implied.

`bun run start` is the API's only script. If `DATABASE_URL` is set, startup applies only pending embedded migrations before opening the listener; a migration failure stops startup. Use the Supabase **session pooler, not transaction pooling**. Migrations are ordered SQL files guarded by an advisory lock and a history table: an up-to-date database is a no-op. Review pending migrations before starting against an existing database. If a migration fails, inspect the history and database state and take a backup before any repair; do not blindly reset the history or drop data. An existing hand-created `applications` table is not adopted automatically.

The API listens on `127.0.0.1:8080` by default. `GET /health` returns `{"status":"ok"}` while the process is up; it does not check the database.

From `apps/web`:

```sh
bun install
bun run dev
bun run test
bun run lint
bun run build
```

`bun run build` needs `NEXT_PUBLIC_CLERK_PUBLISHABLE_KEY` set for prerendering. See [apps/web/README.md](apps/web/README.md) for web-specific setup.

## Tests and data safety

`go test ./...` and `bun run test` use local mocks and need no database. Two Go test groups are opt-in through `HIRELOOP_TEST_DATABASE_URL`: the migration idempotence test and the SQL-backed `PostgresStore` integration tests. The account lifecycle SQL tests additionally accept `HIRELOOP_ACCOUNTS_TEST_DATABASE_URL` (falling back to `HIRELOOP_TEST_DATABASE_URL`). **All of them apply pending migrations to the configured database**, so point these variables only at disposable test databases — never production. Integration tests write only temporary rows under freshly generated random owners and delete exactly those rows in cleanup; they never read or write another owner's data. Use dedicated test databases to avoid affecting user data. Web component tests use Happy DOM and need no browser.

The restore drill (`TestRestoreDrillDeletedAccountDoesNotReturn`) is opt-in and destructive. It requires two separate newly created disposable databases via `HIRELOOP_BACKUP_SOURCE_URL` and `HIRELOOP_BACKUP_RESTORE_URL`, plus `pg_dump`/`pg_restore` (optionally `HIRELOOP_PG_DUMP`/`HIRELOOP_PG_RESTORE`). It proves that a naive logical restore resurrects a deleted account, then proves that importing the live deletion journal and reconciling erases it from all ten data tables while preserving an unrelated owner. See [docs/backup-restore-and-deletion.md](docs/backup-restore-and-deletion.md). Run Go test packages serially (`go test -p 1 ./...`) when several SQL suites share a disposable cluster.

## API

All `/api/*` routes except `/health` require `Authorization: Bearer <Clerk session JWT>`. The owner always comes from the verified token. The old `/api/v1/` prefix is gone; routes are under `/api/`.

| Method & path | Purpose |
| --- | --- |
| `GET /health` | Liveness check; no authentication or database check. |
| `GET /api/me` | Current user (`id`, `email`) resolved from the Clerk subject. |
| `POST /api/applications` | Create an application (201). |
| `GET /api/applications` | List the caller's applications as a bounded page. |
| `GET /api/applications/{id}` | Read one application (200/404). |
| `GET /api/applications/{id}/events` | Read its owner-scoped stage history as a bounded page. |
| `PATCH /api/applications/{id}` | Update one or more fields (200/404); an explicit empty string clears an optional field. |
| `DELETE /api/applications/{id}` | Delete an application (204/404). |
| `POST /api/tasks` | Create a task on the caller's own application (201/404). |
| `GET /api/tasks` | List tasks as a bounded page (`filter=open` by default; `filter=all` includes `done` and `cancelled`; `application_id` scopes the list to an owned application). |
| `GET /api/tasks/{id}` | Read one task (200/404). |
| `PATCH /api/tasks/{id}` | Update a task, including status and the whole deadline shape (200/404). |
| `GET /api/tasks/{id}/ics` | Export one task as a single-task calendar entry (`text/calendar`). |
| `GET /api/notification-settings` | Read the caller's notification controls: `{"reminders_enabled": true\|false}` (default `true`). |
| `PATCH /api/notification-settings` | Set the caller's `reminders_enabled` boolean. |
| `GET /api/account/export` | Download the caller's complete account archive (`200`, `application/json`, `no-store`, attachment). |
| `DELETE /api/account` | Erase the caller's account: `204` only after Clerk deletion is confirmed, otherwise `202` while pending. |
| `POST /api/webhook/resend` | Receive verified Resend delivery events (webhook, not authenticated). |

Missing or invalid tokens return 401; a transient verification failure returns 503. Missing or other-owner rows return 404 without revealing ownership. Malformed IDs, unknown fields or query parameters, and invalid or out-of-range values return 400; unavailable database/configuration returns 503. Applications list parameters are `q` (case-insensitive over company, role and job ID, max 100 characters), `status`, `channel`, `cursor` and `page_size`; task list parameters are `filter`, `application_id`, `cursor` and `page_size`. Application event queries accept `cursor` and `page_size`; event cursors are bound to their application.

**Pagination contract.** List endpoints return bounded keyset pages of the shape `{"items":[…],"page_size":25,"total":N,"next_cursor":"…"|null}`. Application pages also include counts for all seven stages; those counts ignore the `status` filter. `total` and `counts` describe every matching row, not just the page. `page_size` is 1–50 (default 25) and is rejected, not clamped, when out of range. Applications are ordered by `created_at DESC, id DESC`; tasks by deadline day, `due_at`, `created_at`, `id`, with deadline-free actions last; application events are ordered by `occurred_at DESC, id DESC`. Cursors are bound to the filters/view that minted them and rejected with 400 when those change. Unknown parameters and malformed or oversized cursors also return 400. Count and page share one read-only `REPEATABLE READ` snapshot, and seeking after the cursor avoids offset shifts when rows are inserted or deleted before it; updates that change a task’s sort key can still move it between pages; a cursor remains usable even if its anchor row was deleted, and a cursor past the end yields an empty page with a null cursor.

Database access is restricted at both layers: the applications table has row-level security with no direct client policies, the backend role needs its own grants, and every query filters by the authenticated owner. Task data lives in its own additive table with a deadline-shape constraint; its rollback drops only task rows.
