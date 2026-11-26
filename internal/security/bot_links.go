package security

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const BotLinkSchema = `
CREATE TABLE IF NOT EXISTS bot_link_codes (
 token_hash TEXT PRIMARY KEY,
 member_id TEXT NOT NULL REFERENCES workspace_members(id) ON DELETE CASCADE,
 channel TEXT NOT NULL CHECK(channel IN ('telegram','max')),
 auth_version BIGINT NOT NULL,
 expires_at TIMESTAMPTZ NOT NULL,
 UNIQUE(member_id,channel)
);
CREATE TABLE IF NOT EXISTS member_bot_links (
 bot_tenant_id TEXT NOT NULL,
 sender TEXT NOT NULL,
 channel TEXT NOT NULL CHECK(channel IN ('telegram','max')),
 member_id TEXT NOT NULL REFERENCES workspace_members(id) ON DELETE CASCADE,
 auth_version BIGINT NOT NULL,
 created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
 PRIMARY KEY(bot_tenant_id,sender),
 UNIQUE(member_id,channel)
);
ALTER TABLE tickets ADD COLUMN IF NOT EXISTS bot_tenant_id TEXT NOT NULL DEFAULT '';
`

func BotSenderChannel(sender string) string {
	for prefix, channel := range map[string]string{"tg:": "telegram", "max:": "max"} {
		if strings.HasPrefix(sender, prefix) {
			v := strings.TrimPrefix(sender, prefix)
			id, err := strconv.ParseInt(v, 10, 64)
			if err == nil && id > 0 && strconv.FormatInt(id, 10) == v {
				return channel
			}
		}
	}
	return ""
}

// BotContext resolves a verified private-chat identity, never an email supplied
// in a message. Invalidated bindings fail closed instead of becoming anonymous.
func BotContext(ctx context.Context, db *pgxpool.Pool, sender string) (context.Context, error) {
	if BotSenderChannel(sender) == "" {
		return ctx, nil
	}
	var p Principal
	var active bool
	var current, linked int64
	err := db.QueryRow(ctx, `SELECT m.id,m.tenant_id,m.display_name,m.email,m.role,m.is_active,m.auth_version,l.auth_version FROM member_bot_links l JOIN workspace_members m ON m.id=l.member_id WHERE l.bot_tenant_id=$1 AND l.sender=$2`, TenantID(ctx), sender).Scan(&p.ID, &p.TenantID, &p.Name, &p.Email, &p.Role, &active, &current, &linked)
	if errors.Is(err, pgx.ErrNoRows) {
		return ctx, nil
	}
	if err != nil {
		return ctx, err
	}
	if !active || p.Role != "employee" || current != linked {
		return ctx, errors.New("bot account link disabled")
	}
	return WithPrincipal(ctx, p), nil
}

type BotLinksHandler struct{ db *pgxpool.Pool }

func NewBotLinksHandler(db *pgxpool.Pool) *BotLinksHandler { return &BotLinksHandler{db: db} }

func (h *BotLinksHandler) Status(w http.ResponseWriter, r *http.Request) {
	p, ok := CurrentPrincipal(r.Context())
	if !ok || p.Owner {
		ingestError(w, 403, "Войдите в персональный аккаунт")
		return
	}
	rows, err := h.db.Query(r.Context(), `SELECT l.channel,m.is_active AND m.role='employee' AND l.auth_version=m.auth_version FROM member_bot_links l JOIN workspace_members m ON m.id=l.member_id WHERE l.member_id=$1`, p.ID)
	if err != nil {
		ingestError(w, 503, "Не удалось загрузить подключения")
		return
	}
	defer rows.Close()
	links := map[string]any{"telegram": map[string]bool{"connected": false}, "max": map[string]bool{"connected": false}}
	for rows.Next() {
		var channel string
		var active bool
		if rows.Scan(&channel, &active) != nil {
			ingestError(w, 503, "Не удалось загрузить подключения")
			return
		}
		links[channel] = map[string]bool{"connected": active, "linked": true}
	}
	if rows.Err() != nil {
		ingestError(w, 503, "Не удалось загрузить подключения")
		return
	}
	roleJSON(w, 200, map[string]any{"can_connect": p.Role == "employee", "channels": links})
}

