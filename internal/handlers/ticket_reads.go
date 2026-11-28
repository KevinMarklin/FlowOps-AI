package handlers

import (
	"flowops-ai/internal/security"
	"net/http"
)

// Acknowledge the exact transcript the operator saw, never unseen concurrent messages.
func (h *TicketsHandler) MarkRead(w http.ResponseWriter, r *http.Request) {
	actor := webDialogueActor(r.Context())
	if !actor.operator || actor.id == "" {
		writeError(w, 403, "Отметка доступна только оператору")
		return
	}
	id, ok := ticketID(r)
	if !ok {
		writeError(w, 400, "Некорректный номер обращения")
		return
	}
	var input struct {
		LastMessage int64 `json:"last_message_id"`
	}
	if !decodeDialogue(w, r, &input) {
		return
	}
	if input.LastMessage < 0 {
		writeError(w, 400, "Некорректное сообщение")
		return
	}
	tx, err := h.db.Begin(r.Context())
	if err != nil {
		writeDialogueError(w, err)
		return
	}
	defer tx.Rollback(r.Context())
	ticket, err := h.lockDialogue(r.Context(), tx, id, actor)
	if err != nil {
		writeDialogueError(w, err)
		return
	}
	var valid bool
	err = tx.QueryRow(r.Context(), `SELECT ($3::bigint=0 AND NOT EXISTS(SELECT 1 FROM ticket_messages WHERE tenant_id=$1 AND ticket_id=$2)) OR EXISTS(SELECT 1 FROM ticket_messages WHERE tenant_id=$1 AND ticket_id=$2 AND id=$3)`, security.TenantID(r.Context()), id, input.LastMessage).Scan(&valid)
	if err != nil {
		writeDialogueError(w, err)
		return
	}
	if !valid {
		writeError(w, 400, "Сообщение не относится к обращению")
		return
	}
	if _, err = tx.Exec(r.Context(), `INSERT INTO ticket_operator_reads(tenant_id,ticket_id,operator_id,last_message_id) VALUES($1,$2,$3,$4) ON CONFLICT(tenant_id,ticket_id,operator_id) DO UPDATE SET last_message_id=GREATEST(ticket_operator_reads.last_message_id,EXCLUDED.last_message_id)`, security.TenantID(r.Context()), id, actor.id, input.LastMessage); err != nil {
		writeDialogueError(w, err)
		return
	}
	var unread bool
	if !dialogueClosed(ticket.Status) {
		err = tx.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM ticket_messages m JOIN ticket_operator_reads r ON r.tenant_id=m.tenant_id AND r.ticket_id=m.ticket_id AND r.operator_id=$3 WHERE m.tenant_id=$1 AND m.ticket_id=$2 AND m.role='employee' AND m.id>r.last_message_id)`, security.TenantID(r.Context()), id, actor.id).Scan(&unread)
		if err != nil {
			writeDialogueError(w, err)
			return
		}
	}
	if err = tx.Commit(r.Context()); err != nil {
		writeDialogueError(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"unread_employee_message": unread})
}
