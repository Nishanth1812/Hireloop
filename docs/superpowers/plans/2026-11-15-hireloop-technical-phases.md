# Hireloop Technical Phase Plan

**Goal:** Build a candidate action tracker that captures applications, makes deadlines visible, and turns recruitment messages into user-reviewed suggestions.

**Status:** Proposed roadmap; the repository currently contains a product plan, not an implemented app. This is a phase-level plan, not a file-by-file implementation specification. Refer to [the product and market plan](../../../candidateops_market_audit_and_build_plan.md) for product flows, threat model, quality targets and research limitations.

**Proposed stack:** Next.js and TypeScript for the web app; Go and Chi for the API and workers; Supabase PostgreSQL for persistence and durable jobs; Supabase Auth for Google sign-in; user-managed Supabase session-pooler connection for development; do not start or connect to a database without the user’s direction. Google sign-in requests identity access only, not Gmail access. Forwarded email uses a thin Cloudflare Email Worker; email reminders use a transactional provider. Confirm providers and costs before external deployment.

**Architecture:** One monorepo contains the web app, Go backend, database migrations, deployment configuration and tests. The backend is a modular monolith: authentication, applications, tasks, intake and notifications have clear ownership but run as one application against one PostgreSQL database. A separately launched worker runs the same backend codebase for durable jobs; it is not a separate independently owned service. The external inbound email adapter is a thin transport integration only. Users explicitly approve externally inferred changes; the app never assumes access to an inbox or an employer's internal status.

**Proposed repository layout:** `apps/web/` for Next.js; `apps/api/` for the Go monolith, worker entry point and migrations; `apps/inbound-worker/` only when forwarding is added; `deploy/` for local/deployment configuration; `docs/` for decisions and runbooks; `testdata/` for synthetic fixtures. Create directories when needed. Domain modules own their rules and database operations; avoid separate services and shared packages until an actual need appears.

**Incremental releases:** Each phase closes with a tested, runnable increment and a release record containing scope, verification, known limitations, migration/rollback notes and the next phase. Release labels below are planning labels, not published versions. A release is promoted to external users only after its privacy, security, backup and deletion gates pass. Use feature disabling or a documented rollback for incomplete integrations; preserve user data during schema changes. The [test use-case catalog](hireloop-test-use-cases.md) maps acceptance cases to each release.

**Collaboration:** Build frontend and backend together as thin, integrated increments. Explain changes and verification to the project owner at each step; review each release and agree on the next before proceeding.

**Sequence:** 0 → 1 → 2 → 3 → 4 → 5. Phase 5 is conditional on measured benefit. Security and accessibility checks belong in each phase, not only at the end. No dates below are delivery promises.

## Phase 0 / Release 0 — Foundation and scope

**Outcome:** A reproducible development environment and a small, testable first workflow.

- [ ] Confirm the manual capture → task → reminder journey and distinguish required launch behavior from later features.
- [ ] Set up the web app, Go API, Supabase-compatible PostgreSQL migrations, local development configuration and basic CI checks.
- [ ] Document configuration, setup, architecture and the main privacy and security threats. Use synthetic data for examples.
- [ ] Prepare initial cases for date-only deadlines, same-company roles and access across two accounts.

**Exit:** A clean checkout starts locally using documented steps; a basic request reaches the API and database; CI runs successfully. Record unresolved provider and deployment choices.

## Phase 1 / Release 1 — Manual application tracker

**Outcome:** A user can track applications and actions without connecting an inbox.

- [ ] Implement Google sign-in through Supabase Auth with secure session handling; validate Supabase-issued credentials and enforce ownership on every user-owned backend operation.
- [ ] Add application CRUD with company, role, source, optional address used, and explicit saved/applied status. A saved opportunity does not imply submission.
- [ ] Add tasks with completion, cancellation and deadlines; distinguish date-only values from exact instants and time zones.
- [ ] Build a responsive, keyboard-accessible Today view and application detail view.
- [ ] Provide user data export and account deletion before external users participate; document retention and deletion limits, including backups.

**Verification:** Test account isolation, saved versus applied, exact versus date-only deadlines, task completion, export and deletion. Walk through forms using a keyboard.

**Exit:** A new user can sign in, record an application, create and complete a task, export their data and request deletion; another account cannot access their records.

