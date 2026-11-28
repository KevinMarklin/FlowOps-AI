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
 SELECT t.tenant_id,t.id,$2,COALESCE((SELECT MAX(m.id) FROM ticket_messages m WHERE m.tenant_id=t.tenant_id AND m.ticket_id=t.id AND m.role='employee'),0) FROM tickets t WHERE t.tenant_id=$1 AND flowops_ticket_visible(t.tenant_id,$2,t.department,t.requester_id,t.id)
 ON CONFLICT(tenant_id,ticket_id,operator_id) DO UPDATE SET last_employee_message_id=GREATEST(ticket_queue_hidden.last_employee_message_id,EXCLUDED.last_employee_message_id)`, security.TenantID(ctx), actor.id)
	if err != nil {
		writeError(w, 503, "Не удалось очистить личный список. Повторите попытку.")
		return
	}
	writeJSON(w, 200, map[string]any{"hidden_count": result.RowsAffected()})
}

// UI lifecycle is separate from routing and automated-answer states.
const ticketDisplayStatusSQL = `CASE WHEN t.status IN ('closed','auto_resolved') THEN 'closed'
 WHEN t.workflow_status<>'' THEN CASE WHEN t.status='new' THEN 'new' ELSE 'in_progress' END
 WHEN NULLIF(BTRIM(t.operator_reply),'') IS NOT NULL
 OR EXISTS(SELECT 1 FROM ticket_messages m WHERE m.tenant_id=t.tenant_id AND m.ticket_id=t.id AND m.role='operator')
 OR EXISTS(SELECT 1 FROM ticket_work_sessions w WHERE w.tenant_id=t.tenant_id AND w.ticket_id=t.id)
 THEN 'in_progress' ELSE 'new' END`
const ticketRequesterEmailSQL = `COALESCE((SELECT NULLIF(m.email,'') FROM workspace_members m WHERE m.tenant_id=t.tenant_id AND m.id=t.requester_id),'')`
