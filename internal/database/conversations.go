package database

const ConversationsSchema = `
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
CREATE TABLE IF NOT EXISTS ticket_operator_reads (
 tenant_id TEXT NOT NULL,
 ticket_id INTEGER NOT NULL REFERENCES tickets(id) ON DELETE CASCADE,
 operator_id TEXT NOT NULL,
 last_message_id BIGINT NOT NULL DEFAULT 0,
 PRIMARY KEY(tenant_id,ticket_id,operator_id)
);
`