## Phase 2 / Release 2 — Reminders and private alpha

**Outcome:** The manual tracker can surface deadlines and send reminders with visible delivery state.

- [ ] Add durable PostgreSQL-backed jobs and a worker with leases, retries and stable notification identifiers.
- [ ] Schedule reminders for confirmed deadlines; cancel or suppress stale unsent reminders after task completion, deletion or rescheduling.
- [ ] Add notification preferences, in-app status and per-task calendar export. Track dispatch, provider acceptance and delivery separately when supported.
- [ ] Deploy a private alpha with TLS, secrets management, redacted logs, encrypted backups and a documented restore procedure.

**Verification:** Test worker restart, expired lease, provider failure or timeout, duplicate callbacks, and a reschedule racing with dispatch. Restore a backup and check deletion handling. External delivery may be ambiguous; do not claim exactly-once delivery.

**Exit:** A controlled test mailbox receives a reminder for a real alpha task, status is truthful, and recovery and account-isolation checks pass. Before inviting external users, publish applicable privacy, retention and support information.

## Phase 3 / Release 3 — Message review and forwarding

**Outcome:** Users can turn recruitment messages into evidence-backed tasks without silent changes.

- [ ] Start with pasted message text, minimal retained evidence and a review inbox. Show proposed application, event, task and deadline alongside the source excerpt.
- [ ] Require explicit user approval or correction; allow an unmatched message and multiple plausible applications. Never infer task completion from an invitation.
- [ ] Add forwarding through a revocable high-entropy user alias and an authenticated inbound handoff. Limit message size and rate, ignore attachments, and treat headers and content as untrusted.
- [ ] Deduplicate retries and support reschedules, preserving history while suppressing obsolete unsent reminders. Keep paste/manual entry available when forwarding is blocked.

**Verification:** Exercise two roles at one employer, missing/ambiguous dates, forwarded duplicates, malformed or oversized messages, forged headers, reschedules and repeated approval.

**Exit:** A pasted or forwarded assessment invitation produces one reviewable proposal; the user can select the correct application and approve a task with evidence. Ambiguity causes no silent critical mutation.

## Phase 4 / Release 4 — Quality, pilot and portfolio

**Outcome:** A closed beta is safe enough to learn from, and its results are reproducible.

- [ ] Expand synthetic or consented fixtures toward 200 messages; label event, match, deadline, review requirement and task. Keep a held-out set for evaluation.
- [ ] Report counts and precision and recall for each relevant category, plus known failure cases; separately track dispatch and provider delivery failures.
- [ ] Complete authorization, security, accessibility, backup/restore and deletion checks; document incident response and retention.
- [ ] Run a small consented pilot when available. Record invited, onboarded and active users and the methods behind any time or usage measurements.
- [ ] Prepare a README, architecture diagram, synthetic-data demo, test instructions and an honest case study with measured results.

**Exit:** The applicable closed-beta checklist in the product plan is met or failures are documented and affected capabilities disabled. CSV import is conditional in Phase 5; do not advertise it as available until implemented and verified. Decide whether to iterate, narrow scope or stop based on actual findings; do not infer causality or product–market fit from a small pilot.

## Phase 5 / later releases — Conditional improvements

**Outcome:** Add only enhancements justified by evidence from the preceding phases.

- [ ] **AI extraction:** If rules miss useful cases, compare an opt-in structured model against the same held-out fixtures. Measure precision, recall, latency and cost; validate output and require user approval. Do not send message contents to a third party without clear consent and suitable terms.
- [ ] **CSV import:** Add preview, column mapping and duplicate review if pilot users need it; enforce limits and protect spreadsheet exports from formula injection.
- [ ] **Other integrations:** Consider browser capture or Gmail/Outlook sync only after demand, platform rules, privacy obligations and operational costs are verified.

**Exit:** Each enhancement has a measured benefit, its own tests and a rollback or disable path. The manual tracker remains usable if AI or an integration is unavailable.

## Scope boundaries

No auto-apply, portal scraping, recruiter impersonation, silent status changes or automatic email sending. AI-assisted drafting and subject-line generation are **not specified in the existing product plan**; decide separately whether to include them before treating them as deliverables. The phase plan describes intended work, not completed functionality or verified results.
