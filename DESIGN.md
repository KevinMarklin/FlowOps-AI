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
