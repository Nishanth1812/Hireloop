# Hireloop foundation design

**Status:** Proposed first increment for review. The existing [phase plan](../plans/2026-11-15-hireloop-technical-phases.md) and [test cases](../plans/hireloop-test-use-cases.md) cover later releases.

## Goal

Create the smallest runnable monorepo foundation that demonstrates a Next.js frontend talking to a Go backend, configured for a Supabase PostgreSQL session-pooler connection. Work on both sides together in a thin vertical slice. This is infrastructure for the application tracker, not a completed tracker or sign-in experience.

## Choices and boundaries

- One repository contains `apps/web` (Next.js/TypeScript), `apps/api` (Go modular monolith and migrations), and setup documentation. Add directories when needed.
- Supabase provides PostgreSQL and, in the next increment, Google identity sign-in through Supabase Auth. Google sign-in does not request Gmail access. Account ownership must be enforced on the backend for user data; credential verification and authorization receive dedicated tests before user data is exposed.
- The initial frontend shows a simple accessible foundation page and the API's connection status. A Go health endpoint reports database availability without exposing database credentials or detailed errors. The database connection comes from server-only environment configuration.
- The user will set up Supabase and provide its session-pooler connection separately. Do not start or connect to any database without the user’s direction. Use placeholders in example configuration; never commit credentials. A missing database connection is shown honestly, not as a successful health check.
- Provide instructions for local startup, configuration and running checks. Avoid user data, account creation, production deployment and schema changes on a hosted project during the foundation increment.

## Approaches

1. **Use the Go API as the owner of application behavior** and use Supabase for Postgres and Auth. This preserves the agreed modular monolith and keeps validation and authorization at one backend boundary; it requires secure validation of Supabase-issued credentials when authentication is added. Recommended.
2. **Access Supabase tables directly from the frontend.** This can reduce backend code, but moves application authorization and business rules into database policies and the client, conflicting with the agreed Go backend ownership. Not selected.

## Acceptance checks

1. Documented commands start frontend and API; the health request reports API status and the frontend presents it. Database integration is checked only when the user directs and provides access.
2. An unavailable or misconfigured database produces a clear failure state without exposing connection details when database connectivity is checked. Tests cover success and failure without requiring a live database.
3. Setup documents required tools and variables, and no secrets are committed. Checks run locally and in CI where dependencies are available.
4. Review the running increment and results with the user before moving to authentication and application tracking.

**Not included:** Google OAuth implementation, user records, public deployment, email intake, reminders or AI. OAuth setup requires a Supabase project and Google provider configuration; never treat an unconfigured provider as a working login.
