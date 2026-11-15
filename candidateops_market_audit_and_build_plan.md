# Hireloop — Market Audit, Product Strategy & Build Plan

**Status:** Research-backed implementation blueprint (not validated product–market fit)  
**Research cutoff:** 7 November 2026  
**Product name:** **Hireloop**. Do not launch under **ApplyFlow**: multiple unrelated products already use it. Conduct trademark, domain, app-store, GitHub, and company-register checks before launch.  
**Initial audience:** Indian engineering students and recent graduates balancing campus recruitment with off-campus internships/jobs.  
**Primary objective:** Reduce missed recruitment deadlines and unhandled next steps, even when updates arrive across multiple sources and email addresses.

> **Decision:** Build a **candidate action system** powered by an evidence-backed application timeline. Do **not** build a general job board, auto-apply bot, ATS resume scanner, or all-platform scraper.

---

## 1. Executive decision

### Build / don't build

**Build a lean, user-facing product and run a closed beta.** There is enough observable pain and enough technical feasibility to justify building; there is **not** enough evidence to claim an uncontested niche, willingness to pay, or product–market fit. No market analysis can make that 100% certain without product trials and purchase behavior.

The first commercially testable promise is:

> **“I always know which job application requires my action, by when, and why.”**

The product must answer, in order:

1. **What requires my action today?** Genuine deadline, interview, document, reply, or user-approved follow-up.
2. **What changed?** Recruitment message or manual update, with source and time.
3. **How certain is that update?** Confirmed by user, extracted from a source, or still ambiguous.
4. **Where is each application?** Consolidated campus/off-campus pipeline, *without pretending to know employer-internal state*.
5. **Which email address did I apply with?** Verified identities and sources, without conflating sign-in identity with application identity.

### Confidence assessment

| Dimension | Assessment | Basis | Still unproven |
|---|---|---|---|
| Technical feasibility | High for manual tracker, forwarding intake, notifications | Mature web, database, MIME/email, and worker tooling | Production error rate and mail-delivery edge cases |
| Core problem exists | Medium-high | Independent user discussions and competitive product category | Prevalence/priority within a representative Indian student cohort |
| Competitive uniqueness | Low if described as “AI job tracker” | Numerous existing products | Whether evidence-first action workflows feel meaningfully better |
| MVP usability | Medium | Clear workflows, small scope | Real users' capture effort and repeat use |
| Commercial willingness to pay | Low | No pricing experiment conducted | Conversion and acceptable price |
| Public Gmail integration feasibility | Conditional | Restricted OAuth scopes and compliance obligations | Cost, verification, security assessment, provider approval |

**Product gate:** If the closed beta cannot prove significant time savings and reliable deadline capture, reposition or stop rather than adding more AI features.

---

## 2. Market research: what already exists

### 2.1 Direct and adjacent competitors

The table reflects **vendor documentation/claims**, not hands-on testing or an endorsement. Prices are snapshots as of the research cutoff and may change. “Not established” means the source reviewed did not confirm the capability—not proof of absence.

| Product | Verified capabilities relevant to Hireloop | Pricing/limits observed | Competitive implication |
|---|---|---|---|
| **Simplify** | Job tracker; manual/CSV addition; browser capture; Gmail-matched conversations; approve/decline suggested status changes; AI follow-up drafts on Plus | Free tracker/Gmail integration; Simplify+ **$39.99 / 1 month** | Gmail-driven statuses and follow-ups **are not unique**. Most threatening general competitor. [A1][A2][A3] |
| **Teal** | Free unlimited tracker, Chrome extension, contact tracking, follow-up guidance/checklists, job posting capture | Free unlimited tracking; Teal+ **$13/week, $29/month, $79/quarter** | “Free Kanban + reminders + guidance” already exists. [A4][A5] |
| **Huntr** | Job clipper, pipeline, unlimited autofill, resumes, documents, contacts | Free **100 tracked jobs**; Pro **$40/month** | Capture and organization alone don't differentiate. [A6] |
| **Careerflow** | Limited free tracker, Chrome extension, autofill, documents, networking tracker | Basic free; paid tiers | Another bundled job-search suite; avoid competing across breadth. [A7] |
| **Superset** | Student-side application status and stages for opportunities running on Superset | Student-facing platform; no universal cross-platform claim | Do not displace placement systems; coexist with them and record what users can see/receive. [A8] |
| **Rezome (India)** | Campus and off-campus application stages; job capture, reminders, interview scheduling; linked resumes | Free tier tracks **3 applications**; paid passes | Campus-specific stages/reminders already exist. Hireloop must offer better **action reconciliation**, not just campus labels. [A9][A10] |
| **ApplyOwl** | Publicly describes Gmail and Outlook reply tracking, status classification, and student plans | Depends on plan; verify before making pricing claim | Cross-inbox automation isn't an exclusive moat. [A11] |
| **Mailflux** | Private-beta forwarding/alias workflow; automatically tracks correspondence addressed to supplied alias | Invite-only page; public price not established | Forwarding-based intake is also an established concept. [A12] |
| **SoundCV** | Tracks via manually forwarded recruitment emails with extracted events | Advertises free tracker | Forward-to-tracker alone is not a moat. [A13] |
| **Google Sheets / Notion / Calendar** | Flexible and already familiar; user-maintained status and deadlines | Often effectively free to user | Must beat their speed-to-value or users will not change habits. |

**Hard conclusion:** No individually listed feature is defensible. The potential differentiation is *execution*: reliable cross-source task reconciliation, explicit confidence/evidence, campus restrictions and deadlines, fast correction, and almost no daily maintenance. Test whether those combined outcomes matter enough to switch.

### 2.2 Evidence that the problem is real — and its limitations

- People publicly describe losing track of applications, recruitment email, follow-ups and dates; spreadsheets can become costly to maintain. These are **qualitative anecdotes**, not representative survey results. [R1][R2][R3]
- A discussion in an Indian developer community explicitly mentions maintaining off-campus applications alongside campus opportunities, with repetitive entry as a pain point. [R4]
- India had **4.50 crore higher-education enrolments** and **1.02 crore STEM enrolments** in the AISHE 2023–24 results published in July 2026. These figures show a broad pool, **not** the number currently seeking internships, using several email accounts, or willing to pay. [M1]

**Market-size discipline:** Do **not** transform enrolment into users, conversion, or revenue using unvalidated percentages. A defensible serviceable market estimate requires active candidate counts, eligible campus cohorts, adoption and payment data from the pilot. Record these after interviews; do not fake a TAM slide.

### 2.3 Segmentation and buying behavior

| Segment | Frequency of pain | Product fit | Ability/willingness to pay | Priority |
|---|---|---|---|---|
| Indian engineering students doing campus + off-campus | High during a concentrated season; multiple notification channels | Highest | Likely price-sensitive; validate | **Primary** |
| Other fresh graduates with high application volume | High while searching | High | Validate after student beta | Secondary |
| Experienced job switchers | Variable; fewer campus-specific workflows | Medium | Potentially higher, but competitive | Later |
| Placement cells | Different buyer, privacy/permissions, institution workflows | Unclear | Requires B2B sales and trust | **Not MVP** |
| Staffing companies/recruiters | Different workflow entirely | Low | Irrelevant to first version | Excluded |

### 2.4 Why a user would switch

A credible switch pitch is **not** “we track your jobs.” It is:

- A single **Today** feed ordered by actionable deadlines, requiring no Kanban review.
- An **audit trail** that says “Assessment due Wednesday, extracted from the email you forwarded at 16:12” rather than a mysterious status label.
- Separate tracking of the **email used on the application** versus the login account.
- Deadline reminders that account for time zone, unknown cutoff time, reschedules, and conflicting messages.
- Support for campus processes where an email is missing, a portal blocks automation, or a placement cell sends a common announcement.
- Low-friction correction of uncertain company/role associations, especially when multiple applications exist at one company.

**Risk:** Teal, Rezome, Simplify and emerging products can copy most of this. Speed of learning, measured accuracy, and user trust are the practical advantages to build—not patent-like feature exclusivity.

---

## 3. Deep audit of the original ApplyFlow proposal

