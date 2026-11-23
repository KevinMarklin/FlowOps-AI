CREATE TABLE IF NOT EXISTS tickets (
    id SERIAL PRIMARY KEY,
    user_email VARCHAR(255),
    channel VARCHAR(50) DEFAULT 'web',
    subject TEXT,
    callback_url TEXT,
    raw_text TEXT NOT NULL,
    department VARCHAR(100) DEFAULT 'Unassigned',
    priority VARCHAR(50) DEFAULT 'medium',
    extracted_summary TEXT,
    rag_answer TEXT,
    operator_reply TEXT,
    pii_masked_count INTEGER NOT NULL DEFAULT 0,
    pii_masked_types TEXT[] NOT NULL DEFAULT '{}',
    status VARCHAR(50) DEFAULT 'new',
    delivery_status TEXT NOT NULL DEFAULT '',
    reply_version INTEGER NOT NULL DEFAULT 0,
    reply_hash TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS audit_logs (
    id SERIAL PRIMARY KEY,
    created_at TIMESTAMPTZ DEFAULT NOW(),
    event_type VARCHAR(64) NOT NULL,
    actor VARCHAR(128) NOT NULL,
    ticket_id INT NULL,
    details TEXT
);

CREATE TABLE IF NOT EXISTS ticket_deliveries (
    id BIGSERIAL PRIMARY KEY,
    ticket_id INTEGER NOT NULL REFERENCES tickets(id) ON DELETE CASCADE,
    reply_version INTEGER NOT NULL,
    kind TEXT NOT NULL,
    channel TEXT NOT NULL DEFAULT '',
    recipient TEXT NOT NULL,
    body TEXT NOT NULL,
    dedupe_key TEXT NOT NULL,
    state TEXT NOT NULL DEFAULT 'pending' CHECK(state IN ('pending','processing','sent','failed','superseded')),
    attempts INTEGER NOT NULL DEFAULT 0,
    next_attempt_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    locked_until TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE(ticket_id,reply_version,dedupe_key)
);
CREATE INDEX IF NOT EXISTS ticket_deliveries_ready ON ticket_deliveries(next_attempt_at) WHERE state IN ('pending','processing');
