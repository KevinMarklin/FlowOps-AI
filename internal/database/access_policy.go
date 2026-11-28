package database

const AccessPolicySchema = `
CREATE TABLE IF NOT EXISTS organization_access_roles(
 tenant_id TEXT NOT NULL REFERENCES workspaces(tenant_id) ON DELETE CASCADE,
 id TEXT NOT NULL,
 name TEXT NOT NULL,
 base_role TEXT NOT NULL CHECK(base_role IN ('operator','analyst','employee')),
 scope TEXT NOT NULL CHECK(scope IN ('own','organization','units')),
 unit_ids JSONB NOT NULL DEFAULT '[]',
 revision BIGINT NOT NULL DEFAULT 1,
 PRIMARY KEY(tenant_id,id)
);

CREATE TABLE IF NOT EXISTS organization_member_roles(
 tenant_id TEXT NOT NULL,
 member_id TEXT NOT NULL,
 role_id TEXT NOT NULL,
 PRIMARY KEY(tenant_id,member_id),
 FOREIGN KEY(tenant_id,member_id) REFERENCES workspace_members(tenant_id,id) ON DELETE CASCADE,
 FOREIGN KEY(tenant_id,role_id) REFERENCES organization_access_roles(tenant_id,id)
);
CREATE OR REPLACE FUNCTION flowops_ticket_visible(org TEXT,person TEXT,department_code TEXT,requester TEXT) RETURNS BOOLEAN LANGUAGE sql STABLE AS $$
 WITH RECURSIVE policy AS (SELECT r.scope,r.unit_ids FROM organization_member_roles m JOIN organization_access_roles r ON r.tenant_id=m.tenant_id AND r.id=m.role_id WHERE m.tenant_id=org AND m.member_id=person), units AS(
 SELECT u.id,u.routing_code FROM organization_units u,policy p WHERE u.tenant_id=org AND u.active AND p.unit_ids @> to_jsonb(ARRAY[u.id])
 UNION SELECT child.id,child.routing_code FROM organization_units child JOIN units parent ON child.parent_id=parent.id WHERE child.tenant_id=org AND child.active
 ) SELECT NOT EXISTS(SELECT 1 FROM policy) OR requester=person OR EXISTS(SELECT 1 FROM policy WHERE scope='organization') OR EXISTS(SELECT 1 FROM policy WHERE scope='units') AND EXISTS(SELECT 1 FROM units WHERE routing_code<>'' AND routing_code=department_code)
$$;
`
