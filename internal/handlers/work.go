package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"

	"flowops-ai/internal/security"
	"github.com/jackc/pgx/v5"
)

// Each visible, focused work heartbeat confirms at most thirty seconds. Client
// supplied durations are never accepted; gaps and background tabs are not time.
func (h *TicketsHandler) Work(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()
	r = r.WithContext(ctx)
	p, ok := security.CurrentPrincipal(r.Context())
	if !ok || (p.Role != "admin" && p.Role != "operator") || p.ID == "" {
		writeError(w, 403, "Учёт времени доступен операторам")
		return
	}
	id, ok := ticketID(r)
	if !ok {
		writeError(w, 400, "invalid ticket id")
		return
	}
	var request struct {
		Action string `json:"action"`
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil || (request.Action != "start" && request.Action != "heartbeat" && request.Action != "pause") {
		writeError(w, 400, "invalid work action")
		return
	}
	if decoder.Decode(&struct{}{}) != io.EOF {
		writeError(w, 400, "invalid JSON body")
		return
	}
	tx, err := h.db.Begin(r.Context())
	if err != nil {
		writeError(w, 503, "Не удалось учесть время")
		return
	}
	defer tx.Rollback(r.Context())
	// Serialize this operator's tabs so one person never runs two timers.
	if _, err = tx.Exec(r.Context(), `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, p.TenantID+":"+p.ID); err != nil {
		writeError(w, 503, "Не удалось учесть время")
		return
	}
	var status string
	err = tx.QueryRow(r.Context(), `SELECT status FROM tickets WHERE tenant_id=$1 AND id=$2 FOR UPDATE`, p.TenantID, id).Scan(&status)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, 404, "Обращение не найдено")
		return
	}
	if err != nil {
		writeError(w, 503, "Не удалось загрузить обращение")
		return
	}
	if request.Action != "pause" && (status == "closed" || status == "auto_resolved") {
		writeError(w, 409, "Обращение уже завершено")
		return
	}
	if request.Action == "start" && (status == "new" || status == "awaiting_confirmation") {
		if _, err = tx.Exec(r.Context(), `UPDATE tickets SET status='assigned_to_operator' WHERE tenant_id=$1 AND id=$2`, p.TenantID, id); err != nil {
			writeError(w, 503, "Не удалось взять обращение в работу")
			return
		}
		status = "assigned_to_operator"
	}
	if request.Action == "start" {
		if _, err = tx.Exec(r.Context(), `UPDATE ticket_work_sessions SET active_seconds=active_seconds+LEAST(30,GREATEST(0,EXTRACT(EPOCH FROM NOW()-last_seen_at))),running=FALSE,last_seen_at=NOW() WHERE tenant_id=$1 AND operator_id=$2 AND running=TRUE`, p.TenantID, p.ID); err != nil {
			writeError(w, 503, "Не удалось остановить предыдущий таймер")
			return
		}
		_, err = tx.Exec(r.Context(), `INSERT INTO ticket_work_sessions(tenant_id,operator_id,ticket_id,running) VALUES($1,$2,$3,TRUE) ON CONFLICT(tenant_id,operator_id,ticket_id) DO UPDATE SET running=TRUE,last_seen_at=NOW()`, p.TenantID, p.ID, id)
	} else {
		result, updateErr := tx.Exec(r.Context(), `UPDATE ticket_work_sessions SET active_seconds=active_seconds+CASE WHEN running THEN LEAST(30,GREATEST(0,EXTRACT(EPOCH FROM NOW()-last_seen_at))) ELSE 0 END,running=CASE WHEN $4='pause' THEN FALSE ELSE running END,last_seen_at=NOW() WHERE tenant_id=$1 AND operator_id=$2 AND ticket_id=$3 AND ($4='pause' OR running=TRUE)`, p.TenantID, p.ID, id, request.Action)
		err = updateErr
		if err == nil && result.RowsAffected() == 0 && request.Action == "heartbeat" {
			writeError(w, 409, "Таймер приостановлен в другой вкладке")
			return
		}
	}
	if err != nil {
		writeError(w, 503, "Не удалось учесть время")
		return
	}
	var seconds float64
	var running bool
	err = tx.QueryRow(r.Context(), `SELECT COALESCE(SUM(active_seconds),0),COALESCE(BOOL_OR(running),FALSE) FROM ticket_work_sessions WHERE tenant_id=$1 AND operator_id=$2 AND ticket_id=$3`, p.TenantID, p.ID, id).Scan(&seconds, &running)
	if err != nil || tx.Commit(r.Context()) != nil {
		writeError(w, 503, "Не удалось сохранить время")
		return
	}
	writeJSON(w, 200, map[string]any{"ticket_id": id, "active_seconds": seconds, "running": running, "heartbeat_seconds": 15, "status": status})
}
