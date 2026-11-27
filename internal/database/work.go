package database

const WorkSchema = `
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
`
