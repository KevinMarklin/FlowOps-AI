package database

const OneCSchema = `
CREATE TABLE IF NOT EXISTS onec_connections (
 tenant_id TEXT PRIMARY KEY,
 config JSONB NOT NULL,
 secret_ciphertext TEXT NOT NULL DEFAULT '',
 updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
`