func (h *BotLinksHandler) Change(w http.ResponseWriter, r *http.Request) {
	p, ok := CurrentPrincipal(r.Context())
	channel := r.PathValue("channel")
	if !ok || p.Owner || (channel != "telegram" && channel != "max") {
		ingestError(w, 403, "Подключение недоступно")
		return
	}
	tx, err := h.db.Begin(r.Context())
	if err != nil {
		ingestError(w, 503, "Не удалось обновить подключение")
		return
	}
	defer tx.Rollback(r.Context())
	var version int64
	var role string
	if tx.QueryRow(r.Context(), `SELECT auth_version,role FROM workspace_members WHERE id=$1 AND tenant_id=$2 AND is_active=TRUE FOR UPDATE`, p.ID, p.TenantID).Scan(&version, &role) != nil {
		ingestError(w, 403, "Аккаунт недоступен")
		return
	}
	if _, err = tx.Exec(r.Context(), `DELETE FROM bot_link_codes WHERE member_id=$1 AND channel=$2`, p.ID, channel); err != nil {
		ingestError(w, 503, "Не удалось обновить код")
		return
	}
	if r.Method == http.MethodDelete {
		if _, err = tx.Exec(r.Context(), `DELETE FROM member_bot_links WHERE member_id=$1 AND channel=$2`, p.ID, channel); err != nil {
			ingestError(w, 503, "Не удалось отключить аккаунт")
			return
		}
		if tx.Commit(r.Context()) != nil {
			ingestError(w, 503, "Не удалось отключить аккаунт")
			return
		}
		roleJSON(w, 200, map[string]bool{"disconnected": true})
		return
	}
	if role != "employee" {
		ingestError(w, 403, "Привязка бота доступна сотрудникам")
		return
	}
	secret, err := randomCredential()
	if err != nil {
		ingestError(w, 500, "Не удалось создать код")
		return
	}
	code := "link_" + secret
	expires := time.Now().Add(10 * time.Minute)
	if _, err = tx.Exec(r.Context(), `INSERT INTO bot_link_codes(token_hash,member_id,channel,auth_version,expires_at) VALUES($1,$2,$3,$4,$5)`, digest(code), p.ID, channel, version, expires); err != nil {
		ingestError(w, 503, "Не удалось создать код")
		return
	}
	if tx.Commit(r.Context()) != nil {
		ingestError(w, 503, "Не удалось создать код")
		return
	}
	roleJSON(w, 201, map[string]any{"code": code, "command": "/link " + code, "expires_at": expires})
}

func LinkBotAccount(ctx context.Context, db *pgxpool.Pool, sender, code string) error {
	channel := BotSenderChannel(sender)
	if channel == "" || len(code) != 48 || !strings.HasPrefix(code, "link_") {
		return errors.New("invalid bot link code")
	}
	tx, err := db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var memberID, tenant, role string
	var version int64
	var active bool
	// Lock the member before consuming the code; issuance, unlink and reset all
	// serialize on this identity. A code is only valid for one channel/version.
	if tx.QueryRow(ctx, `SELECT m.id,m.tenant_id,m.role,m.auth_version,m.is_active FROM bot_link_codes c JOIN workspace_members m ON m.id=c.member_id WHERE c.token_hash=$1 AND c.channel=$2 AND c.expires_at>NOW() AND c.auth_version=m.auth_version FOR UPDATE OF m`, digest(code), channel).Scan(&memberID, &tenant, &role, &version, &active) != nil {
		return errors.New("invalid bot link code")
	}
	botTenant := TenantID(ctx)
	if !active || role != "employee" || (botTenant != "default" && botTenant != tenant) {
		return errors.New("bot link not allowed")
	}
	if _, err = tx.Exec(ctx, `INSERT INTO member_bot_links(bot_tenant_id,sender,channel,member_id,auth_version) VALUES($1,$2,$3,$4,$5)`, botTenant, sender, channel, memberID, version); err != nil {
		return errors.New("bot or account already linked")
	}
	tag, err := tx.Exec(ctx, `DELETE FROM bot_link_codes WHERE token_hash=$1 AND expires_at>NOW()`, digest(code))
	if err != nil || tag.RowsAffected() != 1 {
		return errors.New("bot link code already used")
	}
	// Only claim anonymous history already belonging to this same company/chat.
	if _, err = tx.Exec(ctx, `UPDATE tickets SET requester_id=$1 WHERE tenant_id=$2 AND channel='bot' AND user_email=$3 AND requester_id=''`, memberID, tenant, sender); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO audit_logs(tenant_id,event_type,actor,details) VALUES($1,'BOT_ACCOUNT_LINKED',$2,$3)`, tenant, memberID, "channel="+channel); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
