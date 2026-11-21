# Hireloop

Hireloop is a job application and deadline tracker under active local development. It is a modular monolith with a Go API backed by PostgreSQL (Supabase) and a Next.js web app; Clerk provides sign-in. This is a working prototype, not a released product.

## Features

- **Clerk authentication.** Sign in with Google through Clerk. The API verifies the session JWT against Clerk's JWKS (signature, issuer, expiry, and optional `azp` authorized parties) and scopes every row to the verified owner, never to a value from the request body.
- **Application tracker.** Manually create, read, search/filter, update and delete applications. Required fields are `company` and `role`; `status` is `saved` or `applied`. Optional fields are `source`, `address`, `url`, `channel` (`campus` or `off-campus`), `job_id`, `applied_on` (date-only) and `notes`.
- **Today — open actions.** Each application can carry tasks with an `open` / `done` / `cancelled` lifecycle. The default Today view lists open actions soonest deadline first, with deadline-free actions last; a `filter=all` view also shows finished actions. Deadlines have an explicit `due_precision`: `none`, a date-only `date`, or an exact `instant` given as a local wall time plus a full IANA `time_zone`, resolved to a UTC instant. Date-only deadlines are displayed as a day with "cutoff time unspecified" — no time is invented. A local time that does not exist (spring-forward gap) or is ambiguous (fall-back overlap, including 30-minute shifts such as `Australia/Lord_Howe`) is rejected with 400 instead of silently choosing one; abbreviations like `IST` and server-local `Local` are never accepted.
- **Deadlines.** Today shows the stored deadline on each task. Date-only deadlines have no invented time; no notification or alarm is sent.
- **Per-task calendar export.** Each task can be exported as a `.ics` calendar entry (`GET /api/tasks/{id}/ics`): a date-only deadline becomes `DUE;VALUE=DATE` with no time or zone, an exact deadline becomes a UTC `DUE` value preserving the stored instant, and a task without a deadline has no `DUE`. Exports are owner-scoped, and invalid or empty deadline fields are rejected rather than exported as invented facts.
- **Durable reminders.** Confirmed instant deadlines get a reminder job in PostgreSQL (`reminder_jobs`). A worker claims due jobs with leases, retries failed dispatches with backoff, records explicit delivery states (`scheduled`, `leased`, `accepted`, `delivered`, `uncertain`, `failed`, `exhausted`, `suppressed`) and deduplicates provider callbacks. Jobs are suppressed when a task completes, is cancelled, is deleted or is rescheduled; jobs survive worker restarts and are re-claimed when leases expire with the same notification identity. No delivery is implied until a provider confirms it — the null dispatcher reports failure so jobs land in `exhausted` rather than claiming to be delivered. A per-owner `reminders_enabled` preference (default on) can be read and set through the notification-settings routes.

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

- Root `.env` (ignored, read by the API): `DATABASE_URL`, `CLERK_ISSUER`, `CLERK_JWKS_URL`, optional `CLERK_AUTHORIZED_PARTIES` (comma-separated allowed origins), optional `HIRELOOP_API_ADDR` (default `127.0.0.1:8080`).
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

`go test ./...` and `bun run test` use local mocks and need no database. Two Go test groups are opt-in through `HIRELOOP_TEST_DATABASE_URL`: the migration idempotence test and the SQL-backed `PostgresStore` integration tests. **Both apply pending migrations to the configured database**, so point that variable only at a disposable test database — never production. Integration tests write only temporary rows under freshly generated random owners and delete exactly those rows in cleanup; they never read or write another owner's data and never create or delete auth users. Use a dedicated test database to avoid affecting user data. Web component tests use Happy DOM and need no browser.

## API

All `/api/*` routes except `/health` require `Authorization: Bearer <Clerk session JWT>`. The owner always comes from the verified token. The old `/api/v1/` prefix is gone; routes are under `/api/`.

| Method & path | Purpose |
| --- | --- |
| `GET /health` | Liveness check; no authentication or database check. |
| `GET /api/me` | Current user (`id`, `email`) resolved from the Clerk subject. |
| `POST /api/applications` | Create an application (201). |
| `GET /api/applications` | List the caller's applications as a bounded page. |
| `GET /api/applications/{id}` | Read one application (200/404). |
| `PATCH /api/applications/{id}` | Update one or more fields (200/404); an explicit empty string clears an optional field. |
| `DELETE /api/applications/{id}` | Delete an application (204/404). |
| `POST /api/tasks` | Create a task on the caller's own application (201/404). |
| `GET /api/tasks` | List tasks as a bounded page (`filter=open` by default; `filter=all` includes `done` and `cancelled`). |
| `GET /api/tasks/{id}` | Read one task (200/404). |
| `PATCH /api/tasks/{id}` | Update a task, including status and the whole deadline shape (200/404). |
| `GET /api/tasks/{id}/ics` | Export one task as a single-task calendar entry (`text/calendar`). |
| `GET /api/notification-settings` | Read the caller's notification controls: `{"reminders_enabled": true\|false}` (default `true`). |
| `PATCH /api/notification-settings` | Set the caller's `reminders_enabled` boolean. |

Missing or invalid tokens return 401; a transient verification failure returns 503. Missing or other-owner rows return 404 without revealing ownership. Malformed IDs, unknown fields or query parameters, and invalid or out-of-range values return 400; unavailable database/configuration returns 503. Applications list parameters are `q` (case-insensitive over company, role and job ID, max 100 characters), `status`, `channel`, `cursor` and `page_size`; task list parameters are `filter`, `cursor` and `page_size`.

**Pagination contract.** Both list endpoints return bounded keyset pages of the shape `{"items":[…],"page_size":25,"total":N,"next_cursor":"…"|null}`; applications also include `counts` with `saved` and `applied` totals that ignore the `status` filter. `total` and `counts` describe every matching row, not just the page, so a page is never presented as the whole list and filtering never misstates the summary. `page_size` is 1–50 (default 25) and is rejected, not clamped, when out of range. Applications are ordered by `created_at DESC, id DESC`; tasks by deadline day, `due_at`, `created_at`, `id`, with deadline-free actions last. `next_cursor` is null on the last page and is passed back as `cursor`; it is bound to the filters/view that minted it and is rejected with 400 when they change (restart at page one). Unknown parameters and malformed or oversized cursors also return 400. Count and page share one read-only `REPEATABLE READ` snapshot, and seeking after the cursor avoids offset shifts when rows are inserted or deleted before it; updates that change a task’s sort key can still move it between pages; a cursor remains usable even if its anchor row was deleted, and a cursor past the end yields an empty page with a null cursor.

Database access is restricted at both layers: the applications table has row-level security with no direct client policies, the backend role needs its own grants, and every query filters by the authenticated owner. Task data lives in its own additive table with a deadline-shape constraint; its rollback drops only task rows.