| Original assumption / feature | Verdict | Why it needs change | Corrected decision |
|---|---|---|---|
| A Kanban board is the core product | **Reject as centerpiece** | Commodity, often free | **Today/action queue** is home screen; pipeline is secondary |
| Automatically sync every email address from day one | **Defer for public release** | Gmail scopes are restricted; school tenants can block permissions; multi-provider auth is expensive to operate safely | Start with verified email identities + forwarding intake; gated OAuth later |
| AI automatically updates status from email | **Modify** | Same company/multiple positions, ambiguous wording, and false matches can corrupt records | Store evidence-backed **proposals**; user approves meaningful state changes |
| Browser extension reads LinkedIn/other sites universally | **Reject** | LinkedIn explicitly restricts scraping/modifying/automation via extensions; sites differ | Paste/link/manual capture; optional user-invoked support only on approved sites |
| Campus portal integration via scraping | **Reject MVP** | Proprietary systems, changing layouts, account/ToS risk | Manually confirmed campus milestones and college emails/CSV imports |
| “Application status” and “next action” are the same | **Reject** | Applied can coexist with an OA task, document request, interview, or follow-up | Store **state, events, tasks and deadlines separately** |
| AI needed for all email classification | **Reject** | Cost, privacy and hallucination risk | Rules first; structured AI fallback only when rules fail and allowed |
| “No response” means rejected/ghosted | **Reject** | Silence is not a verified hiring decision | Show “Last heard X days ago,” optionally suggest one user-controlled follow-up |
| Immediate calendar two-way sync | **Defer** | OAuth scopes, duplicate/conflicting event writes | Start with reliable in-app reminders + `.ics` export; optional Calendar later |
| Entire public SaaS in a few weeks | **Reject** | Policy review, security, testing and market validation take time | Closed beta first; public launch gated on trust/compliance |
| ApplyFlow is a safe product name | **Reject** | Multiple products already use it, including `applyflow.in`, `applyflow.store`, and `applyflowtracker.com` | Use **Hireloop**; run brand clearance before launch. [N1][N2][N3] |

### Non-negotiable product principles

1. **No invented employer status.** Distinguish candidate-side “submitted” from employer-side “under review.”
2. **No silent critical automation.** Every status transition and newly discovered hard deadline has a review/verification path.
3. **Evidence beats confidence scores.** Show excerpt/source, timestamp, and “why matched.” Confidence is advisory only.
4. **User controls notification volume and retention.** Unsubscribe, snooze, disconnect, export, delete.
5. **No portal credentials, unattended scraping, auto-submission, or recruiter impersonation.**
6. **Fast without email connection.** The user can always manually save an opportunity and set a deadline.

---

## 4. Final product definition

### 4.1 Core jobs-to-be-done

- **Capture:** “I submitted or saved a role—record it with as little friction as possible.”
- **Reconcile:** “I got a message or portal update—attach it to the right application, not another role at the same company.”
- **Act:** “Show what must be completed, replied to, or prepared before the deadline.”
- **Verify:** “Tell me which claims are confirmed and where the evidence came from.”
- **Review:** “Help me see which applications need follow-up or closure, without speculating.”

### 4.2 Primary screens

**A. Today — DEFAULT LANDING**

- Section 1: **Must do** (hard deadline, interview, candidate-required response).
- Section 2: **Review needed** (ambiguous match, date, stage update or conflict).
- Section 3: **Upcoming** (next 7 days, grouped by day).
- Section 4: **Waiting on employer** (informational, never described as rejected).
- Each card: company, role, account/address used, why it exists, source, due time/unknown-time label, primary action.

**B. Applications** — list first, optional Kanban later; filters for campus/off-campus, company, role, email identity, last activity, deadlines, closed.

**C. Application detail** — posting snapshot, applied-via/source, email used, user-confirmed submission time, overall state, pipeline stage, event timeline, active tasks, attachments/links, notes, prior messages/evidence.

**D. Intake / Review inbox** — forward/message imports waiting for association or structured-action approval.

**E. Calendar** — factual due dates, interview appointments, date-only deadlines distinctly styled; `.ics` export.

**F. Settings** — verified application email identities, forwarding address, notification preferences, export, deletion, integrations status.

### 4.3 Distinguish opportunity, application, stage and action

- **Opportunity:** Role saved but not necessarily applied to.
- **Application:** Confirmed or user-declared submission for a role/requisition, with submission source and email identity.
- **Stage:** Latest known candidate-facing hiring step, possibly “unknown”; may have many interview rounds.
- **Event:** Immutable historical fact or reported message, e.g. assessment invitation.
- **Task:** Something the user should do (submit test, reply, prepare document).
- **Deadline:** User-provided or source-backed time/date associated with a task, interview, or application.

**Important:** `assessment_due` is a deadline/task and does not automatically prove that an assessment was completed or that the candidate progressed.

---

## 5. MVP feature contract

### P0 — Manual-first feature inventory

This is the full manual feature inventory, not the minimum private-alpha gate. Section 22.2 stages CSV import and secondary-identity verification after the initial alpha.

1. Google OAuth for **sign-in only** using ordinary identity scopes; **no Gmail read scope** required for authentication.
2. Application CRUD: company, role, URL, source, campus/off-campus, job ID, date applied, email identity used, user-supplied notes.
3. Quick capture: pasted job URL plus manual fields; paste job text; user confirms `saved` versus `applied`.
4. CSV import from existing tracker, with preview, field mapping, duplicate review, and no silent overwrite.
5. Timeline, task CRUD, hard vs suggested deadlines, due date/time/time zone or “cutoff unspecified.”
6. Today queue, due-soon reminders, snooze/complete, notification preferences.
7. Verified additional email **identities**: OTP/verification, recorded as “address used,” **not** implied inbox access.
8. `.ics` export for a confirmed interview/deadline; email notification delivery and in-app status.
9. Export own data to CSV/JSON; delete account and all associated data; audit and corrections.
10. Basic product metrics that do not reveal recruitment-email contents.

### P1 — Forwarding-based closed beta (after P0)

1. Assign each user a random, unguessable personal intake address on a domain you control, e.g. `<opaque-token>@in.hireloop.example` (illustrative, not an actual domain).
2. User forwards a recruitment message from any email account. Optional email-provider forwarding rules where the provider and institution allow them.
3. Inbound email handler parses relevant text, strips risky HTML, enforces size limits, stores minimal event evidence and avoids attachments initially.
4. Rules first: look for confirmation, assessment, interview, action request, deadline, rejection/closure, offer; AI only as an opt-in fallback.
5. Match to an application using role/requisition/job-ID, employer domain, thread hints and account identity; flag ambiguity.
6. Suggest event, deadline and status changes in review inbox; user approves or edits before critical mutation.
7. One-click “create new application from message” when nothing matches, with submission status **unconfirmed** until verified.
8. Detect revised/cancelled interview times and supersede the old reminder after user approval.

**Important limitation:** Forwarding **does not automatically see all mail**. The promise should be “multi-account-compatible inbox intake,” not “fully automatic multi-inbox sync,” until OAuth provider integrations ship. Many college/work accounts block external forwarding; manual import remains essential.

### P2 — After beta signals justify it

- Chrome MV3 extension with user click to save URL/text from *permitted* sites; `activeTab` and minimal permissions; no bulk browsing-history monitoring.
- Gmail API OAuth connection for eligible users **only after** legal/compliance/security work, including restricted-scope verification and relevant assessments.
- Microsoft Graph delegated `Mail.Read` for Outlook after tenant/privacy validation; institution admin approval may be necessary.
- Better structured parsers for placement notices and notification documents, only with permission and sample coverage.
- Calendar write integration with explicit per-event confirmation and dedupe.
- Optional quick-share/mobile PWA.

### Explicitly out of scope for v1

Auto-applying; scraping student portals; scraping LinkedIn or auto-injecting LinkedIn UI; mass resume tailoring; ATS score estimates; job discovery engine; auto-emailing recruiters; employer status API claims; “ghosted = rejected”; shared-placement data for other students; university admin dashboards; paid subscription platform.

---

## 6. Detailed functional flows

### Flow A — Saving an off-campus application

