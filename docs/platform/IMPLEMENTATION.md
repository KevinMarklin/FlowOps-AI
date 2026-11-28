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
