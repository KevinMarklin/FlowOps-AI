package database

const DashboardSchema = `
CREATE TABLE IF NOT EXISTS analytics_dashboards (
 id TEXT PRIMARY KEY,
 tenant_id TEXT NOT NULL REFERENCES workspaces(tenant_id) ON DELETE CASCADE,
 owner_id TEXT NOT NULL,
 name TEXT NOT NULL,
 shared BOOLEAN NOT NULL DEFAULT FALSE,
 revision BIGINT NOT NULL DEFAULT 1,
 config JSONB NOT NULL,
 updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS analytics_dashboard_access ON analytics_dashboards(tenant_id,owner_id,shared);
`
