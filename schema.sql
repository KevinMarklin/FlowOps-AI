CREATE TABLE IF NOT EXISTS tickets (
    id SERIAL PRIMARY KEY,
    tenant_id TEXT NOT NULL DEFAULT 'default',
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
    tenant_id TEXT NOT NULL DEFAULT 'default',
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

CREATE TABLE IF NOT EXISTS api_keys (
    id SERIAL PRIMARY KEY,
    tenant_id TEXT NOT NULL DEFAULT 'default',
    name TEXT NOT NULL,
    key_prefix TEXT NOT NULL,
    key_hash TEXT NOT NULL UNIQUE,
    scope TEXT NOT NULL DEFAULT 'ingest',
    allowed_origin TEXT NOT NULL DEFAULT '',
    is_active BOOLEAN NOT NULL DEFAULT TRUE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    last_used_at TIMESTAMPTZ
);

CREATE TABLE IF NOT EXISTS app_settings (
    tenant_id TEXT NOT NULL DEFAULT 'default',
    id BOOLEAN NOT NULL DEFAULT TRUE CHECK (id),
    widget_title TEXT NOT NULL DEFAULT 'Онлайн-поддержка',
    widget_color TEXT NOT NULL DEFAULT '#2563eb',
    widget_greeting TEXT NOT NULL DEFAULT 'Здравствуйте! Чем мы можем помочь?',
    builder_config JSONB NOT NULL DEFAULT '{}'::jsonb,
    channel_config JSONB NOT NULL DEFAULT '{}'::jsonb,
    channel_metadata JSONB NOT NULL DEFAULT '{}'::jsonb,
    PRIMARY KEY (tenant_id,id)
);
CREATE TABLE IF NOT EXISTS channel_bindings (
    identity TEXT PRIMARY KEY,
    tenant_id TEXT NOT NULL,
    channel TEXT NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE TABLE IF NOT EXISTS workspaces (
    tenant_id TEXT PRIMARY KEY,
    name TEXT NOT NULL,
    token_prefix TEXT NOT NULL,
    token_hash TEXT NOT NULL UNIQUE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
INSERT INTO app_settings(tenant_id,id) VALUES ('default',TRUE) ON CONFLICT (tenant_id,id) DO NOTHING;

CREATE TABLE IF NOT EXISTS workspace_members (
 id TEXT PRIMARY KEY,
 tenant_id TEXT NOT NULL REFERENCES workspaces(tenant_id) ON DELETE CASCADE,
 display_name TEXT NOT NULL,
 email TEXT NOT NULL,
 role TEXT NOT NULL CHECK (role IN ('admin','operator','analyst','employee')),
 token_hash TEXT NOT NULL UNIQUE,
 is_active BOOLEAN NOT NULL DEFAULT TRUE,
 created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
 UNIQUE(tenant_id,email)
);
ALTER TABLE tickets ADD COLUMN IF NOT EXISTS requester_id TEXT NOT NULL DEFAULT '';
CREATE INDEX IF NOT EXISTS tickets_requester ON tickets(tenant_id,requester_id,created_at DESC);

ALTER TABLE workspace_members ADD COLUMN IF NOT EXISTS password_hash TEXT NOT NULL DEFAULT '';
CREATE INDEX IF NOT EXISTS workspace_members_login ON workspace_members(email) WHERE is_active=TRUE;
CREATE TABLE IF NOT EXISTS auth_sessions (
 token_hash TEXT PRIMARY KEY,
 csrf_hash TEXT NOT NULL,
 member_id TEXT NOT NULL REFERENCES workspace_members(id) ON DELETE CASCADE,
 expires_at TIMESTAMPTZ NOT NULL,
 created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS auth_sessions_member ON auth_sessions(member_id);
CREATE INDEX IF NOT EXISTS auth_sessions_expiry ON auth_sessions(expires_at);

ALTER TABLE workspace_members ADD COLUMN IF NOT EXISTS auth_version BIGINT NOT NULL DEFAULT 0;
ALTER TABLE auth_sessions ADD COLUMN IF NOT EXISTS auth_version BIGINT NOT NULL DEFAULT 0;