1. User pastes a company job URL or copies the role details.
2. App offers an editable company/role and role/requisition ID, if available; never invents missing details.
3. User chooses `Saved` or **I applied**.
4. If applied, user chooses source, submission date, and which of their verified emails they used.
5. App searches likely duplicate applications and lets user merge/link/keep separate.
6. Application detail is created; if no deadline exists, no bogus countdown is shown.

**Done when:** Typical user can track an application in **30 seconds or less** using the fast path (design target, to measure in pilot).

### Flow B — Campus placement opportunity with no confirmation mail

1. User logs a drive as `campus` and marks origin as a named placement notice/portal.
2. User records a **registration deadline**, not an assumed application submission.
3. After actually registering, user explicitly marks `registered/applied` and records an optional confirmation/reference.
4. Later shortlist, OA, GD, interview and documents appear as separate events/tasks.
5. If portal has no accessible update, the app says **“No newer information recorded”**, not **“Under review.”**

### Flow C — Assessment invitation forwarded from a second email address

1. Message is forwarded from any address (or its text is pasted if forwarding is blocked); the application identity is confirmed separately.
2. Intake validates inbound routing and associates the user using recipient token; **sender headers are not trusted as proof of user identity**.
3. MIME parser extracts subject, text, source sender, visible date, message ID and candidate deadline language.
4. Matcher finds potential applications; if one company has two roles, user chooses.
5. The review card shows: “Possible OA invite; deadline Nov 12, 18:00 IST; source excerpt; confidence: high.”
6. User accepts; a deadline event and `Complete assessment` task are committed atomically.
7. Reminder scheduler creates durable notifications and deduplicates them.
8. User marks task completed. **Completion must never be inferred from the invitation.**

### Flow D — Interview rescheduled after initial reminder

1. New evidence points to the same interview round.
2. Proposal says “Move interview from A to B”; both dates and sources visible.
3. On approval, mark the prior event superseded, update the task and cancel unsent old reminders.
4. Keep historical event trail so the user can audit what changed.

### Flow E — Rejection email for one of two positions

1. Candidate has two applications to the same company.
2. Email only says “your application” with no position identifier.
3. System **must not** close either application; it creates an unmatched or ambiguous message requiring user intervention.
4. User selects correct role and approves `closed/rejected`.

---

## 7. Data and domain model

### 7.1 Recommended PostgreSQL tables

| Table | Essential fields / notes |
|---|---|
| `users` | id, login provider subject, preferences, created_at |
| `email_identities` | user_id, normalized address, verified_at, provider label; **not** necessarily connected inbox |
| `inbox_connections` | user_id, provider, encrypted refresh token reference, scopes, sync cursor, health, last_success_at; **P2** |
| `intake_aliases` | user_id, hashed recipient token, state, rotated_at |
| `companies` | user-scoped normalized name, domains optional; no false automatic global merging |
| `opportunities` | user_id, company_id, title, URL, requisition ID, source type, posting snapshot, created_at |
| `applications` | user_id, opportunity_id, email_identity_id, applied_at, submission_confirmed_by, application_state, current_stage, visibility |
| `recruitment_events` | application_id nullable; event type, occurred_at, evidence_id, provenance, reviewed_at, supersedes_event_id; append-only history |
| `tasks` | application_id, event_id nullable, type, title, status, due_at nullable, due_date nullable, time_zone, due_precision, priority_reason |
| `evidence` | user_id, source_type, original sender/subject/received metadata, safe excerpt, source_link nullable, content hash, retention_until |
| `review_proposals` | user_id, evidence_id, proposed match, JSON suggestions, confidence/reason, status, actioned_at |
| `notifications` | task_id, scheduled_for, channel, idempotency_key, delivery state, attempts |
| `jobs` | type, payload, run_at, lease_until, attempt count, outcome/error class; PostgreSQL workers |
| `audit_log` | user_id, actor, target entity, before/after *redacted*, reason, time |

Use UUIDs, database constraints, indexes on `user_id`, task due dates, and uniqueness where semantically justified. Every user-owned query must enforce user ownership; don't rely solely on frontend IDs.

### 7.2 Domain states: don't model a fantasy ATS

**Top-level application state:**

- `SAVED` — not submitted.
- `APPLIED` — candidate says submitted, or submitted email evidence reviewed.
- `IN_PROCESS` — a verified candidate-facing hiring step took place.
- `OFFER` — offer communicated (not the same as accepted).
- `CLOSED` — candidate rejected, withdrew, declined, accepted, or manually archived; store `closure_reason`.

**Specific steps** belong in events/stages: `registered`, `shortlisted`, `assessment_invited`, `assessment_completed`, `screening`, `technical_interview_n`, `hr_interview`, `document_request`, `offer_received`, `offer_accepted`, `rejected`, `withdrawn`. Do not force a fixed linear progression.

**Task status:** `OPEN`, `SNOOZED`, `DONE`, `CANCELLED`.

**Evidence state:** `USER_CONFIRMED`, `SOURCE_VERIFIED_BY_USER`, `EXTRACTED_UNREVIEWED`, `AMBIGUOUS`, `DISMISSED`.

### 7.3 Deduplication

- Distinct applications may have the same company and title; **never** unique-constrain on just those fields.
- Prefer stable requisition IDs and source URLs, but normalize tracking parameters and ATS redirects cautiously.
- Merge duplicates only with user approval; preserve both original source references and event history.
- For messages, use provider message ID when available; otherwise a scoped digest of normalized headers/content + mailbox identity; also handle identical forwarded copies with different transport IDs.
- Build an idempotent `ingest -> event -> proposal -> task -> notification` pipeline with database transactions/outbox pattern.

---

## 8. Deadline and reminder correctness

This is the product's **highest-risk feature**, and quality matters more than AI flair.

### Date semantics

- Store a specific instant as UTC **plus** original IANA timezone and source rendering.
- If the message says `11:59 PM IST`, store that exact instant and display it in user's selected timezone with original timezone visible.
- If the message only says `Apply by Nov 12`, store **date-only**, not an invented `23:59` cutoff. Display **“Nov 12 — cutoff time unspecified.”**
- If it says `within 48 hours`, derive the anchor only if the relevant starting timestamp is explicit and reliable; otherwise ask for confirmation.
- Detect `tomorrow`, weekdays, and AM/PM ambiguity carefully; do not use server-local time for interpretation.
- Detect reschedules, cancellations, duplicate invites, expired opportunities, and delayed imported emails.
- “Application closes” and “assessment must be submitted” are different task types.

### Reminder policy (user editable)

- Hard deadlines: suggest **24 h and 2 h beforehand**, with an earlier warning if a submission needs preparation; never schedule past-deadline notifications.
- Date-only deadlines: morning-of and day-before, labeled approximate and **not** a guaranteed cutoff.
- Interviews: default **24 h and 1 h before**; acknowledge location/timezone and meeting link if known.
- Suggested follow-up: opt-in once, after the employer's communicated window, never spam recurring reminders by default.
- On failure: expose `last_attempt`, delivery channel, and “notification not sent” state; no false success claim.

### Priority order

1. Due/overdue **hard tasks** requiring candidate action.
2. Scheduled interviews/assessments.
3. New messages requesting a response.
4. Unreviewed **high-impact** suggestions (unknown deadline, reschedule, ambiguous match).
5. User-created follow-up reminders.
6. Passive waiting/stale applications.

Priority is **deterministic** with an explanation, not a hidden “AI urgency score.”

---

## 9. Email ingestion — compliance-first architecture

### 9.1 Why not public Gmail sync in v1

Google categorizes `gmail.readonly` **and** `gmail.metadata` as **restricted** OAuth scopes. Public applications require appropriate verification. If restricted Gmail data is stored or transmitted on servers, Google states that a security assessment is required. A read-only label is **not** an exemption. [T1][T2]

**Implication:** Google Sign-In (`openid`, `email`, `profile`) is easy relative to **Gmail mailbox reading**, which is a separate permission and launch plan. Do not disguise read access as sign-in, use password collection/IMAP as a workaround, or assume a third-party LLM can freely receive full email bodies.

### 9.2 Recommended v1 intake: user forwarding

