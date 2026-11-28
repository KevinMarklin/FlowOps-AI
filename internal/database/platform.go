package database

const PlatformSchema = `
ALTER TABLE app_settings ADD COLUMN IF NOT EXISTS platform_config JSONB NOT NULL DEFAULT '{}';
ALTER TABLE app_settings ADD COLUMN IF NOT EXISTS platform_revision BIGINT NOT NULL DEFAULT 0;
CREATE INDEX IF NOT EXISTS tickets_tenant_created ON tickets(tenant_id,created_at,id);
CREATE INDEX IF NOT EXISTS audit_ticket_event ON audit_logs(tenant_id,ticket_id,event_type,created_at);
`
