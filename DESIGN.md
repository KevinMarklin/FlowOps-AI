# FlowOps workspace

Mode: Operate. An employee or support operator uses the application at a desk; light working surfaces keep long forms and queues readable, with a navy brand header.

Palette retained: ink #102a43, action #0b5367, blue #147d92, teal #32a7a0, background #f4f7fa, white #ffffff. Secondary text #526d82.

Typography: locally served Golos Text, regular through bold, with Cyrillic coverage. Headings use medium weight and restrained negative tracking. Numbers use tabular figures.

Desktop navigation is a persistent left rail; below 850px it becomes horizontal scrolling navigation. Content leads with a compact heading, followed directly by the task. Employee request form is primary; the result and history form a secondary column. Mobile collapses these into one column. Metrics share a single summary surface.

No decorative eyebrows, section numbering, sparkle symbols, gradient backgrounds or display-serif headings. Teal identifies actions and selection. Forms and meaningful groups use borders; typography and spacing carry the rest.

Logo provenance: generated in the dedicated FlowOps AI logo chat, 01a2189e-abcd-7eb3-8c7b-beeb14d9857f, standalone nested-F variant. Original PNG is shipped unchanged as web/assets/flowops-mark.png. Font source: Google Fonts Golos Text, OFL license included alongside font files.

Role-aware surfaces: employee requests, operator queue and regulations, analyst aggregates, administrator membership and settings. Analytics is a separate section. Navigation includes only granted surfaces, matching server permissions. The administrator landing surface prioritizes inviting the team and managing access. Personal codes appear once and are removed from DOM on session changes.

Authentication: email/password is primary. First-time activation and password recovery expand inside a native details section; new-company creation remains below. Keep password-manager autocomplete and visible labels. Never store session secrets in JS storage.

Workflow refinement: employees have a full-width workspace without a redundant single-item rail. The request form explains useful input; the result leads with its actual outcome (regulation, specialist handoff, or completed response), followed by the summary, responsible department and priority. Source instructions stay readable after confirmation or escalation. History has text/ID search; the operator queue combines search, priority, department and status. Source text and reply composition use native disclosure controls; drafts survive polling. Navy, teal and Golos Text remain the visual identity. The implementation lives in workflow.css and the existing app.js; mobile stacks the employee flow and queue filters.

Personal settings open from an accessible three-line icon in the top-right header. A native dialog contains theme selection and employee Telegram/MAX connections. Light and dark themes use the same navy/teal identity; dark surfaces use #101e2a and #1a2c3b with #e0edf7 text. Theme preference is local to the browser. Connection codes are displayed temporarily, never persisted in browser storage, and cleared on close or account change. Mobile preserves the header name and keeps the dialog within the viewport.

Connection refinement: Telegram offers a direct, short-lived bot link after code issuance, with manual copying as fallback. Pending actions cannot be submitted twice; stale responses cannot restore cleared codes. History and queue use bounded pages with explicit older/latest navigation and page-scoped search.

Pilot evidence: the operator action opens the reply editor and begins visible work measurement, with an explicit pause. Analytics separates observed operator minutes, routing changes and hypothetical savings. Six metrics form a three-column desktop layout, two columns on medium widths and one on mobile.

University intake refinement: preserve the established Golos Text, navy #102a43, action #0b5367, blue #147d92, muted #526d82, white #ffffff and background #f4f7fa. A blue left rule marks the optional clarification stage inside the existing request form; it is a continuation of the request, rather than a modal or a second ticket. The employee gives a location, symptom, work impact and checks only when explicit facts are missing. Three task examples lead with classroom equipment, service access and a study certificate. Labels remain left aligned, keyboard focus moves to the first unanswered field, mobile stacks naturally, and dark mode keeps the same hierarchy. The operator sees requester context immediately above routing controls. No university crest or claim of an official deployment is added.