- Use **Cloudflare Email Routing + a tiny Email Worker** to receive mail for a dedicated intake subdomain. Cloudflare documents this capability and its `email()` handler. [T3]
- The worker enforces recipient token validation, size limits, flood controls and basic MIME safety; it calls the Go backend over an authenticated endpoint or queues a signed short-lived message.
- The main backend handles application matching and review proposal creation; **the worker must stay thin**.
- Link forwarded mail to the chosen *application email identity* by user confirmation if the forwarding envelope does not reliably prove origin.
- **Never** trust `From`, `X-Forwarded-*`, or inner forwarded headers as proof that the sender owns an account; inbound forwarding rules can also duplicate/alter metadata.
- HTML and remote images are untrusted; parse plain text, sanitize, never load remote URLs/pixels or execute embedded content.
- Ignore attachments in v1; if later supported, scan, size-limit, type-check and require opt-in.
- Store minimum necessary evidence snippets for UI and audit; set a short retention period for transient raw email if absolutely needed (proposed **7 days**). Let users delete earlier.
- Cloudflare routing is still third-party processing: disclose it in the privacy notice and assess retention/security, **not** a magically private alternative.
- If forwarding is prohibited by school or employer, allow **paste text/manual entry** instead.

### 9.3 Future full inbox sync (separate release gate)

If testing shows users strongly need automatic multi-mailbox reading:

**Gmail:**

1. Verify scope justification and limited-use terms; arrange OAuth verification/security assessment as applicable **before** general public launch.
2. Start with an explicit date window; avoid importing entire inbox history.
3. Do initial targeted search and store Gmail `historyId` cursor; subsequent `history.list` sync.
4. Re-sync if `historyId` expires; Google says a stale history ID may return **404**. [T4]
5. Push notifications use Cloud Pub/Sub and a Gmail `watch` that must be renewed **at least every seven days**, recommended daily; use scheduled sync as fallback. [T5]
6. Encrypt tokens with proper secret management, rotate/revoke on disconnect, show last sync, and fail safe when permissions lapse.

**Outlook:**

- Use Microsoft Graph delegated `Mail.Read` for eligible account contexts; confirm consumer/work-school permission support and tenant administrator restrictions before enabling.
- Graph supports Outlook change subscriptions with expiry/renewal. A mailbox may need admin consent or a tenant may block third-party apps. [T6][T7]

**Google Workspace Add-on** could be explored separately for user-initiated single-message capture; it does not substitute for unrestricted background mailbox access.

---

## 10. Browser extension and platform constraints

- LinkedIn says third-party extensions that **scrape, modify its appearance, or automate activity** are not permitted. Do not build LinkedIn DOM scraping, overlays, autopopulate, or auto-apply as a dependency. [T8]
- A user can manually copy a job URL or text into Hireloop; that's the dependable common denominator.
- For sites allowing extension-assisted capture, use **Manifest V3**, explicit click, minimal permissions (prefer `activeTab`), preview extracted fields, and user confirmation. [T9]
- Never capture passwords, authentication tokens, form answers, or unrelated browsing history.
- Avoid broad `<all_urls>` host access and “monitor all tabs” functionality.
- Read Chrome Web Store limited-use and privacy rules before submission, including privacy disclosures and prohibition on unrelated data use. [T10]
- A captured page **does not prove submitted application**; user confirms actual submission or attaches a confirmation.

---

## 11. Technical architecture: lean modular monolith

### Stack

| Layer | Decision | Reason |
|---|---|---|
| Web | Next.js + TypeScript | Responsive UI, familiar stack, easy forms and dashboard |
| API | Go + Chi | Focused modules, straightforward worker processes and reliability |
| Database | PostgreSQL | Durable transactions, relational links, queues/reminders in one DB |
| SQL access | `sqlc` + `pgx` or straightforward repository SQL | Type safety without unnecessary ORM abstraction |
| Migrations | `goose` or equivalent | Reproducible schema changes |
| Email intake | Cloudflare Email Worker (small TypeScript adapter) | Vendor-supported inbound routing; one small adapter, not a service mesh |
| Background work | Go worker + `FOR UPDATE SKIP LOCKED` and leases | No Redis/RabbitMQ in MVP |
| Auth | Google Sign-In only initially | Simple access; address verification separate |
| Email notifications | Transactional mail provider with delivery callbacks | Critical reminders need delivery/error status |
| Calendar | `.ics` first | No calendar write OAuth to begin with |
| AI | Structured extraction only when needed; optional toggle | Keep core system deterministic and useful without AI |
| Deploy | One small VPS, Docker Compose, HTTPS reverse proxy; managed email edge | Low infrastructure complexity |
| CI/CD | GitHub Actions: lint, test, migrations, image build, deploy | Shows full production workflow |

### System flow

```text
Pasted job / manual update / CSV
                |                    Forwarded email
                |                         |
                |              Cloudflare Email Worker
                |                         |
                +----------> Go API / ingestion ----------+
                                      |                    |
                                 PostgreSQL          Review proposals
                                      |                    |
                           Tasks + event history <--- user approval
                                      |
                            Durable reminder jobs
                                      |
                          Email + in-app notifications
                                      |
                                Next.js Today UI
```

### Go modules

- `auth`: sessions, Google sign-in, current user, verified identity.
- `applications`: opportunities, applications, dedup/merge, source identity.
- `timeline`: recruitment events and source evidence.
- `intake`: signed inbound, MIME metadata, parsing, matching, proposals.
- `tasks`: deadlines, priority, snooze/complete, reminders.
- `notifications`: templated delivery, retries, idempotency, preferences.
- `exports`: CSV/JSON and `.ics`, delete requests.
- `analytics`: privacy-preserving aggregate events only.

### Repository sketch

```text
hireloop/
├── apps/
│   ├── web/                         # Next.js App Router
│   ├── api/                         # Go chi modular monolith
│   │   ├── cmd/api/
│   │   ├── cmd/worker/
│   │   ├── internal/{auth,applications,timeline,intake,tasks,notifications,exports}/
│   │   ├── internal/platform/{db,config,logging,security}/
│   │   └── db/{migrations,queries}/
│   └── inbound-worker/              # Thin Cloudflare Email Worker
├── packages/
│   └── contracts/                   # Shared OpenAPI/schema-generated TS types
├── deploy/
│   ├── compose.yaml
│   └── reverse-proxy/
├── docs/
│   ├── architecture.md
│   ├── threat-model.md
│   ├── api.md
│   └── runbooks/
├── testdata/
│   └── synthetic-email-fixtures/
└── .github/workflows/
```

**No:** Kubernetes, microservices, search cluster, vector database, LangChain agent graph, Redis, queues, or browser farm until benchmarks demonstrate a need.

---

## 12. HTTP API sketch

Use `/api`; every path is user-scoped via the authenticated session, not by trusting a body-supplied `user_id`.

```text
GET     /me
GET     /applications?source=&state=&q=&due_before=
POST    /opportunities
POST    /applications
GET     /applications/{id}
PATCH   /applications/{id}
POST    /applications/{id}/events
POST    /applications/{id}/merge-preview
POST    /applications/{id}/merge
GET     /tasks?filter=today|upcoming|overdue
POST    /tasks
PATCH   /tasks/{id}
GET     /review-proposals
POST    /review-proposals/{id}/approve
POST    /review-proposals/{id}/dismiss
POST    /review-proposals/{id}/reassign
GET     /email-identities
POST    /email-identities/verification
POST    /email-identities/{id}/verify
POST    /import/csv/preview
POST    /import/csv/commit
GET     /exports/applications.csv
GET     /exports/data.json
GET     /tasks/{id}/calendar.ics
POST    /account/delete
POST    /internal/intake/email          # Signed machine-only endpoint
```

Use idempotency keys on intake/approval/import mutations; input validation, pagination, rate limits, and consistent problem responses.

---

## 13. Matching and AI extraction design

### Deterministic first

