# Platform expansion — implementation record

## Baseline audit (2026-11-28)

Go net/http + PostgreSQL + vanilla JS. Existing tenant principal middleware, member-bound cookie sessions, CSRF, scoped bot identities, bounded durable delivery, ticket dialogue/idempotency, builder JSON configuration, file-backed tenant knowledge bases, operator work leases and aggregate /api/stats are reused.

Critical seams:
- organization configuration: app_settings, workspaces and settings.Loader;
- authorization: internal/security/roles.go and tenant-bound SQL;
- events: existing audit_logs are incomplete and have textual payloads; new structured events must preserve history and distinguish observed events from reconstructed creation timestamps;
- analytics: existing /api/stats remains compatible; new exploration is server-side and tenant-scoped;
- knowledge: only approved published documents may enter the existing tenant RAG base;
- workflow: must use durable idempotent steps, not model-driven side effects;
- UI: existing navy/teal tokens, settings subnavigation and ticket route are preserved.

Current gaps: custom scoped roles, hierarchy/memberships, taxonomy/forms, calendars/SLA, filtered analytics, custom dashboards/reports, optimization review workflow, KB approval/version history, copilot, workflow engine, QR assets and AI usage accounting are not implemented at baseline. No financial savings, response times or AI charges may be fabricated for legacy records lacking evidence.

## Work log

This file records shipped stages and explicit remaining scope. See REQUEST.md for the complete acceptance criteria. An unchecked item is not a delivered feature.

- [x] Baseline architecture/data-flow review.
- [x] Organization profile/timezone/contact configuration, working calendar, economic inputs and customizable closure reasons with optimistic revisions.
- [ ] Structured process events, calendars and SLA.
- [ ] Server-side analytical exploration and report exports.
- [ ] Persisted dashboards and report templates/schedules.
- [ ] Organization hierarchy, custom roles, taxonomy/forms and rule-based routing.
- [ ] Optimization detection, evidence and recommendation workflow.
- [ ] Knowledge gaps, draft generation, approval and version lifecycle.
- [ ] Officially sourced Financial University configuration and wider template catalog.
- [ ] Copilot, durable workflows and context QR resources.
- [ ] Complete security/performance/end-to-end acceptance review.

## Evidence constraints

The official https://www.fa.ru/ returned HTTP 403 through the web tool on 2026-11-28. Existing docs/FA_KNOWLEDGE_SOURCES.md and tenant knowledge are useful baseline evidence, not proof of current internal regulations. University-specific limits, routes and deadlines remain demonstration data unless independently confirmed from an official source.

### Stage 1 — organization configuration

Added app_settings.platform_config/platform_revision with compatible defaults. GET/PUT /api/v1/settings/organization validates configuration and atomically updates workspace name and audit; only admins can write, analysts can read for calculations. GET /api/v1/organization returns the public subset without costs. Closure validation uses organization-specific reasons. Default workspace sync preserves edited names. UI: Organization and SLA subsection, calendar/cost/reason editors; pending settings-arrow request completed.

Validation: organization isolation/revision/conflict tests, calendar/holiday/DST unit tests, existing 21 UI regression tests and mocked-browser form-save/mobile verification. Configured SLA targets are not yet operational until Stage 2 deadline capture. No existing deadlines have been invented.

### Stage 2 — process events, SLA and analytical exploration

Migration: ticket_events, indexed tenant/time/ticket lookups; transactional triggers for creation/status/closure/reopen/department/work transitions; policy snapshots and first-response/resolution deadlines. Business deadlines respect local working days, holidays and DST. Legacy creation dates are reconstructed and explicitly marked; historic transitions/deadlines are not invented. SQL timestamp sessions now use UTC; organization timezone is a presentation/calendar input. Existing textual audit remains compatible. Ticket transactions set a local actor for event attribution.

API: GET /api/tickets/{id}/events with ownership enforcement and 100-event pages; GET /api/analytics/explore with combined cohort-date/department/requester/status/priority/channel/processing/SLA filters, PostgreSQL aggregation, median response/resolution metrics, load/dynamics, comparison-compatible bounds, documented methods, null unavailable metrics and CSV export. Administrator drill-down is bounded to 50 tickets; analysts receive aggregates and no ticket content. Maximum single report range is one year, query execution is bounded. /api/stats remains unchanged for compatibility.

UI: new analytical explorer, prior-period comparison, real counts/charts, per-metric methods/sample sizes, admin drill-down, CSV export, ticket event timeline and persisted SLA due dates. Organization input controls and earlier settings-marker removal styled consistently.

