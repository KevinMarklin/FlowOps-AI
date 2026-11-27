# University intake pilot

The implementation extends the existing service; it does not claim an integration with itsm.fa.ru or an approved university deployment.

## Employee website

Describe the problem and select Continue. Classroom equipment and access problems open a bounded clarification form. Explicit facts (for example a named personal account or “in 10 minutes”) are reused. Missing details can be supplied or deliberately skipped. A read-only authenticated POST /api/v1/intake/preview produces the same deterministic plan as bot workers, without a model call or ticket creation. Final POST /api/tickets persists the original request and the supplied context in raw_text, under existing tenant and requester ownership. Existing clients without intake stay compatible.

## Telegram and MAX

Telegram starts from New request. MAX accepts a message in a private chat. Both reuse internal/intake and ask one missing question at a time. /skip hands off the partial context; /cancel discards it. Drafts expire after 15 minutes, are bounded to 4096 conversations per worker, are not logged and are discarded on restart. A stale answer does not become a ticket. Telegram preserves its original message idempotency key across clarification and retry; MAX uses the original message MID where provided by the platform. Draft data stays in worker memory until submission; submitted context is stored in PostgreSQL as part of the request.

MAX supports /yes ID and /no ID for the existing requester-scoped feedback service. Telegram keeps its existing buttons. An offered instruction is not counted as a confirmed resolution. Group chats do not enter the clarification dialogue.

## Safety and limitations

No passwords, OTPs or identity documents are requested. Supplementary fields are single-line, capped at 300 Unicode characters and checked by existing prompt guards. All context is untrusted requester input; it does not set departments, grant permissions or override routing policy. Existing masking/provider policies still apply to the complete composed text. Body limits apply after composition. No promises of arrival time or university SLA are generated. Pattern-based fact extraction is intentionally limited; users can skip a redundant question.

The pilot should use an approved location/service directory and reviewed regulations before production. No live platform bot messages are sent by the development tests.

## Demonstration and evaluation

1. “In ten minutes there is a class, the projector shows no image.” Supply the location and previous checks. Verify a single ticket contains every answer and is visible only to its requester and authorized operators.
2. “I cannot log in to the personal account.” Supply the observed error and checks; inspect the proposed instruction or escalation, then submit feedback.
3. Cancel, skip, expire and retry a draft. Verify no partial ticket or duplicate is introduced.
4. Reuse the existing temporary-incident feature for a confirmed service outage. Informing a user about an incident does not prove that their problem has been solved.

Compare ordinary intake with FlowOps on the same anonymized cases: operator handling time, additional clarification count, reassignment count and user-confirmed instruction success. Existing operator work measurements are observed time; baseline savings remain assumptions until measured. Do not turn queue waiting time into labor savings or claim cash savings without a change in spending.

Design/testing guidance is installed locally under .agents/skills from anthropics/skills (frontend-design, Apache-2.0) and openai/skills (playwright). These are development instructions and are not served by the application. In a new Codex turn the project-local skills can be discovered automatically; this implementation read their instructions directly.