1. Filter obvious non-recruitment spam/marketing by known patterns and opt-in user labels.
2. Extract structured identifiers and dates with rule-based logic where possible.
3. Normalize company names and find exact requisition IDs or URLs.
4. Score candidate application matches using auditable signals: exact ID, title match, destination address, company domain, temporal proximity, thread metadata.
5. If multiple matches are plausible, **never automatically select**; show alternatives.
6. Only after deterministic parsing is insufficient, ask an LLM for schema-constrained **candidate extraction**.
7. Validate enum values, timestamps, quoted evidence spans, timezone and that the extracted event is entailed by the message.
8. Queue proposals; no externally inferred employer state is committed before user approval.

### Example extraction contract

```json
{
  "event_type": "assessment_invited",
  "company": "Example Corp",
  "role": "Software Engineer Intern",
  "requisition_id": null,
  "deadline": {
    "date": "2026-11-12",
    "time": "18:00",
    "timezone": "Asia/Kolkata",
    "precision": "exact"
  },
  "candidate_action": "Complete online assessment",
  "source_quote": "Please complete the assessment by 6 PM IST on 12 Nov.",
  "needs_review": true
}
```

**Threat model:** A malicious email could tell the LLM to change account state, reveal secrets, or access a URL. Treat *all email and job text* as untrusted data; give the extraction model no tool credentials/actions, require structured output, validate it server-side, and do not fetch arbitrary linked URLs automatically.

---

## 14. Security, privacy and compliance requirements

### Must-haves before any external beta

- Per-user authorization tested on all reads/writes and on object IDs (IDOR prevention).
- HTTP-only secure cookies, CSRF protection where applicable, short-lived sessions, session revocation.
- Verify Google OAuth state/nonce/issuer/audience; keep OAuth scopes separated by function.
- TLS; encrypted backups; sensitive tokens encrypted at rest; secrets never written to logs.
- Signed inbound worker-to-backend requests with timestamp/replay protection.
- Injection-safe SQL and sanitized Markdown/HTML render; safe link-handling and SSRF prevention.
- Virus/attachment processing **disabled** initially; MIME size and nesting limits.
- Rate limits on login, import, forwarded-email aliases and notification sends.
- User-facing privacy notice, service terms, data export, account deletion and disconnect controls.
- Raw forwarded email is temporarily processed only for the disclosed purpose; no third-party AI processing without clear opt-in and appropriate provider terms.
- Production logs exclude message bodies, email contents, access tokens and candidate notes.
- Daily encrypted DB backups; routinely test restoration.
- If targeting Indian users, track applicable DPDP Act/Rules implementation and obtain legal review before public launch; rules were notified in November 2025 with phased commencement. [T11]

### Privacy choices that help adoption

- “Use without connecting your inbox” as a first-class option.
- “Only forwarded messages are processed” explicitly displayed in forwarding mode.
- Source excerpts visible to user; no sale of application or recruitment data.
- Per-user retention, delete/eject controls, and clear limits of automated recognition.
- No bulk mail scanning unless the user later explicitly authorizes a compliant integration.

---

## 15. Quality gates and test plan

These are **engineering acceptance targets**, not observed performance claims.

### Test corpus

Build a consented or synthetic, privacy-safe fixture set of at least **200 messages / notices** across:

- submission confirmations, OA invites, timed coding tests, interview invites/reschedules/cancellations, missing links, document requests, rejection, offers;
- college placement broadcasts and individual candidate updates;
- two job applications at the same employer, one position/title reused across multiple dates;
- date only, time only, relative time, “within N hours”, timezone mismatch, changed dates;
- forwarded chains, auto replies, promos, multilingual text, HTML-only messages, attachments present but ignored;
- malicious instructions/prompt injection, links to untrusted hosts;
- duplicates via multiple forwarders and retries.

Label **application match**, **event type**, **deadline exactness**, **needs review**, **resulting task**. Maintain a gold standard with manually checked expected values.

### Proposed release thresholds

| Test | Target | Fail action |
|---|---|---|
| Accepted exact deadlines are correct | **≥ 98% precision** on gold set; all uncertain cases forced to review | Improve date parsing or disable auto-suggestion for that category |
| Wrong application auto-association | **0 high-impact unreviewed assignments** | Require explicit match approval |
| Critical state transition without user approval | **0** | Block release |
| Reminder DB-to-sender dispatch under normal test conditions | **≥ 99%** | Fix retries/worker leases; report email provider failures separately |
| Duplicate alert on retry | **0** | Correct idempotency |
| Quick manual capture | **Median ≤ 30 sec** across pilot tasks | Reduce fields and navigation |
| Correct event handling on inbound replay | **100% deterministic result** | Fix transaction/outbox logic |
| Account isolation / export / delete | **All authorization tests pass** | Block release |
| Notification user control (disable, snooze, delete) | **All end-to-end tests pass** | Block release |

**Reminder scope note:** An application can verify *dispatch*, not guaranteed delivery/open/reading. Surface bounce/provider status and do not market “never miss” as an absolute SLA.

### Testing layers

- Go unit tests: state transitions, matching, timezone conversion, due precision, idempotency.
- Go integration tests: PostgreSQL transactions, lease expiry, concurrent workers, unique constraints, outbox, account scoping.
- Web E2E: manual capture, CSV import, proposal approval, reschedule, mobile viewport, account deletion.
- Email Worker tests: MIME edge cases, forged headers, retry, oversized attachments, webhook signature/replay.
- Security tests: IDOR, XSS, CSRF, SQL injection, SSRF, prompt-injection fixtures.
- Operational tests: scheduler disabled 1 h then resumed; DB restore; email provider temporary outage; dead-letter/replay workflow.

---

## 16. Sequential 12-week implementation plan

**Timeline is a planning assumption for one experienced developer; do not interpret as a delivery guarantee.** Keep a working vertical slice at the end of each milestone.

### Week 0–1 — Discovery and product proof

- Interview **12–15** active candidates; include students using campus portals and at least two email identities.
- Ask for a real recent application journey (with identifying data hidden) instead of only preference polling.
- Compare their current spreadsheet/Teal/Simplify method against a clickable **Today** prototype.
- Capture top 20 actual task/deadline patterns and import formats.
- Decide a primary task-flow based on observed time loss; freeze non-goals.

**Exit:** ≥ 8 of 12 users identify the same recurring, meaningful problem *and* ≥ 5 agree to test with real applications. These are proposed thresholds, not market results.

### Weeks 2–3 — Auth, schema and manual tracker

- Monorepo, CI, database migrations, Google sign-in, secure sessions.
- Applications, opportunities, email identity model; basic CRUD; import preview; unit tests.
- Responsive list/detail screens; source and status semantics; saved-versus-applied confirmation.

**Exit:** User can enter/edit/import applications, distinguish email identities, and export records; no cross-user access.

### Weeks 4–5 — Today queue, events and reliable reminders

- Event timeline; task model; exact/date-only deadlines; timezone handling.
- Reminder scheduler with leases, retry, idempotency, notification preferences and in-app status.
- `.ics` export, task completion/snooze, passive waiting list.
- Deploy private alpha; restore backup tested.

**Exit:** A fully useful **non-AI tracker** for a real application set, with dependable manual reminders.

### Weeks 6–7 — Forwarded email intake and review

- Per-user rotating intake alias; Cloudflare inbound worker; authenticated handoff.
- MIME parser, relevance rule classifiers, evidence retention, unmatched inbox.
- User approval workflow, deterministic matching, duplicate/replay tests.

**Exit:** A forwarded OA or interview message can be reconciled to a correct application and generate a confirmed task with evidence.

### Week 8 — Date correctness and optional LLM fallback

- Synthetic/consented fixture corpus; parsing of time zones, conflicting deadlines, reschedules.
- Optional structured LLM fallback, bounded cost, prompt-injection defenses, confidence explanations.
- Benchmark and improve ambiguous role matching.

**Exit:** Quality gates pass; any uncertain critical date is explicitly flagged for review.

### Weeks 9–10 — Closed beta and iteration

- Recruit 15–25 pilot users; onboard from their existing spreadsheet and forwarding workflow.
- Measure time-to-first-value, logging friction, true/false updates, actions caught, reminder effectiveness.
- Fix top onboarding failures; **do not** add autofill or job discovery.

**Exit:** Meets validation metrics below or decision to pivot scope.

### Weeks 11–12 — Hardening and release decision