Validation: complete Go suite with isolated PostgreSQL passed; 21 JS tests passed; organization/SLA/analytics isolation and null-missing-data integration tests; live browser against isolated server verified settings save, dynamic empty/nonempty filtering, comparison and mobile no-overflow. Legacy records without reliable first-response/closure time show no data.

Remaining within analytics: organization hierarchy/category dimensions; user-configurable dashboards; XLSX/PDF; report schedules; ticket-specific calendars/pause/escalation rules; provider billing; confirmed routing labels; richer process-variant analysis. These are not represented as delivered.

### Stage 3 — persisted dashboards and report formats

Migration: tenant-scoped analytics_dashboards with ownership, shared visibility, configuration JSON and optimistic revisions. API: GET/POST /api/analytics/dashboards and PUT/DELETE /{id}. Analysts manage personal dashboards; administrators publish/manage shared dashboards; another user's private layout cannot be edited. Configs cap widget counts and accepted filter/visualization keys. All mutations are audited.

UI: multiple named dashboards, three templates, widget add/remove, keyboard move and drag ordering, width/height, per-widget filters, shared/personal copies. Nine visualizations use real aggregates. Distribution visualizes the distribution of group counts. The funnel is explicitly a sorted group distribution, not a claim of measured sequential process conversion. Report formats: authenticated server-generated CSV, XLSX and Unicode PDF with the bundled Golos font; no formulas or external spreadsheet references are emitted.

Validation: full Go suite and 21 JS tests passed. Dashboard ownership/sharing/revision tests; report ZIP/XML/formula tests; PDF rendered and visually reviewed; extracted Cyrillic verified; XLSX opened with openpyxl. Live browser verified widget types, layout persistence across reload, mobile no-overflow and downloaded all three report formats.

Remaining reporting scope: scheduled generation and fine-grained shared-dashboard grants. Current dashboard filters also serve as saved report selections, but there is no report scheduler yet. No unsupported sequential-funnel or AI-billing claim is made.

### Stage 4 — optimization and governed knowledge lifecycle

Migrations: optimization_decisions with optimistic versions and implementation tasks; semantic model-group snapshots; kb_articles and immutable kb_article_versions. Deterministic detectors cover exact normalized repeats, manually solved gaps, configured-SLA breaches, repeated redirection and old backlog. Recommendations retain evidence IDs, measured work, explicit scenario estimates, decisions, assignees, notes and tasks. Their saved baseline is distinct from the current selected period. PUT decisions is admin-only; analysts receive aggregate evidence without ticket IDs.

Semantic analysis is an explicit real-model action over at most 30 masked tickets. Every returned source ID is validated against the tenant/filter snapshot; unsupported/foreign IDs reject the result. If the configured structured model is unavailable, the API returns 503 and creates no fake groups. Numeric counts and scenario estimates remain deterministic. Endpoints: GET /api/optimization, PUT /api/optimization/{id}, POST /api/optimization/semantic.

Knowledge API: GET/POST /api/v1/kb/articles, PUT /articles/{id}, GET /articles/{id}/versions, POST /api/v1/kb/from-tickets. Draft/review/changes-requested/published/stale/archived states, revision conflicts, source provenance, review dates and restore-to-draft. Admin approval publishes; operators can author/submit. An edit preserves the previous approved revision in RAG until the new one is approved. Archive/stale withdraw publication; restore cannot auto-publish. The existing tenant knowledge module now takes an isolated approved-version snapshot per request; drafts never overwrite the shared RAG index. Legacy file-backed articles and editing remain compatible. Production smart-import persists drafts through the same lifecycle, and its existing form field/result-container mismatch was fixed.

Economic improvements: department-specific rates now drive estimated time values; infrastructure/support allocations are prorated by actual calendar month. Partial effect is explicitly incomplete; ROI/payback and AI cost remain null without confirmed complete financial data.

Validation: full Go suite and 21 JS tests passed after catching and correcting an overbroad KB path permission; new IDOR/evidence/revision/approval/RAG tests; concurrent snapshot isolation test; real browser verified accepted recommendation + task and full draft→review→publish→edit→archive→restore cycle against isolated DB. PDF/XLSX regression preserved.

Remaining: automatic semantic scheduling, conflicting-instruction semantic review, confirmed before/after causal impact, organization hierarchy/custom scopes, copilot, durable workflows, QR and report scheduling. Manual legacy publication remains the pre-existing explicit human action; AI import does not publish. Existing built-in roles remain authoritative until the custom-role extension is delivered.
