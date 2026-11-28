package database

const StructureSchema = `
CREATE UNIQUE INDEX IF NOT EXISTS workspace_member_tenant_identity ON workspace_members(tenant_id,id);
CREATE TABLE IF NOT EXISTS organization_units(
 tenant_id TEXT NOT NULL REFERENCES workspaces(tenant_id) ON DELETE CASCADE,
 id TEXT NOT NULL,
 parent_id TEXT,
 name TEXT NOT NULL,
 kind TEXT NOT NULL DEFAULT 'department',
 routing_code TEXT NOT NULL DEFAULT '',
 active BOOLEAN NOT NULL DEFAULT TRUE,
 revision BIGINT NOT NULL DEFAULT 1,
 PRIMARY KEY(tenant_id,id),
 FOREIGN KEY(tenant_id,parent_id) REFERENCES organization_units(tenant_id,id)
);
CREATE UNIQUE INDEX IF NOT EXISTS organization_unit_routing ON organization_units(tenant_id,routing_code) WHERE routing_code<>'';
CREATE INDEX IF NOT EXISTS organization_unit_parent ON organization_units(tenant_id,parent_id);
CREATE TABLE IF NOT EXISTS organization_unit_members(
 tenant_id TEXT NOT NULL,
 unit_id TEXT NOT NULL,
 member_id TEXT NOT NULL,
 relation TEXT NOT NULL CHECK(relation IN ('member','manager','deputy','responsible')),
 PRIMARY KEY(tenant_id,unit_id,member_id,relation),
 FOREIGN KEY(tenant_id,unit_id) REFERENCES organization_units(tenant_id,id),
 FOREIGN KEY(tenant_id,member_id) REFERENCES workspace_members(tenant_id,id)
);
`
