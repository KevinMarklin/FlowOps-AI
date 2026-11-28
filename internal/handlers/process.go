package handlers

import (
	"context"
	"encoding/json"
	"flowops-ai/internal/security"
	"github.com/jackc/pgx/v5"
	"net/http"
	"strconv"
)

func (h *TicketsHandler) beginTicketTx(ctx context.Context) (pgx.Tx, error) {
	tx, err := h.db.Begin(ctx)
	if err != nil {
		return nil, err
	}
	actor := security.RequesterID(ctx)
	if actor == "" {
		actor = "system"
	}
	if _, err = tx.Exec(ctx, `SELECT set_config('flowops.actor',$1,TRUE)`, actor); err != nil {
		tx.Rollback(ctx)
		return nil, err
	}
	return tx, nil
}
func (h *TicketsHandler) ProcessEvents(w http.ResponseWriter, r *http.Request) {
	id, ok := ticketID(r)
	if !ok {
		writeError(w, 400, "Некорректный тикет")
		return
	}
	p, ok := security.CurrentPrincipal(r.Context())
	if !ok || !hasValue(p.Role, "admin", "operator", "employee") {
		writeError(w, 403, "Нет доступа")
		return
	}
	var exists bool
	if err := h.db.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM tickets WHERE tenant_id=$1 AND id=$2 AND ($3=FALSE OR requester_id=$4))`, security.TenantID(r.Context()), id, p.Role == "employee", p.ID).Scan(&exists); err != nil {
		writeError(w, 503, "Не удалось проверить доступ")
		return
	}
	if !exists {
		writeError(w, 404, "Тикет не найден")
		return
	}
	cursor, _ := strconv.ParseInt(r.URL.Query().Get("before"), 10, 64)
	rows, err := h.db.Query(r.Context(), `SELECT id,event_type,occurred_at,actor,payload,origin FROM ticket_events WHERE tenant_id=$1 AND ticket_id=$2 AND ($3::bigint=0 OR id<$3) ORDER BY id DESC LIMIT 101`, security.TenantID(r.Context()), id, cursor)
	if err != nil {
		writeError(w, 503, "Не удалось загрузить историю")
		return
	}
	defer rows.Close()
	items := []map[string]any{}
	for rows.Next() {
		var eventID int64
		var kind, actor, origin string
		var at any
		var raw []byte
		if err = rows.Scan(&eventID, &kind, &at, &actor, &raw, &origin); err != nil {
			break
		}
		var payload any
		if err = json.Unmarshal(raw, &payload); err != nil {
			break
		}
		items = append(items, map[string]any{"id": eventID, "type": kind, "at": at, "actor": actor, "payload": payload, "origin": origin})
	}
	if err != nil || rows.Err() != nil {
		writeError(w, 503, "Не удалось прочитать историю")
		return
	}
	more := len(items) > 100
	if more {
		items = items[:100]
	}
	writeJSON(w, 200, map[string]any{"events": items, "has_more": more})
}
