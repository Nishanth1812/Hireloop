# Hireloop Test Use Cases and Release Gates

These are proposed acceptance scenarios, not passing tests. Implement automated coverage at the appropriate unit, integration or end-to-end level as each release is built; use manual checks where an external delivery, restore or accessibility workflow requires them. Synthetic names, dates and message text below are test data. See the [technical phase plan](2026-11-15-hireloop-technical-phases.md) for scope and dependencies.

## Release 0 — Foundation

| ID | Given / when | Expected result | Proof |
|---|---|---|---|
| R0-01 | A developer follows the setup instructions on a clean checkout. | Web, API and PostgreSQL start and the documented health check succeeds. | Setup smoke check |
| R0-02 | CI runs on the repository. | Configured build, lint and test commands finish successfully; a failing check blocks the release. | CI run |

## Release 1 — Manual tracker

| ID | Given / when | Expected result | Proof |
|---|---|---|---|
| R1-01 | A signed-in user saves Acme's Software Intern role without applying. | The role is saved and is not described as submitted. | End-to-end |
| R1-02 | That user marks the same role as applied and records an address used. | The application shows applied and the recorded address; no inbox access is implied. | End-to-end |
| R1-03 | A user creates a task due 12 November with no cutoff time. | Today displays a date-only deadline and explicitly says the time is unspecified. | Integration and UI |
| R1-04 | A user enters a deadline of 12 November 2026, 18:00 Asia/Kolkata. | The stored instant and displayed original zone represent the same time; conversion respects the selected zone. | Unit and integration |
| R1-05 | User B requests or changes user A's application or task by ID. | Access is denied without exposing A's data or changing it. | Authorization integration |
| R1-06 | A user completes a task and reloads the app. | It remains complete and no longer appears as an open action. | End-to-end |
| R1-07 | A user exports their data and requests account deletion. | Export contains their records only; active data is deleted according to documented retention, including outstanding work, and backup limitations are disclosed. | Integration and deletion procedure |
| R1-08 | A user navigates capture and task forms using a keyboard and a screen reader. | Controls are labeled, focus is visible, errors are announced, and the core journey is operable. | Manual accessibility check |

## Release 2 — Reminders and private alpha

| ID | Given / when | Expected result | Proof |
|---|---|---|---|
| R2-01 | A confirmed deadline has a reminder scheduled and the worker runs. | A controlled mailbox receives the reminder; the UI distinguishes dispatch, acceptance and delivery where known. | Integration with test provider |
| R2-02 | A worker stops after leasing a job and its lease expires. | Another worker can recover the job; retries retain the same notification identity. | Database integration |
| R2-03 | A deadline is moved or a task completed before its old reminder is sent. | The old unsent reminder is cancelled or suppressed; any new reminder uses the updated deadline. | Integration, including dispatch race |
| R2-04 | A provider times out after a send request or repeats a callback. | The system records uncertainty and deduplicates callbacks; retry behavior and any duplicate-send window are documented. | Failure-injection integration |
| R2-05 | A backup is restored after an account deletion. | The documented restoration process prevents deleted data from becoming accessible again. | Restore drill |

## Release 3 — Message intake and review

| ID | Given / when | Expected result | Proof |
|---|---|---|---|
| R3-01 | A user pastes “Complete the assessment by 12 November 2026, 18:00 IST” for a known role. | A review proposal displays a candidate task, date, zone and source excerpt; no task or status changes until approval. | End-to-end |
| R3-02 | Two Acme roles exist and a message only says “your application”. | Neither role is silently selected or closed; the user can choose or leave it unmatched. | Integration and UI |
| R3-03 | A user approves an invitation twice or the same forwarded message is replayed. | There is one logical review item and one approved task; completion is not inferred. | Concurrent integration |
| R3-04 | A later message proposes moving an interview from A to B. | Both source times are visible; approval updates the task, preserves history and suppresses the obsolete unsent reminder. | End-to-end |
| R3-05 | A message has no timezone, a date-only deadline, or an unclear relative deadline. | The app preserves precision and asks for clarification when needed; it invents no time or timezone. | Fixture-driven unit and UI |
| R3-06 | An oversized or malformed email arrives, or forged sender headers claim another identity. | Limits are enforced safely; sender headers do not authenticate an identity; no unauthorized account data changes. | Inbound integration |
| R3-07 | A forwarded message contains instructions to reveal secrets or follow a link. | Content remains untrusted; no instruction executes, no arbitrary URL is fetched, and no critical mutation occurs without approval. | Security fixture |

## Release 4 — Pilot and quality

| ID | Given / when | Expected result | Proof |
|---|---|---|---|
| R4-01 | The labeled held-out fixture set is evaluated. | Report true/false positives and false negatives, precision and recall by category, sample counts and known errors. | Reproducible benchmark |
| R4-02 | The pilot release is prepared. | Applicable security, deletion, restore, accessibility and notification checks pass; failures are fixed or affected features disabled. | Release checklist |
| R4-03 | A pilot is reported in a case study. | Counts, recruitment method, timeframe and limitations are stated; unmeasured impact is not claimed. | Documentation review |

## Later releases — Conditional features

| ID | Given / when | Expected result | Proof |
|---|---|---|---|
| RX-01 | A user declines AI processing or the model is unavailable. | Manual and rules-based workflows remain usable; no message is sent to the model without consent. | Integration |
| RX-02 | An AI output has invalid schema, unsupported claims or ambiguous dates. | It cannot directly change records; invalid or uncertain results are rejected or flagged for user review. | Adversarial fixture tests |
| RX-03 | A CSV import includes duplicates, invalid rows or a spreadsheet formula prefix. | Preview shows actionable errors and duplicate choices; commit follows its documented failure behavior; exports protect against formula execution. | Import/export integration |

## Release practice

For each release, record its scope and version or commit, environment, checks and results, known limitations, migration and rollback procedure, and owner/date. Run the current release's relevant cases plus regression cases for previously delivered behavior. A failing security, data isolation, deletion or critical deadline check blocks external release; disable or repair the affected capability and repeat the relevant checks. Do not represent provider acceptance as guaranteed delivery.
