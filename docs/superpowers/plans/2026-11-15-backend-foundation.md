# Hireloop Backend Foundation Implementation Plan

**Goal:** Provide a runnable Go API with an honest process health endpoint, without starting or contacting a database.

**Architecture:** A single Go HTTP server owns the API. Supabase database access and authentication are later increments once the user supplies configuration and authorizes connection.

**Tech stack:** Go standard library. **Spec:** [Foundation design](../specs/2026-11-15-hireloop-foundation-design.md).

## Tasks

1. Add `apps/api/go.mod` and a handler test for `GET /health`: status 200, JSON `{"status":"ok"}`; unrelated paths return 404 and unsupported methods return 405. Run it and confirm it fails before implementation.
2. Implement the handler in `apps/api/internal/httpapi/server.go` and the HTTP entry point in `apps/api/cmd/api/main.go`. Bind to `127.0.0.1:8080` for local development and set an HTTP read-header timeout.
3. Document run and test commands in `README.md`. Run formatting, Go tests and vet. Do not start a database or make a database connection.

Health reports only API process availability, not database readiness. No application, auth, or user-data endpoints are included in this increment.
