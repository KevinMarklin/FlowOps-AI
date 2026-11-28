package handlers

import (
	"context"
	"flowops-ai/internal/security"
	"net/http"
	"time"
)

// EvaluateSLA records durable, deduplicated inbox notifications and reassignment.
// Running more than one worker is safe: the unique event key owns each action.
func (h *TicketsHandler) EvaluateSLA(ctx context.Context) error {
	_, err := h.db.Exec(ctx, `WITH due AS(
 SELECT t.*,deadlines.phase,deadlines.deadline FROM tickets t CROSS JOIN LATERAL (VALUES('resolution',t.resolution_due_at),('first_response',t.first_response_due_at)) deadlines(phase,deadline)
 WHERE t.status NOT IN ('closed','auto_resolved') AND t.sla_paused_at IS NULL AND deadlines.deadline IS NOT NULL AND deadlines.deadline<NOW()+INTERVAL '7 days'
 AND (deadlines.phase='resolution' OR NOT EXISTS(SELECT 1 FROM ticket_events e WHERE e.tenant_id=t.tenant_id AND e.ticket_id=t.id AND e.event_type='response_available') AND NOT EXISTS(SELECT 1 FROM ticket_messages m WHERE m.tenant_id=t.tenant_id AND m.ticket_id=t.id AND m.role IN ('operator','assistant') AND m.source_key NOT IN ('legacy-reply','initial-answer')))
 ), warnings AS(
 INSERT INTO ticket_sla_notifications(tenant_id,ticket_id,kind,recipient_id)
 SELECT tenant_id,id,CASE WHEN phase='first_response' THEN 'warning_first_response' ELSE 'warning' END,assignee_id FROM due WHERE COALESCE((sla_policy_snapshot->'process'->>'warning_minutes')::INTEGER,0)>0 AND deadline>NOW() AND deadline<=NOW()+COALESCE((sla_policy_snapshot->'process'->>'warning_minutes')::INTEGER,0)*INTERVAL '1 minute'
 ON CONFLICT DO NOTHING RETURNING id
 ), events AS(
 INSERT INTO ticket_sla_notifications(tenant_id,ticket_id,kind,level_id,recipient_id)
 SELECT t.tenant_id,t.id,CASE WHEN t.phase='first_response' THEN 'breach_first_response' ELSE 'breach' END,level->>'id',COALESCE(NULLIF(level->>'assignee',''),t.assignee_id) FROM due t CROSS JOIN LATERAL jsonb_array_elements(COALESCE(NULLIF(t.sla_policy_snapshot->'process'->'escalations','null'::jsonb),'[]'::jsonb)) level
 WHERE t.deadline+COALESCE((level->>'minutes_after')::INTEGER,0)*INTERVAL '1 minute'<=NOW()
 ORDER BY COALESCE((level->>'minutes_after')::INTEGER,0),level->>'id'
 ON CONFLICT DO NOTHING RETURNING id,tenant_id,ticket_id,level_id,recipient_id
 ), assignments AS(
 UPDATE tickets t SET assignee_id=e.recipient_id FROM (SELECT DISTINCT ON(tenant_id,ticket_id) * FROM events ORDER BY tenant_id,ticket_id,id DESC) e
 WHERE t.tenant_id=e.tenant_id AND t.id=e.ticket_id AND e.recipient_id<>'' AND EXISTS(SELECT 1 FROM workspace_members m WHERE m.tenant_id=e.tenant_id AND m.id=e.recipient_id AND m.is_active AND m.role IN ('operator','admin') AND flowops_ticket_visible(m.tenant_id,m.id,t.department,t.requester_id,t.id)) RETURNING t.id
 ), journalled AS(INSERT INTO ticket_events(tenant_id,ticket_id,event_type,payload) SELECT tenant_id,ticket_id,'sla_escalated',jsonb_build_object('level_id',level_id,'recipient_id',recipient_id) FROM events RETURNING id) INSERT INTO audit_logs(tenant_id,event_type,actor,ticket_id,details) SELECT tenant_id,'SLA_ESCALATED','system',ticket_id,'level='||level_id FROM events`)
	return err
}
func (h *TicketsHandler) SLANotifications(w http.ResponseWriter, r *http.Request) {
	p, ok := security.CurrentPrincipal(r.Context())
	if !ok || p.Role != "admin" && p.Role != "operator" {
		writeError(w, 403, "Недостаточно прав")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	rows, err := h.db.Query(ctx, `SELECT n.id,n.ticket_id,n.kind,n.level_id,n.created_at FROM ticket_sla_notifications n JOIN tickets t ON t.id=n.ticket_id AND t.tenant_id=n.tenant_id WHERE n.tenant_id=$1 AND ($3 OR n.recipient_id=$2) AND flowops_ticket_visible(t.tenant_id,$2,t.department,t.requester_id,t.id) ORDER BY n.id DESC LIMIT 100`, p.TenantID, p.ID, p.Role == "admin")
	if err != nil {
		writeError(w, 503, "Не удалось загрузить уведомления")
		return
	}
	defer rows.Close()
	items := []map[string]any{}
	for rows.Next() {
		var id int64
		var ticket int
		var kind, level string
		var at time.Time
		if err = rows.Scan(&id, &ticket, &kind, &level, &at); err != nil {
			break
		}
		items = append(items, map[string]any{"id": id, "ticket_id": ticket, "kind": kind, "level_id": level, "created_at": at})
	}
	if err != nil || rows.Err() != nil {
		writeError(w, 503, "Не удалось прочитать уведомления")
		return
	}
	writeJSON(w, 200, items)
}
