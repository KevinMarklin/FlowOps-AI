package handlers

import (
	"context"
	"flowops-ai/internal/security"
	"net/http"
	"time"
)

// Hide an exact snapshot for this operator. Tickets, messages and knowledge stay intact.
func (h *TicketsHandler) ClearQueue(w http.ResponseWriter, r *http.Request) {
	actor := webDialogueActor(r.Context())
	if !actor.operator || actor.id == "" {
		writeError(w, 403, "Список доступен оператору или администратору")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	result, err := h.db.Exec(ctx, `INSERT INTO ticket_queue_hidden(tenant_id,ticket_id,operator_id,last_employee_message_id)
 SELECT t.tenant_id,t.id,$2,COALESCE((SELECT MAX(m.id) FROM ticket_messages m WHERE m.tenant_id=t.tenant_id AND m.ticket_id=t.id AND m.role='employee'),0) FROM tickets t WHERE t.tenant_id=$1
 ON CONFLICT(tenant_id,ticket_id,operator_id) DO UPDATE SET last_employee_message_id=GREATEST(ticket_queue_hidden.last_employee_message_id,EXCLUDED.last_employee_message_id)`, security.TenantID(ctx), actor.id)
	if err != nil {
		writeError(w, 503, "Не удалось очистить личный список. Повторите попытку.")
		return
	}
	writeJSON(w, 200, map[string]any{"hidden_count": result.RowsAffected()})
}
