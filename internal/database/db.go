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
    status VARCHAR(50) DEFAULT 'new',
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);`

	if _, err := pool.Exec(ctx, query); err != nil {
		return fmt.Errorf("create tickets table: %w", err)
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