- Threat-model audit, rate limits, consent/retention review, deliverability alerts, backup restore.
- Instrument service health, error budget and support loop.
- If user-initiated capture is highly requested, prototype MV3 extension on explicitly permitted sites only; otherwise improve core app.
- Publish a transparent beta limitation page and roadmap.

**Exit:** Release to a wider invited beta only when privacy, reliability, and product gates pass.

### Backlog after this release, ordered by *evidence*

1. Gmail/Outlook integration **if** many active users require automatic multi-account sync and provider/compliance costs are viable.
2. Browser extension limited to policy-compliant, user-invoked capture where it measurably saves time.
3. Campus-drive notice templates/CSV importers based on supported specific formats.
4. More granular interview workflows, resume version association, calendar write-back.
5. Pricing, billing and broader go-to-market after retention and willingness-to-pay evidence.

---

## 17. Product metrics and go/no-go decisions

### North-star metric

**Weekly confirmed candidate actions resolved before deadline per active user**, with the originating source recorded.

This measures practical benefit more directly than raw applications stored or LLM requests made. Avoid claiming the app *caused* every action without a control group; track “assisted/reported” actions.

### Supporting metrics

- **Activation:** percentage of onboarded users who log ≥ 5 applications and set ≥ 1 deadline in first 48 h.
- **Time-to-value:** time to first useful Today card.
- **Effort:** median seconds to add application; weekly minutes spent correcting extracted events.
- **Reliability:** share of explicit hard deadlines detected, proposal accuracy, false alerts, notification delivery error rate.
- **Engagement:** weekly users completing ≥ 1 task, week-2 and week-4 return rates *during an active job search*.
- **Trust:** percentage accepting suggestions, dismissing a mismatch, disconnecting forwarding, privacy complaints.
- **Coverage:** applications tracked by capture source; number of relevant forwarded updates matched to correct applications.

### Proposed beta decisions (set before recruiting users)

| Gate | Green signal | Red signal / action |
|---|---|---|
| Repeated need | ≥ 70% pilot users report genuine recurring reminder/reconciliation pain | Narrow workflow or rethink target persona |
| Activation | ≥ 60% reach 5 applications + first action within 48 h | Simplify setup and imports |
| Weekly use | ≥ 50% of actively searching users complete/review an action in week 3 | Daily value weak; audit Today queue |
| Corrections | Median ≤ 2 minutes/week correcting app suggestions | Too much maintenance; reduce automation |
| Deadline trust | 0 critical missed due to accepted-but-wrong date in monitored beta, plus accuracy gates | Stop automation expansion |
| Pay signal | ≥ 5 users voluntarily commit to/pay a meaningful price in a real offer test | Treat as free utility or change buyer; don't infer pricing from survey answers |

These are practical **hypotheses**, not universal industry benchmarks. Sample sizes are small; supplement with qualitative evidence and iterate.

---

## 18. Go-to-market and business-model audit

### Distribution: start where the pain is concentrated

- Recruit testers from one engineering campus and adjacent student networks where consent/appropriate outreach is possible.
- Show a 45-second demo: old scattered inbox + portal notices -> confirmed **Today** action queue.
- Provide a clean import template and “track the next application in 30 seconds” onboarding.
- Publish transparent comparisons to **spreadsheets** and **Simplify/Teal**, without implying they lack features they already ship.
- Avoid growth tactics requiring personal data sharing, résumé harvesting or college-placement database extraction.

### Pricing hypothesis — **not a market finding**

- **Free:** unlimited manually tracked applications (within reasonable abuse limits), basic tasks, local reminders/export; no lock-in to retrieve user data.
- **Possible paid tier:** ₹99–199/month for automated forwarded-email triage, advanced multi-account organization, structured insights and priority support **only after** users prove willingness to pay.
- Alternative for students: **semester pass**, because placement search is seasonal. Price test rather than asserting superiority.
- Avoid offering “unlimited AI” until token economics are measured.
- No B2B placement-cell product until the consumer workflow has strong evidence and privacy approvals.

### Cost model: decision framework, not vendor quotes

For `N` active users and `E` relevant forwarded messages/user/month:

- `messages = N × E`
- `AI_calls = messages × %_escalated_to_LLM`
- `AI_cost = AI_calls × measured_cost_per_call`
- `monthly_variable_cost = AI_cost + intake_worker_usage + notification_sends + storage + transfer`
- `gross_margin = (revenue - monthly_variable_cost) / revenue`

Illustration **only**: 100 users × 30 relevant messages = **3,000 messages/month**. If 15% require an LLM, that's **450 model calls**; actual cost depends on model, token counts, retries and privacy controls. Measure these rather than assuming they are cheap.

Other expenses: domain, VPS/Postgres/backups, transactional mail, Cloudflare limits, monitoring, OAuth verification/security assessment/legal work, and support. Gmail compliance costs could dominate infrastructure and must be a **separate launch go/no-go**.

### Monetization risk

Free competitors are very strong. The likely initial outcome is a solid portfolio/personal utility and small user base, not guaranteed SaaS revenue. That is still valuable if engineered and benchmarked well.

---

## 19. Risk register

| Risk | Severity | Probability | Mitigation / release gate |
|---|---|---|---|
| User never forms tracking habit | Critical | High | Default Today, fast capture, calendar task utility; interview before build |
| Broad competitor parity | High | High | Measure time saved / deadline reliability; avoid commodity features |
| Gmail restricted scope approval/security costs | Critical for public Gmail sync | High | Do not require Gmail API for v1; forwarding/manual intake |
| LinkedIn/college portal extension breaks policy or account | Critical | Medium | No prohibited scraping or automation; policy review for each supported site |
| Wrong email-to-application match | Critical | Medium-high | Evidence, requisition ID, review inbox, no silent critical transitions |
| Inaccurate deadline / timezone | Critical | Medium-high | Exact vs date-only, validation, test corpus, manual confirmation |
| Forwarding not permitted in institutional accounts | High | High for some campuses | Manual text/share/CSV paths; separate identity from inbox connection |
| Email spoofing / guessed intake address | High | Medium | High-entropy aliases, auth checks, rate limits, alias rotation, manual review |
| Sensitive candidate/email data leakage | Critical | Medium | Data minimization, encryption, authorization, external security review |
| Notifications fail silently | Critical | Medium | Retry/outbox, provider callbacks, health dashboard, explicit delivery states |
| LLM hallucination or prompt injection | High | Medium | Rules first, schema, quoted evidence, no model actions, human review |
| Low willingness to pay | High | High | Free utility first; real willingness-to-pay test; no premature billing |
| Feature overload/solo-dev burnout | High | High | 12-week scope cap; no resume builder/auto-apply |
| Brand conflict | Medium-high | Confirmed for original name | Retire ApplyFlow name; formal name clearance before launch |

---

## 20. Definition of done for v1

**A real candidate should be able to:**

- [ ] Sign in and track an application from any source without installing a browser extension.
- [ ] Mark `Saved` versus `Applied` with the correct email address used.
- [ ] Import a spreadsheet and review duplicates.
- [ ] View **Today** and see exact, date-only and unconfirmed deadlines clearly distinguished.
- [ ] Receive a test reminder and see whether delivery was attempted/succeeded/failed.
- [ ] Forward an OA/interview email from a second inbox, have it **suggest** the correct application and next action, then approve it.
- [ ] Resolve two similarly titled applications at the same company without the product silently choosing the wrong one.
- [ ] Correct an extracted date and have stale reminders cancelled.
- [ ] Use the product when school email forwarding is blocked.
- [ ] Export all records and delete their account/data.
- [ ] Confirm the product never claims an employer-internal status without evidence.

**The implementer should be able to:**

- [ ] Run a full local stack with documented setup.
- [ ] Pass automated unit, integration, E2E and authorization tests in CI.
- [ ] Deploy reproducibly with Docker and GitHub Actions.
- [ ] Restore a backup, replay a failed intake job safely and inspect notification delivery health.
- [ ] Publish privacy, security, data retention and limitation documentation.
- [ ] Produce a benchmark report on the email/date fixture set and known failure cases.

---

## 21. Final recommendation and next 3 actions

### Recommended scope

