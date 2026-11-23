package database

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func NewPool(ctx context.Context, databaseURL string) (*pgxpool.Pool, error) {
	config, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		return nil, fmt.Errorf("parse database config: %w", err)
	}
	config.ConnConfig.RuntimeParams["client_encoding"] = "UTF8"

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
			created_at TIMESTAMPTZ DEFAULT NOW(),
			event_type VARCHAR(64) NOT NULL,
			actor VARCHAR(128) NOT NULL,
			ticket_id INT NULL,
			details TEXT
		)`,
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
	return nil
}
