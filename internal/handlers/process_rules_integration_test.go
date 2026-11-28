package handlers

import (
	"context"
	"testing"
	"time"
)

func TestRoutingPriorityAndScopedSLASnapshot(t *testing.T) {
	db := replyTestDB(t)
	ctx := context.Background()
	_, err := db.Exec(ctx, `INSERT INTO workspaces(tenant_id,name,token_prefix,token_hash) VALUES('route-a','A','x','route-a');INSERT INTO app_settings(tenant_id,platform_config) VALUES('route-a','{"timezone":"UTC","sla":{"clock":"calendar","first_response_minutes":60,"resolution_minutes":120},"process":{"routing":[{"id":"email-first","order":1,"match":{"channel":"email"},"department":"IT","priority":"high"},{"id":"catch-all","order":2,"match":{},"department":"HR"}],"sla":[{"id":"urgent-it","match":{"department":"IT","priority":"high"},"policy":{"clock":"calendar","first_response_minutes":5,"resolution_minutes":30}}]}}')`)
	if err != nil {
		t.Fatal(err)
	}
	var id int
	var department, priority string
	var due time.Time
	created := time.Now().UTC().Truncate(time.Microsecond)
	err = db.QueryRow(ctx, `INSERT INTO tickets(tenant_id,raw_text,channel,department,priority,created_at) VALUES('route-a','Synthetic','email','General','low',$1) RETURNING id,department,priority,first_response_due_at`, created).Scan(&id, &department, &priority, &due)
	if err != nil || department != "IT" || priority != "high" || due.Sub(created) != 5*time.Minute {
		t.Fatalf("routing/SLA failed: %s %s %v %v", department, priority, due, err)
	}
	if _, err = db.Exec(ctx, `UPDATE app_settings SET platform_config='{}' WHERE tenant_id='route-a'`); err != nil {
		t.Fatal(err)
	}
	var unchanged time.Time
	if err = db.QueryRow(ctx, `SELECT first_response_due_at FROM tickets WHERE id=$1`, id).Scan(&unchanged); err != nil || !unchanged.Equal(due) {
		t.Fatal("SLA snapshot changed", err)
	}
}

func TestSLAPauseAndDurableEscalation(t *testing.T) {
	db := replyTestDB(t)
	ctx := context.Background()
	_, err := db.Exec(ctx, `INSERT INTO workspaces(tenant_id,name,token_prefix,token_hash) VALUES('sla-flow','SLA','x','sla-flow'); INSERT INTO workspace_members(id,tenant_id,display_name,email,role,token_hash) VALUES('sla-manager','sla-flow','Manager','sla@example.org','operator','sla-manager');INSERT INTO app_settings(tenant_id,platform_config) VALUES('sla-flow','{"timezone":"UTC","sla":{"clock":"calendar","first_response_minutes":5,"resolution_minutes":30},"process":{"pause_statuses":["pending_confirmation"],"escalations":[{"id":"level-1","minutes_after":0,"assignee":"sla-manager"}]}}')`)
	if err != nil {
		t.Fatal(err)
	}
	var pausedID, lateID int
	var paused bool
	if err = db.QueryRow(ctx, `INSERT INTO tickets(tenant_id,raw_text,status) VALUES('sla-flow','Waiting','pending_confirmation') RETURNING id,sla_paused_at IS NOT NULL`).Scan(&pausedID, &paused); err != nil || !paused {
		t.Fatal("initial SLA pause missing", err)
	}
	if _, err = db.Exec(ctx, `UPDATE tickets SET status='assigned_to_operator' WHERE id=$1`, pausedID); err != nil {
		t.Fatal(err)
	}
	var deadlines bool
	if err = db.QueryRow(ctx, `SELECT first_response_due_at IS NOT NULL AND resolution_due_at IS NOT NULL AND sla_paused_at IS NULL FROM tickets WHERE id=$1`, pausedID).Scan(&deadlines); err != nil || !deadlines {
		t.Fatal("resume lost deadlines", err)
	}
	if err = db.QueryRow(ctx, `INSERT INTO tickets(tenant_id,raw_text,status,created_at) VALUES('sla-flow','Overdue','assigned_to_operator',(NOW() AT TIME ZONE 'UTC')-INTERVAL '2 hours') RETURNING id`).Scan(&lateID); err != nil {
		t.Fatal(err)
	}
	h := NewTicketsHandler(db, nil, nil)
	for i := 0; i < 2; i++ {
		if err = h.EvaluateSLA(ctx); err != nil {
			t.Fatal(err)
		}
	}
	var count int
	var assignee string
	if err = db.QueryRow(ctx, `SELECT COUNT(*) FROM ticket_sla_notifications WHERE tenant_id='sla-flow' AND ticket_id=$1 AND kind='breach'`, lateID).Scan(&count); err != nil || count != 1 {
		t.Fatal("duplicate escalation", count, err)
	}
	if err = db.QueryRow(ctx, `SELECT assignee_id FROM tickets WHERE id=$1`, lateID).Scan(&assignee); err != nil || assignee != "sla-manager" {
		t.Fatal("escalation assignee missing", assignee, err)
	}
}