**Hireloop v1 = manual/CSV application tracking + reliable deadline/task queue + forwarding-based email evidence + reviewable event reconciliation.**

Everything else is conditional. This protects the product from becoming a generic tracker and protects implementation from premature OAuth, scraping, resume AI and infrastructure complexity.

### Immediate actions

1. **Validate:** interview 12–15 active candidates, with real anonymized workflows, and show a clickable Today prototype beside their current solution.
2. **Prototype the narrowest risk:** implement the assessment invitation flow end-to-end (manual/forwarded message -> match proposal -> correct deadline -> confirmed task -> reminder), testing same-company duplicates and date-only cases.
3. **Build the first release against gates:** freeze requirements, write the 200-case fixture corpus and ship a manual-first vertical slice before adding optional AI.

### What would change this recommendation?

- If most target users already trust and use Simplify/Teal, and the action queue saves little time: **don't build the SaaS**; consider a small personal tool or extension of their current workflow instead.
- If users strongly want auto-sync and accept the privacy model: scope public Gmail/Outlook integration only after compliance budget and provider restrictions are addressed.
- If campus data is the main source of misses: invest in human-approved placement notice templates rather than generic recruiter email AI.
- If most alerts are false positives or stale: stop automated extraction and strengthen manual deadline capture.

---

## 22. Portfolio-focused audit and revised execution roadmap

**Audit date:** 15 November 2026. This repository currently contains this plan and a minimal README; no application, tests, deployment, user research, or benchmarks were found. The existing market references and prices were not independently rechecked in this audit. Everything below is proposed work, not a claim of completed results. The milestones below refine the original schedule and take precedence where scope or sequencing differs.

### 22.1 What to improve and why

| Finding | Improvement | Evidence of completion |
|---|---|---|
| The plan is strong on product safety but spreads a solo build across many P0 features. A calendar screen, verified secondary addresses, full CSV mapping and merge machinery compete with the core action workflow. | Deliver a usable manual-first slice first; introduce CSV, identity verification and forwarding only when their milestone is reached. A task list and per-task `.ics` export do not require a separate calendar screen. | A stranger can sign in, record an application and deadline, complete a task, and export/delete their data on a deployed private alpha. |
| Forwarding is P1 but the existing v1 definition requires it, while “closed beta” is used for both. | Name the manual milestone **private alpha** and the forwarding milestone **closed beta**. The full v1 checklist remains the closed-beta target; do not describe alpha as v1 complete. | Each release has its own acceptance checks below. |
| A rules engine, 200 examples and optional AI arrive together, obscuring the actual incremental value. | Start with paste-to-review and a small labeled fixture set; add forwarding, rules, and only then optional AI. Evaluate each against the same held-out set, including a manual-only baseline. | Versioned benchmark report includes inputs, labels, splits, false positives/negatives, and known failure cases. |
| The 98% precision target can sound conclusive with very few exact-date examples, and precision alone misses deadlines never suggested. | Report exact counts and precision **and recall** for deadlines and matches, split by case type; distinguish proposal quality from user-confirmed outcomes. Keep the 200-case corpus as a later target, not a prerequisite for the first slice. Publish uncertainty and do not tune on the held-out set. | Report TP/FP/FN and denominators, corpus provenance, category coverage and unresolved errors; do not claim population-level reliability from a small synthetic sample. |
| Queue idempotency cannot guarantee exactly-once delivery across a database and an external mail provider; callbacks may also arrive late or twice. | Use transactional scheduling, stable notification IDs, provider idempotency if supported, deduplicated callbacks and explicit uncertain delivery state. Suppress stale reminders before dispatch and test races, retries and provider timeouts. | Failure-injection test demonstrates safe retry and documents any remaining duplicate-send window; dispatch, provider acceptance and delivery are separate states. |
| Account deletion and event history/backup retention are specified without resolving their interaction. | Make history append-only during normal use, but allow authorized account erasure. Define deletion of primary data, aliases, pending jobs, provider data and temporary files; document backup expiry and prevent restoration from resurrecting deleted accounts. | Deletion test covers queued work and restored backup procedure; privacy notice states actual retention and exceptions. |
| Hard deadlines are first-class, yet ambiguous dates, stale job leases, authorization and import safety need more explicit proof. | Make date-only vs exact instants, user ownership, bounded uploads, safe CSV exports and stale-worker handling release blockers. Do not fetch pasted URLs automatically. | Focused integration tests exercise each boundary, plus a documented failure/recovery drill. |
| The roadmap emphasizes commercial validation but leaves portfolio presentation implicit. | Produce a reproducible demo, architecture decision records, a benchmark, an operations/recovery report and a concise case study alongside the actual work. | Public artifacts link to reproducible commands and measured results, with synthetic demo data. |

### 22.2 Priority roadmap and deliverables

Dates in section 16 are estimates rather than commitments; complete these gates in order, and reduce scope rather than weakening privacy or correctness. Stack choices in section 11 are proposals: use them if they support the core flow, not to increase the technology count. No code paths or package names are fixed until scaffolding establishes the actual structure.

**Milestone A — product proof and runnable foundation.** Write the core user journey and a small threat model; record 5–10 representative synthetic examples, including same-company roles and date-only deadlines. Scaffold the selected stack, migrations and CI, and document one-command local startup. Conduct interviews if participants are available; record the recruitment method and separate anecdotes from observed behavior. **Gate:** reproducible clean setup, passing basic checks, and a written decision on the narrow first workflow. **Portfolio artifact:** short problem statement, architecture diagram and two decisions with alternatives and tradeoffs.

**Milestone B — manual-first private alpha.** Implement sign-in and scoped application/task CRUD, saved vs applied state, Today ordering, exact/date-only deadlines, in-app state, a durable email reminder, and per-task `.ics`. Give users export and account deletion before external use; document retention and recovery. Verify ownership across two accounts, time-zone/date-only cases, task completion/cancellation and worker recovery. **Gate:** a new user completes capture → deadline → reminder → completion; the same user can export and delete their data, and another account cannot access it. Use a controlled test mailbox and record dispatch separately from delivery. **Portfolio artifact:** short demo and an integration-test/recovery report.

**Milestone C — evidence and safe reconciliation.** Add pasted message text, retained minimal evidence and a review inbox. A user associates or creates a record, edits the proposed deadline and explicitly approves changes; handle two roles at one employer and conflicting/rescheduled dates. Add forwarding only after this flow works, with a revocable high-entropy alias, authenticated handoff, size limits, retry/replay protection and privacy disclosures. Preserve a manual fallback. **Gate:** a replayed invitation creates one review item; ambiguity never silently changes a record; approving a reschedule cancels or suppresses obsolete unsent reminders. Test duplicate callbacks and the dispatch/reschedule race. **Portfolio artifact:** a sequence diagram and test cases demonstrating safe failure and recovery.

**Milestone D — evaluation and bounded enhancements.** Grow the fixture corpus toward 200 consented or synthetic messages, label it before benchmarking and keep a held-out set. Compare manual baseline, deterministic extraction and, only if justified, opt-in AI on identical examples; report quality, latency and measured cost per processed message, including failure cases. Add CSV preview/import with limits and explicit duplicate review if pilot users need it; verified secondary identities are separate from a user-entered application address. **Gate:** no unreviewed critical mutation, category-level results meet the stated thresholds with denominators shown, and users can correct every proposal. Disable unreliable categories rather than claiming coverage. **Portfolio artifact:** versioned benchmark with dataset limitations, sample errors and exact reproduction steps.

**Milestone E — controlled pilot and presentation.** Invite a small consented cohort only after security, deletion, notification and backup gates pass. Record number invited, onboarded and actively searching; observe task completion and report denominators and drop-off, not just percentages. Run an accessible mobile/keyboard walkthrough and document incident response and restore steps. **Gate:** decide to iterate, narrow scope or stop using the predeclared section 17 criteria; never claim causality or product–market fit from a small uncontrolled cohort. **Portfolio artifact:** anonymized case study, honest pilot results and a two-minute demo.

### 22.3 Optional enhancements, ranked by demonstrated value

