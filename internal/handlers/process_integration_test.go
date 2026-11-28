package handlers

import (
	"context"
	"testing"
	"time"
)

func TestProcessEventsAndBusinessDeadlines(t *testing.T) {
	db := replyTestDB(t)
	ctx := context.Background()
	_, err := db.Exec(ctx, `INSERT INTO app_settings(tenant_id,platform_config) VALUES('sla-company','{"timezone":"Europe/Moscow","sla":{"clock":"business","first_response_minutes":120,"resolution_minutes":180},"calendar":{"days":[1,2,3,4,5],"start":"09:00","end":"18:00","holidays":["2026-11-30"]}}')`)
	if err != nil {
		t.Fatal(err)
	}
	var id int
	var due time.Time
	err = db.QueryRow(ctx, `INSERT INTO tickets(tenant_id,raw_text,created_at) VALUES('sla-company','Synthetic','2026-11-27 14:00:00') RETURNING id,first_response_due_at`).Scan(&id, &due)
	if err != nil {
		t.Fatal(err)
	}
	if due.UTC().Format(time.RFC3339) != "2026-12-01T07:00:00Z" {
		t.Fatal(due)
	}
	_, err = db.Exec(ctx, `UPDATE app_settings SET platform_config='{}' WHERE tenant_id='sla-company'`)
	if err != nil {
		t.Fatal(err)
	}
	var later time.Time
	if err = db.QueryRow(ctx, `SELECT first_response_due_at FROM tickets WHERE id=$1`, id).Scan(&later); err != nil || !later.Equal(due) {
		t.Fatal("retroactive SLA", later, err)
	}
	if _, err = db.Exec(ctx, `UPDATE tickets SET status='closed' WHERE id=$1`, id); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(ctx, `UPDATE tickets SET status='assigned_to_operator' WHERE id=$1`, id); err != nil {
		t.Fatal(err)
	}
	var closed, reopened int
	if err = db.QueryRow(ctx, `SELECT COUNT(*) FILTER(WHERE event_type='closed'),COUNT(*) FILTER(WHERE event_type='reopened') FROM ticket_events WHERE tenant_id='sla-company' AND ticket_id=$1`, id).Scan(&closed, &reopened); err != nil || closed != 1 || reopened != 1 {
		t.Fatal(closed, reopened, err)
	}
}
