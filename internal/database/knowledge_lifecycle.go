package database

const KnowledgeLifecycleSchema = `
CREATE TABLE IF NOT EXISTS kb_articles(
 id TEXT PRIMARY KEY,
 tenant_id TEXT NOT NULL REFERENCES workspaces(tenant_id) ON DELETE CASCADE,
 author_id TEXT NOT NULL,
 state TEXT NOT NULL CHECK(state IN ('draft','review','changes_requested','published','stale','archived')),
 revision BIGINT NOT NULL DEFAULT 1,
 published_revision BIGINT,
 document JSONB NOT NULL,
 approved_by TEXT NOT NULL DEFAULT '',
 updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE TABLE IF NOT EXISTS kb_article_versions(
 tenant_id TEXT NOT NULL,
 article_id TEXT NOT NULL REFERENCES kb_articles(id) ON DELETE CASCADE,
 revision BIGINT NOT NULL,
 state TEXT NOT NULL,
 document JSONB NOT NULL,
 actor TEXT NOT NULL,
 created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
 PRIMARY KEY(tenant_id,article_id,revision)
);
CREATE INDEX IF NOT EXISTS kb_article_list ON kb_articles(tenant_id,state,updated_at,id);
`