1. **User-controlled calendar export and reschedule history:** already in the core plan; prioritize correctness and clear date precision over a separate calendar UI.
2. **CSV import with preview and rollback-safe commit:** add when pilot users bring spreadsheets; guard row/file limits, formula injection on export, duplicates and partial failures.
3. **Placement notice templates:** consider only after collecting permitted, varied samples; show the source and require confirmation, since campus formats change.
4. **Optional AI extraction:** only if the held-out comparison improves useful recall at an acceptable false-positive rate and cost. Treat message content as untrusted; never grant model tools or automatic mutation rights.
5. **Accessible demo mode:** synthetic preloaded records and a reset mechanism can help recruiters explore the app without exposing real users or sending email; isolate it from production accounts and disable external sends.
6. **Extension or mailbox sync:** defer until usage demonstrates the need and platform, privacy, verification and operational costs are understood. Neither is necessary for a credible portfolio project.

Do not add distributed services, complex dashboards, live recruiter integrations, paid billing or a generic AI assistant merely to increase resume keywords.

### 22.4 Resume evidence and presentation

Write bullets only after building and verifying the relevant work. Save the benchmark version, date, sample size, method and raw aggregate counts behind every number. Use placeholders until measured; distinguish a local synthetic test, provider acceptance, a real pilot and observed user behavior. Never invent users, accuracy, uptime, time savings, production scale or business impact.

Potential bullets, **templates rather than claims**:

- Built a candidate action tracker with Go, PostgreSQL and Next.js, separating applications, evidence and deadlines so users can review recruitment updates before changes take effect.
- Designed a durable reminder workflow with retries, stale-job protection and delivery-state reporting; verified recovery using [N] documented failure scenarios.
- Evaluated recruitment-message extraction on [N] labeled [synthetic/consented] examples; measured [TP/FP/FN or precision/recall] for [specific category] against a manual/rules baseline.
- Conducted a [N]-participant pilot and observed [measured outcome, denominator and timeframe]; used the findings to [specific product change].

For a recruiter-facing README, include a problem/solution paragraph, a 60–120 second demo, screenshots using synthetic data, an architecture diagram, local setup, key engineering tradeoffs, testing commands, benchmark methodology, limitations and links to the relevant reports. Explain personal ownership and collaborators accurately. A deployed demo is useful only if it is maintained, safe, and actually works; a recorded walkthrough and reproducible local setup are valid alternatives.

### 22.5 Cross-cutting acceptance details

- **Scheduling:** define leases using a database clock, recover expired leases, check current task/version immediately before dispatch, and log redacted correlation IDs. A provider timeout may be ambiguous; do not call this exactly-once delivery.
- **Dates:** require explicit interpretation when timezone or relative-date anchor is ambiguous; never infer end-of-day for date-only values. Test daylight-saving transitions for non-India zones and missed/offline schedules.
- **Import/export:** validate size, encoding and columns; preview rows and commit atomically or report per-row failures. Escape spreadsheet formula prefixes in CSV exports without altering canonical stored data.
- **Access and abuse:** test IDOR for application, task, evidence, export and intake endpoints; rate-limit alias use and verification attempts. Treat alias possession as permission to submit untrusted content, not proof of the sender's identity.
- **Privacy and deletion:** establish concrete retention and deletion behavior for source excerpts, provider logs, analytics, backups and legal exceptions before external beta. Document provider dependencies and deletion limitations truthfully.
- **Accessibility:** keyboard-operable review and forms, visible focus, labels, non-color status cues, screen-reader announcements for errors and mobile layouts; test the core flow with keyboard and a screen reader.

---

## 23. Research sources and verification notes

**Source hierarchy:** Official product help/pricing, platform developer policies, India government sources for market context; Reddit only for **qualitative** user pain. Vendor feature pages are self-reported. This research did **not** include paid competitor account testing or interviews with a representative sample.

### Competitors

- **[A1]** Simplify Gmail/email integration and suggested status approval: https://help.simplify.jobs/articles/0236686-email-integration
- **[A2]** Simplify job tracker and import: https://simplifyjobs.featurebase.app/help/articles/2140179-use-the-job-tracker
- **[A3]** Simplify+ current plan pricing: https://help.simplify.jobs/en/help/articles/5623502-whats-included-in-simplify-features-and-pricing
- **[A4]** Teal core job tracker: https://www.tealhq.com/tools/job-tracker
- **[A5]** Teal free and Plus feature/pricing comparison: https://help.tealhq.com/en/articles/9530153-teal-vs-teal
- **[A6]** Huntr plan types/limits and pricing: https://help.huntr.co/en/articles/10714568-plan-types-and-pricing
- **[A7]** Careerflow product page: https://www.careerflow.ai/
- **[A8]** Superset student recruitment and status: https://joinsuperset.com/student
- **[A9]** Rezome job tracker (India, campus and off-campus): https://rezome.in/job-tracker
- **[A10]** Rezome pricing: https://rezome.in/pricing
- **[A11]** ApplyOwl's own Gmail/Outlook description: https://applyowl.app/blog/job-application-tracker-gmail
- **[A12]** Mailflux alias-based tracker (private beta): https://mailflux.com/
- **[A13]** SoundCV email-forwarding tracker: https://www.soundcv.com/job-application-tracker

### Qualitative user pain (anecdotes, not market-size data)

- **[R1]** Reddit discussion on spreadsheets/reminders: https://www.reddit.com/r/productivity/comments/1rj3ppa/how_are_you_keeping_track_of_all_your_job/
- **[R2]** Reddit discussion on many applications and follow-ups: https://www.reddit.com/r/jobsearch/comments/1x60zm2/how_is_everyone_keeping_track_of_their_job/
- **[R3]** Reddit discussion on Gmail tracking: https://www.reddit.com/r/GoogleAppsScript/comments/1kerdv1/job_application_tracker_that_automatically_pulls/
- **[R4]** Indian developer-community campus/off-campus user need: https://www.reddit.com/r/developersIndia/comments/1v5yihw/built_a_tracker_for_offcampus_job_applications/

### Market context

- **[M1]** Ministry of Education AISHE release, **8 July 2026** (2022–23 and 2023–24): https://www.pib.gov.in/PressReleasePage.aspx?PRID=2282525&lang=1&reg=48

### API, privacy and operational constraints

- **[T1]** Gmail restricted scopes: https://developers.google.com/workspace/gmail/api/auth/scopes
- **[T2]** Google restricted scope verification/security assessment requirements: https://support.google.com/cloud/answer/13464321?hl=en
- **[T3]** Cloudflare Email Workers: https://developers.cloudflare.com/email-service/api/route-emails/email-handler/
- **[T4]** Gmail incremental synchronization, expired history cursor: https://developers.google.com/workspace/gmail/api/guides/sync
- **[T5]** Gmail push/watch renewal and fallback: https://developers.google.com/workspace/gmail/api/guides/push
- **[T6]** Microsoft Graph Outlook change notification and delegated Mail.Read: https://learn.microsoft.com/en-us/graph/outlook-change-notifications-overview
- **[T7]** Graph subscription expiry: https://learn.microsoft.com/en-us/graph/change-notifications-overview
- **[T8]** LinkedIn third-party automation/scraping policy: https://www.linkedin.com/help/linkedin/answer/a1341543
- **[T9]** Chrome `activeTab` and temporary permissions: https://developer.chrome.com/docs/extensions/develop
- **[T10]** Chrome Web Store user data/limited-use policy: https://developer.chrome.com/docs/webstore/program-policies/policies
- **[T11]** India DPDP Rules 2025 government release and phased setup: https://www.pib.gov.in/PressReleasePage.aspx?PRID=2190014&lang=1&reg=3

### Naming conflict sources

- **[N1]** Existing ApplyFlow AI in India: https://www.applyflow.in/
- **[N2]** Existing ApplyFlow job tracker: https://applyflow.store/
- **[N3]** Existing ApplyFlow tracker: https://www.applyflowtracker.com/

---

**Research limitations:** Pricing and advertised capabilities can change; official vendor pages are not independent audits. The reliability targets, pricing hypotheses, build timeline and pilot gates in this plan are proposed decisions, not observed outcomes. Trademark availability has not been certified. Privacy/legal obligations vary by deployment and should be professionally reviewed before public launch.
