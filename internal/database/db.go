package database

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	"flowops-ai/internal/security"
	"github.com/jackc/pgx/v5/pgxpool"
)

func NewPool(ctx context.Context, databaseURL string) (*pgxpool.Pool, error) {
	config, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		return nil, fmt.Errorf("parse database config: %w", err)
	}
	config.ConnConfig.RuntimeParams["client_encoding"] = "UTF8"
	config.ConnConfig.RuntimeParams["timezone"] = "UTC"

	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		return nil, fmt.Errorf("create database pool: %w", err)
	}

	pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := pool.Ping(pingCtx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping database: %w", err)
	}

	return pool, nil
}

func Migrate(ctx context.Context, pool *pgxpool.Pool) error {
	const query = `
CREATE TABLE IF NOT EXISTS tickets (
    id SERIAL PRIMARY KEY,
    tenant_id TEXT NOT NULL DEFAULT 'default',
    user_email VARCHAR(255),
    channel VARCHAR(50) DEFAULT 'web',
    subject TEXT,
    callback_url TEXT,
    raw_text TEXT NOT NULL,
    department VARCHAR(100) DEFAULT 'Unassigned',
    priority VARCHAR(50) DEFAULT 'medium',
    extracted_summary TEXT,
    rag_answer TEXT,
    pii_masked_count INTEGER NOT NULL DEFAULT 0,
    pii_masked_types TEXT[] NOT NULL DEFAULT '{}',
    status VARCHAR(50) DEFAULT 'new',
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);`

	if _, err := pool.Exec(ctx, query); err != nil {
		return fmt.Errorf("create tickets table: %w", err)
	}
	for _, statement := range []string{
		`ALTER TABLE tickets ADD COLUMN IF NOT EXISTS delivery_status TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE tickets ADD COLUMN IF NOT EXISTS tenant_id TEXT NOT NULL DEFAULT 'default'`,
		`ALTER TABLE tickets ADD COLUMN IF NOT EXISTS reply_version INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE tickets ADD COLUMN IF NOT EXISTS reply_hash TEXT NOT NULL DEFAULT ''`,
		`CREATE TABLE IF NOT EXISTS ticket_deliveries (
   id BIGSERIAL PRIMARY KEY,
   ticket_id INTEGER NOT NULL REFERENCES tickets(id) ON DELETE CASCADE,
   reply_version INTEGER NOT NULL,
   kind TEXT NOT NULL,
   channel TEXT NOT NULL DEFAULT '',
   recipient TEXT NOT NULL,
   body TEXT NOT NULL,
   dedupe_key TEXT NOT NULL,
   state TEXT NOT NULL DEFAULT 'pending' CHECK(state IN ('pending','processing','sent','failed','superseded')),
   attempts INTEGER NOT NULL DEFAULT 0,
   next_attempt_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
   locked_until TIMESTAMPTZ,
   created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
   UNIQUE(ticket_id,reply_version,dedupe_key)
  )`,
		`CREATE INDEX IF NOT EXISTS ticket_deliveries_ready ON ticket_deliveries(next_attempt_at) WHERE state IN ('pending','processing')`,
		`ALTER TABLE tickets ADD COLUMN IF NOT EXISTS pii_masked_count INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE tickets ADD COLUMN IF NOT EXISTS pii_masked_types TEXT[] NOT NULL DEFAULT '{}'`,
		`CREATE TABLE IF NOT EXISTS audit_logs (
			id SERIAL PRIMARY KEY,
			tenant_id TEXT NOT NULL DEFAULT 'default',
			created_at TIMESTAMPTZ DEFAULT NOW(),
			event_type VARCHAR(64) NOT NULL,
			actor VARCHAR(128) NOT NULL,
			ticket_id INT NULL,
			 details TEXT
		)`,
		`CREATE TABLE IF NOT EXISTS api_keys (
			id SERIAL PRIMARY KEY,
			tenant_id TEXT NOT NULL DEFAULT 'default',
			name TEXT NOT NULL,
			key_prefix TEXT NOT NULL,
			key_hash TEXT NOT NULL UNIQUE,
			scope TEXT NOT NULL DEFAULT 'ingest',
			allowed_origin TEXT NOT NULL DEFAULT '',
			is_active BOOLEAN NOT NULL DEFAULT TRUE,
			created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			last_used_at TIMESTAMPTZ
		)`,
		`CREATE TABLE IF NOT EXISTS app_settings (
			tenant_id TEXT NOT NULL DEFAULT 'default',
			id BOOLEAN NOT NULL DEFAULT TRUE CHECK (id),
			widget_title TEXT NOT NULL DEFAULT 'Онлайн-поддержка',
			widget_color TEXT NOT NULL DEFAULT '#2563eb',
			widget_greeting TEXT NOT NULL DEFAULT 'Здравствуйте! Чем мы можем помочь?',
			builder_config JSONB NOT NULL DEFAULT '{}'::jsonb,
			channel_config JSONB NOT NULL DEFAULT '{}'::jsonb,
			channel_metadata JSONB NOT NULL DEFAULT '{}'::jsonb,
			PRIMARY KEY (tenant_id,id)
		)`,
		`CREATE TABLE IF NOT EXISTS workspaces (tenant_id TEXT PRIMARY KEY, name TEXT NOT NULL, token_prefix TEXT NOT NULL, token_hash TEXT NOT NULL UNIQUE, created_at TIMESTAMPTZ NOT NULL DEFAULT NOW())`,
		`ALTER TABLE workspaces ADD COLUMN IF NOT EXISTS organization_type TEXT NOT NULL DEFAULT 'other'`,
		`ALTER TABLE api_keys ADD COLUMN IF NOT EXISTS tenant_id TEXT NOT NULL DEFAULT 'default'`,
		`ALTER TABLE app_settings ADD COLUMN IF NOT EXISTS tenant_id TEXT NOT NULL DEFAULT 'default'`,
		`ALTER TABLE audit_logs ADD COLUMN IF NOT EXISTS tenant_id TEXT NOT NULL DEFAULT 'default'`,
		`DO $$ DECLARE old_pk TEXT; old_size INT; BEGIN SELECT conname,array_length(conkey,1) INTO old_pk,old_size FROM pg_constraint WHERE conrelid='app_settings'::regclass AND contype='p'; IF old_pk IS NOT NULL AND old_size = 1 THEN EXECUTE format('ALTER TABLE app_settings DROP CONSTRAINT %I',old_pk); END IF; END $$`,
		`DO $$ BEGIN IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conrelid='app_settings'::regclass AND contype='p') THEN ALTER TABLE app_settings ADD CONSTRAINT app_settings_pkey PRIMARY KEY (tenant_id,id); END IF; END $$`,
		`ALTER TABLE app_settings ADD COLUMN IF NOT EXISTS builder_config JSONB NOT NULL DEFAULT '{}'::jsonb`,
		`ALTER TABLE app_settings ADD COLUMN IF NOT EXISTS channel_config JSONB NOT NULL DEFAULT '{}'::jsonb`,
		`ALTER TABLE app_settings ADD COLUMN IF NOT EXISTS channel_metadata JSONB NOT NULL DEFAULT '{}'::jsonb`,
		`CREATE TABLE IF NOT EXISTS channel_bindings (identity TEXT PRIMARY KEY, tenant_id TEXT NOT NULL, channel TEXT NOT NULL, updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW())`,
		`DO $$ DECLARE c TEXT; BEGIN SELECT conname INTO c FROM pg_constraint WHERE conrelid='channel_bindings'::regclass AND contype='u' AND pg_get_constraintdef(oid)='UNIQUE (tenant_id, channel)'; IF c IS NOT NULL THEN EXECUTE format('ALTER TABLE channel_bindings DROP CONSTRAINT %I',c); END IF; END $$`,
		`ALTER TABLE app_settings ADD COLUMN IF NOT EXISTS widget_title TEXT NOT NULL DEFAULT 'Онлайн-поддержка'`,
		`ALTER TABLE app_settings ADD COLUMN IF NOT EXISTS widget_color TEXT NOT NULL DEFAULT '#2563eb'`,
		`ALTER TABLE app_settings ADD COLUMN IF NOT EXISTS widget_greeting TEXT NOT NULL DEFAULT 'Здравствуйте! Чем мы можем помочь?'`,
		`INSERT INTO app_settings(tenant_id,id) VALUES ('default',TRUE) ON CONFLICT (tenant_id,id) DO NOTHING`,
	} {
		if _, err := pool.Exec(ctx, statement); err != nil {
			return fmt.Errorf("apply security migration: %w", err)
		}
	}
	if _, err := pool.Exec(ctx, `ALTER TABLE tickets ADD COLUMN IF NOT EXISTS department VARCHAR(100) DEFAULT 'Unassigned'`); err != nil {
		return fmt.Errorf("add department column: %w", err)
	}
	if _, err := pool.Exec(ctx, `ALTER TABLE tickets ADD COLUMN IF NOT EXISTS operator_reply TEXT`); err != nil {
		return fmt.Errorf("add operator reply column: %w", err)
	}
	for _, column := range []string{
		`ALTER TABLE tickets ADD COLUMN IF NOT EXISTS channel VARCHAR(50) DEFAULT 'web'`,
		`ALTER TABLE tickets ADD COLUMN IF NOT EXISTS subject TEXT`,
		`ALTER TABLE tickets ADD COLUMN IF NOT EXISTS callback_url TEXT`,
	} {
		if _, err := pool.Exec(ctx, column); err != nil {
			return fmt.Errorf("add omnichannel column: %w", err)
		}
	}
	if _, err := pool.Exec(ctx, `CREATE TABLE IF NOT EXISTS workspace_members (
 id TEXT PRIMARY KEY,
 tenant_id TEXT NOT NULL REFERENCES workspaces(tenant_id) ON DELETE CASCADE,
 display_name TEXT NOT NULL,
 email TEXT NOT NULL,
 role TEXT NOT NULL CHECK (role IN ('admin','operator','analyst','employee')),
 token_hash TEXT NOT NULL UNIQUE,
 is_active BOOLEAN NOT NULL DEFAULT TRUE,
 created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
 UNIQUE(tenant_id,email)
);
ALTER TABLE tickets ADD COLUMN IF NOT EXISTS requester_id TEXT NOT NULL DEFAULT '';
CREATE INDEX IF NOT EXISTS tickets_requester ON tickets(tenant_id,requester_id,created_at DESC);
`); err != nil {
		return fmt.Errorf("apply role migration: %w", err)
	}
	if _, err := pool.Exec(ctx, `ALTER TABLE workspace_members ADD COLUMN IF NOT EXISTS password_hash TEXT NOT NULL DEFAULT '';
ALTER TABLE workspace_members ADD COLUMN IF NOT EXISTS auth_version BIGINT NOT NULL DEFAULT 0;
CREATE INDEX IF NOT EXISTS workspace_members_login ON workspace_members(email) WHERE is_active=TRUE;
CREATE TABLE IF NOT EXISTS auth_sessions (
 token_hash TEXT PRIMARY KEY,
 csrf_hash TEXT NOT NULL,
 member_id TEXT NOT NULL REFERENCES workspace_members(id) ON DELETE CASCADE,
 auth_version BIGINT NOT NULL DEFAULT 0,
 expires_at TIMESTAMPTZ NOT NULL,
 created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS auth_sessions_member ON auth_sessions(member_id);
ALTER TABLE auth_sessions ADD COLUMN IF NOT EXISTS auth_version BIGINT NOT NULL DEFAULT 0;
CREATE INDEX IF NOT EXISTS auth_sessions_expiry ON auth_sessions(expires_at);`); err != nil {
		return fmt.Errorf("apply password migration: %w", err)
	}
	if _, err := pool.Exec(ctx, security.BotLinkSchema); err != nil {
		return fmt.Errorf("apply bot link migration: %w", err)
	}
	if _, err := pool.Exec(ctx, `CREATE INDEX IF NOT EXISTS tickets_tenant_cursor ON tickets(tenant_id,id DESC); CREATE INDEX IF NOT EXISTS tickets_requester_cursor ON tickets(tenant_id,requester_id,id DESC);`); err != nil {
		return fmt.Errorf("create ticket pagination indexes: %w", err)
	}
	if _, err := pool.Exec(ctx, WorkSchema); err != nil {
		return fmt.Errorf("apply operator work migration: %w", err)
	}
	if _, err := pool.Exec(ctx, IdempotencySchema); err != nil {
		return fmt.Errorf("apply request idempotency migration: %w", err)
	}
	if _, err := pool.Exec(ctx, ConversationsSchema); err != nil {
		return fmt.Errorf("apply conversation migration: %w", err)
	}
	if _, err := pool.Exec(ctx, PlatformSchema); err != nil {
		return fmt.Errorf("apply platform settings: %w", err)
	}
	if _, err := pool.Exec(ctx, ProcessSchema); err != nil {
		return fmt.Errorf("apply process events: %w", err)
	}
	if _, err := pool.Exec(ctx, DashboardSchema); err != nil {
		return fmt.Errorf("apply dashboards: %w", err)
	}
	return security.ScrubStoredChannelErrors(ctx, pool)
}

// SyncDefaultWorkspace makes the configured legacy operator token an ordinary
// workspace credential while retaining its stable "default" tenant identity.
func SyncDefaultWorkspace(ctx context.Context, pool *pgxpool.Pool, token string) error {
	if strings.TrimSpace(token) == "" {
		return nil
	}
	hash := sha256.Sum256([]byte(token))
	_, err := pool.Exec(ctx, `INSERT INTO workspaces(tenant_id,name,token_prefix,token_hash) VALUES('default','FlowOps AI', $1, $2)
	ON CONFLICT (tenant_id) DO UPDATE SET token_prefix=EXCLUDED.token_prefix,token_hash=EXCLUDED.token_hash`, tokenPrefix(token), hex.EncodeToString(hash[:]))
	if err != nil {
		return fmt.Errorf("synchronize default workspace: %w", err)
	}
	return nil
}

func tokenPrefix(token string) string {
	if len(token) > 8 {
		return token[:8]
	}
	return token
}
