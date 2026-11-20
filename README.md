# Hireloop

Hireloop is a job application and deadline tracker. The product roadmap is maintained locally under `docs/`.

## API development

Requires Bun and Go 1.25 or later. Put server settings such as `DATABASE_URL`, `CLERK_ISSUER`, and `CLERK_JWKS_URL` in the root `.env`. From `apps/api`, start the API with:

```sh
bun run start
```

This command loads the root `.env` and `apps/web/.env.local`, then starts the API. If `DATABASE_URL` is set, startup applies only pending embedded migrations before opening the listener. Migration failure stops startup; migrations use the existing advisory lock and history table and never reset schema or data.

The API listens on `http://127.0.0.1:8080`. If that port belongs to another service, set `HIRELOOP_API_ADDR` (for example `127.0.0.1:8081`) for the API and the matching `HIRELOOP_API_URL` (`http://127.0.0.1:8081`) for the web app. `GET /health` returns `{"status":"ok"}` when the API process is responding. This endpoint does not check the database.

Run checks from `apps/api`:

```sh
go test ./...
go vet ./...
```

## Authentication API

`GET /api/me` accepts a Clerk session JWT in an `Authorization: Bearer <token>` header. The API verifies the token's signature, issuer, expiry, and authorized party against Clerk's JWKS, then resolves the Clerk subject to its explicitly bound Supabase owner (an unbound Clerk subject is treated as a new signup that owns itself). Missing or invalid tokens receive 401; a transient verification failure receives 503.

To configure the API for Clerk, set `CLERK_JWKS_URL` to your instance's `/.well-known/jwks.json` endpoint and `CLERK_ISSUER` to its Frontend API origin (both HTTPS and sharing the same host). Optionally set `CLERK_AUTHORIZED_PARTIES` to a comma-separated list of origins matched against the token's `azp` claim. Without Clerk configuration, authenticated requests fail closed with 503. Supabase is used for PostgreSQL persistence only.

Never put a service-role key, database password or access token in source control.

## Application APIs

All routes require the Clerk session JWT described above. The owner is taken from the verified identity, never from the request body.

- `POST /api/applications` accepts JSON such as `{"company":"Acme","role":"Engineer","status":"saved"}`. `company` and `role` are required and `status` must be `saved` or `applied`. Optional fields are `source`, `address`, `url`, `channel` (`campus` or `off-campus`), `job_id`, `applied_on` (date-only, `YYYY-MM-DD`) and `notes`. It returns the created application with status 201; invalid input returns 400.
- `GET /api/applications` returns a bounded page of the current user's applications, newest first, as `{"items":[...],"page":1,"page_size":25,"total":42,"total_pages":2,"counts":{"saved":40,"applied":2}}`. `total` and `counts` describe every matching row, so a single page never misstates the size of the list; `counts` ignores the `status` filter so the summary stays truthful while filtering. Supported parameters are `q` (case-insensitive search over company, role and job ID, max 100 characters), `status`, `channel`, `page` (1-based) and `page_size` (1–50, default 25). Unknown parameters and out-of-range values return 400 rather than being ignored or clamped.
- `GET /api/applications/{id}` returns a specific application by ID with status 200 if it exists and belongs to the authenticated user. Malformed IDs return 400; missing or other-user records return 404 without revealing ownership; database errors return 503.
- `PATCH /api/applications/{id}` accepts one or more valid fields from the same set as `POST` and returns the updated application with status 200. An explicit empty string clears an optional field. Malformed IDs or input return 400; missing or other-user records return 404 without revealing ownership; database errors return 503.
- `DELETE /api/applications/{id}` deletes a specific application and returns 204. Malformed IDs return 400; missing or other-user records return 404 without revealing ownership; database errors return 503.

Application routes return 503 if database configuration or access is unavailable. Configure `DATABASE_URL` with your Supabase session-pooler connection string only when ready. Do not commit it. Migrations are ordered SQL files embedded from `apps/api/db/migrations/`; `migrate.Up` applies only pending versions and treats an up-to-date database as success. The initial migration creates the applications table, enables row-level security, and grants no direct client policies; the backend database role must have appropriate access, and backend queries enforce the authenticated owner.

Migrations use an advisory lock and a migration history table; use the Supabase session pooler, not transaction pooling. If a migration fails, inspect the migration history and database state before repairing it; do not blindly reset the history or drop data. An existing manually created applications table is not automatically adopted. Use a backup and a reviewed corrective migration when needed. API routes previously prefixed `/api/v1/` have moved to `/api/`.

Default authentication and API tests use local mocks. Temporary Clerk development users have also completed signed-in browser CRUD against PostgreSQL, including reload persistence and cross-account GET/PATCH/DELETE denial through the web proxy; those users and their test records were removed afterward. A complete Google OAuth round trip and visual/screen-reader checks remain pending. No database is required to run the default tests. Two groups of tests are opt-in through `DATABASE_URL` (or `HIRELOOP_TEST_DATABASE_URL`): the migration idempotence test, and SQL-backed store integration tests that run the real migrations and `PostgresStore` against the configured database using temporary rows under random owners that they delete afterward. `GET /api/applications` uses bounded `LIMIT`/`OFFSET` pagination; the count and page share one read-only `REPEATABLE READ` snapshot, and a page past the last returns empty without ever forming an offset, so an oversized `page` cannot overflow. OFFSET paging is not stable across concurrent inserts/deletes between separate requests; keyset pagination would remove that but is out of scope.

## Web development

The Next.js app in `apps/web` provides Clerk Google sign-in and the manual application workflow. Set `NEXT_PUBLIC_CLERK_PUBLISHABLE_KEY` in `apps/web/.env.local`; set `CLERK_ISSUER` and `CLERK_JWKS_URL` for that same instance in the root `.env`. Set `DATABASE_URL` there when enabling Supabase persistence. Start the API as described above, then run `bun install` and `bun run dev` from `apps/web`. The web app proxies `/api/*` to `HIRELOOP_API_URL`, which defaults to `http://127.0.0.1:8080`.

Run web checks from `apps/web` with `bun run test`, `bun run lint`, and `bun run build`. Component tests use Happy DOM to exercise token failures, sign-out, late responses during account changes, filter and pagination reloads, and the labelled form controls. The first screen covers application capture, editing, search/filtering and paginated listing with saved/applied state; deadline and task workflows are still to come.
