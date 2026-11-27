package database

const IdempotencySchema = `
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
`
