# Open ticket conversations

Employees continue an open ticket by sending messages. The website, Telegram and MAX share the history when the bot is linked to the same employee account. Opening a ticket in a bot selects it for subsequent ordinary messages. “New request” explicitly starts a separate issue. Selection survives worker restarts and is reauthorized after account-link changes.

The employee has two decisions: **Вопрос решён** and **Не помогло**. A rating from 1 to 5, with an optional website comment, belongs to an individual assistant/operator answer. Ratings neither close nor reopen tickets and remain available after closure. There is no “Continue dialogue” or “Need a specialist” button. An unsuccessful answer keeps the conversation open for the employee and operator. Replies sent from the updated operator interface await employee confirmation; delivery acknowledgements cannot reopen a ticket the employee closed.

## API and storage

- `GET /api/tickets/{id}/messages?before=ID`: latest 100 messages in chronological order, status and reply availability. `before` loads older messages.
- `POST /api/tickets/{id}/messages`: `{body, client_message_id}`. Same actor/key/body retries are idempotent; changed bodies with an existing key conflict. Closed tickets reject new messages.
- `POST /api/tickets/{id}/decision`: `{resolved}` from the requester.
- `POST /api/tickets/{id}/messages/{message}/rating`: `{score, comment}` from the requester, only for assistant/operator messages.

All routes use existing session, CSRF, tenant and ownership checks. Operator reads include the employee's feedback. The previous reply/feedback APIs remain for existing integrations; the updated website and bot adapters use the conversation operations.

Startup migrations add message, rating, bot-selection and receipt tables and a requester-closure flag. Old request/answer snapshots are imported once when the transcript is first accessed. User text is stored and displayed as text, never executable markup. Generation uses approved knowledge sources. Classifier/configuration calls run before taking the conversation write lock; closure, ownership and operator involvement are rechecked under the lock.

Every operator message has an immutable durable delivery event. Later answers do not supersede pending conversation messages. Linked website tickets can notify Telegram and MAX. Each delivery rechecks the account binding. Provider message IDs consumed during intake are recorded with ticket creation so that replayed clarification updates after restart do not become new conversation messages.

## Regulation retrieval fixes

The personal-portal topic guard now accepts ordinary login failures such as “Не могу войти”, not just requests containing “пароль” or “доступ”. Retrieval matches title/keyword topics with bounded Russian inflection handling. Generic body text and the “clarifications skipped” notice cannot select a rule. A relevant approved regulation may correct a mistaken classifier department, provided the tenant enables that department. These cases have regression tests, including old saved university department descriptions.

## Direction contract

Mode: Operate, extending the existing Golos Text, navy/teal and light/dark workspace. Keep the request form and routing summary; place the persistent transcript between result and history. Use role labels, timestamps and readable message surfaces. The operator opens one persistent transcript above the queue so polling does not destroy the composer. Keep draft text through failed sends and ticket switches; erase all conversation state at logout.

## Verification

PostgreSQL integration checks cover ownership, tenant isolation, duplicate sends, message ordering, ratings after closure, bot-link changes, restart persistence, clarification replay and independent delivery of multiple answers. Channel tests exercise native Telegram menu setup, ordinary-message continuation, identity checks for callbacks, ratings and MAX inline keyboards. Browser verification uses separate employee/operator sessions, real registration/session APIs, mobile and desktop layouts, and light/dark themes. Runtime preview data and screenshots stay outside shipping source.
