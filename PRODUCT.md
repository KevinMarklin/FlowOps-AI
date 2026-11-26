# FlowOps AI

Corporate support service: employees submit requests; operators review the queue and maintain regulations; administrators configure channels and response styles. Existing workflows include Telegram, MAX, email, a web widget, knowledge import and a PostgreSQL leadership overview.

The hackathon product supports multiple isolated companies and personal roles: employee (own requests), operator (company queue and regulations), analyst (aggregate analytics), administrator (company settings and membership). Server authorization owns this boundary; UI visibility reflects it. Email/password login uses HttpOnly cookie sessions; personal codes activate or reset an account. SSO is outside this implementation.

## Platform and stack
Web, Go HTTP server, PostgreSQL, plain HTML/CSS/JavaScript. The website is a demonstration and operator interface for the service.

## Confirmed constraints
Preserve the navy, teal and light background palette. Replace the visual style and typography; use the generated FlowOps logo. Keep existing service functions and verify interface bugs. Do not change factual regulations or metrics definitions as part of the redesign.
