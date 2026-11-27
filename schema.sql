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


CREATE TABLE IF NOT EXISTS bot_link_codes (
 token_hash TEXT PRIMARY KEY,
 member_id TEXT NOT NULL REFERENCES workspace_members(id) ON DELETE CASCADE,
 channel TEXT NOT NULL CHECK(channel IN ('telegram','max')),
 auth_version BIGINT NOT NULL,
 expires_at TIMESTAMPTZ NOT NULL,
 UNIQUE(member_id,channel)
);
CREATE TABLE IF NOT EXISTS member_bot_links (
 bot_tenant_id TEXT NOT NULL,
 sender TEXT NOT NULL,
 channel TEXT NOT NULL CHECK(channel IN ('telegram','max')),
 member_id TEXT NOT NULL REFERENCES workspace_members(id) ON DELETE CASCADE,
 auth_version BIGINT NOT NULL,
 created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
 PRIMARY KEY(bot_tenant_id,sender),
 UNIQUE(member_id,channel)
);
ALTER TABLE tickets ADD COLUMN IF NOT EXISTS bot_tenant_id TEXT NOT NULL DEFAULT '';

-- Keyset pagination for operator queues and personal request history.
ALTER TABLE workspaces ADD COLUMN IF NOT EXISTS organization_type TEXT NOT NULL DEFAULT 'other';
CREATE INDEX IF NOT EXISTS tickets_tenant_cursor ON tickets(tenant_id,id DESC);
CREATE INDEX IF NOT EXISTS tickets_requester_cursor ON tickets(tenant_id,requester_id,id DESC);


CREATE TABLE IF NOT EXISTS ticket_work_sessions (
 tenant_id TEXT NOT NULL REFERENCES workspaces(tenant_id) ON DELETE CASCADE,
 operator_id TEXT NOT NULL,
 ticket_id INTEGER NOT NULL REFERENCES tickets(id) ON DELETE CASCADE,
 active_seconds DOUBLE PRECISION NOT NULL DEFAULT 0 CHECK(active_seconds>=0),
 running BOOLEAN NOT NULL DEFAULT FALSE,
 last_seen_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
 PRIMARY KEY(tenant_id,operator_id,ticket_id)
);
CREATE UNIQUE INDEX IF NOT EXISTS ticket_work_one_active ON ticket_work_sessions(tenant_id,operator_id) WHERE running=TRUE;
CREATE INDEX IF NOT EXISTS ticket_work_totals ON ticket_work_sessions(tenant_id,ticket_id);


CREATE TABLE IF NOT EXISTS ticket_requests (
 tenant_id TEXT NOT NULL,
 scope_hash TEXT NOT NULL,
 key_hash TEXT NOT NULL,
 payload_hash TEXT NOT NULL,
 ticket_id INTEGER NOT NULL REFERENCES tickets(id) ON DELETE CASCADE,
 created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
 PRIMARY KEY(tenant_id,scope_hash,key_hash)
);
ALTER TABLE ticket_requests DROP CONSTRAINT IF EXISTS ticket_requests_tenant_id_fkey;
CREATE INDEX IF NOT EXISTS ticket_requests_ticket ON ticket_requests(ticket_id);

-- Persistent ticket conversations and answer ratings.

ALTER TABLE tickets ADD COLUMN IF NOT EXISTS requester_closed BOOLEAN NOT NULL DEFAULT FALSE;
CREATE TABLE IF NOT EXISTS ticket_messages (
 id BIGSERIAL PRIMARY KEY,
 tenant_id TEXT NOT NULL,
 ticket_id INTEGER NOT NULL REFERENCES tickets(id) ON DELETE CASCADE,
 role TEXT NOT NULL CHECK(role IN ('employee','assistant','operator','system')),
 actor TEXT NOT NULL DEFAULT '',
 body TEXT NOT NULL,
 source_key TEXT NOT NULL,
 created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
 UNIQUE(tenant_id,ticket_id,source_key)
);
CREATE INDEX IF NOT EXISTS ticket_messages_page ON ticket_messages(tenant_id,ticket_id,id DESC);
CREATE TABLE IF NOT EXISTS ticket_answer_ratings (
 message_id BIGINT NOT NULL REFERENCES ticket_messages(id) ON DELETE CASCADE,
 actor TEXT NOT NULL,
 score INTEGER NOT NULL CHECK(score BETWEEN 1 AND 5),
 comment TEXT NOT NULL DEFAULT '',
 updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
 PRIMARY KEY(message_id,actor)
);
CREATE TABLE IF NOT EXISTS bot_dialogues (
 bot_tenant_id TEXT NOT NULL,
 sender TEXT NOT NULL,
 ticket_id INTEGER REFERENCES tickets(id) ON DELETE CASCADE,
 PRIMARY KEY(bot_tenant_id,sender)
);
ALTER TABLE ticket_deliveries ADD COLUMN IF NOT EXISTS conversation_message_id BIGINT REFERENCES ticket_messages(id) ON DELETE CASCADE;
ALTER TABLE ticket_deliveries ADD COLUMN IF NOT EXISTS bot_route_tenant TEXT NOT NULL DEFAULT '';
CREATE TABLE IF NOT EXISTS bot_dialogue_receipts (
 bot_tenant_id TEXT NOT NULL,
 sender TEXT NOT NULL,
 key_hash TEXT NOT NULL,
 ticket_id INTEGER NOT NULL REFERENCES tickets(id) ON DELETE CASCADE,
 PRIMARY KEY(bot_tenant_id,sender,key_hash)
);
