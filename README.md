# Hireloop

Hireloop is a job application and deadline tracker. The project is in its foundation stage.

## API development

Requires Go 1.25 or later. From `apps/api`:

```sh
go run ./cmd/api
```

The API listens on `http://127.0.0.1:8080`. `GET /health` returns `{"status":"ok"}` when the API process is responding. This endpoint does not check the database.

Run checks from `apps/api`:

```sh
go test ./...
go vet ./...
```

## Authentication API

`GET /api/me` accepts a Supabase access token in an `Authorization: Bearer <token>` header. It asks Supabase Auth to validate the token and returns the user's `id` and `email`. Missing or invalid tokens receive 401; missing configuration or an unavailable authentication service receives 503. The endpoint never uses the database session-pooler connection.

To configure the API when your Supabase project is ready, set `SUPABASE_URL` to its HTTPS project URL and `SUPABASE_PUBLISHABLE_KEY` to its publishable key in the server environment. Never put a service-role key, database password or access token in source control. Without configuration, `/health` still works and authenticated requests fail closed.

## Application APIs

All routes require the Supabase bearer token described above. The owner is taken from the verified identity, never from the request body.

- `POST /api/applications` accepts JSON such as `{"company":"Acme","role":"Engineer","status":"saved"}`. Status must be `saved` or `applied`. It returns the created application with status 201; invalid input returns 400.
- `GET /api/applications` returns the current user's newest applications as a JSON array, currently limited to 100; pagination is not implemented yet.
- `GET /api/applications/{id}` returns a specific application by ID with status 200 if it exists and belongs to the authenticated user. Returns 404 if the application does not exist or belongs to another user (no ownership is revealed). Returns 503 for database errors.
- `PATCH /api/applications/{id}` updates a specific application with JSON containing only `company`, `role`, and/or `status` fields. Must have valid values, or returns 400. Returns 200 with the updated application on success. Returns 404 if the application does not exist or belongs to another user (no ownership is revealed). Returns 503 for database errors.
- `DELETE /api/applications/{id}` deletes a specific application. Returns 204 on success. Returns 404 if the application does not exist or belongs to another user (no ownership is revealed). Returns 503 for database errors.

All routes return 503 if database configuration or access is unavailable. Configure `DATABASE_URL` in the server environment with your Supabase session-pooler connection string only when ready. Do not commit it. When `DATABASE_URL` is configured, the API automatically applies pending embedded migrations before it starts accepting requests. Startup stops on a migration error. The migration at `apps/api/db/migrations/001_applications.up.sql` creates the applications table; review it and your target database before starting the API with a connection string. It enables row-level security and grants no direct client policies; the backend database role must have appropriate access, and backend queries enforce the authenticated owner. Review role privileges before deployment.

Migrations use an advisory lock and a migration history table; use the Supabase session pooler, not transaction pooling. If a migration fails, inspect the migration history and database state before repairing it; do not blindly reset the history or drop data. An existing manually created applications table is not automatically adopted. Use a backup and a reviewed corrective migration when needed. API routes previously prefixed `/api/v1/` have moved to `/api/`.

The Google sign-in interface is not implemented yet. Authentication and API tests use local mocks; the migration, database role, session-pooler connection and PostgreSQL queries have not been exercised against Supabase. No database is required to run the tests.
