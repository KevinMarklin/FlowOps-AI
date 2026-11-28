package database

const OptimizationSchema = `
CREATE TABLE IF NOT EXISTS optimization_decisions(
 tenant_id TEXT NOT NULL REFERENCES workspaces(tenant_id) ON DELETE CASCADE,
 id TEXT NOT NULL,
 state TEXT NOT NULL CHECK(state IN ('accepted','rejected','deferred','implemented')),
 assignee_id TEXT NOT NULL DEFAULT '',
 note TEXT NOT NULL DEFAULT '',
 revision BIGINT NOT NULL DEFAULT 1,
 baseline JSONB NOT NULL,
 tasks JSONB NOT NULL DEFAULT '[]',
 updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
 PRIMARY KEY(tenant_id,id)
);
CREATE TABLE IF NOT EXISTS optimization_semantic_clusters(
 tenant_id TEXT NOT NULL REFERENCES workspaces(tenant_id) ON DELETE CASCADE,
 id TEXT NOT NULL,
 payload JSONB NOT NULL,
 updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
 PRIMARY KEY(tenant_id,id)
);
`
