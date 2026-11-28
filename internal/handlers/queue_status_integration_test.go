package handlers

import (
	"context"
	"encoding/json"
	"flowops-ai/internal/analyzer"
	"flowops-ai/internal/knowledge"
	"flowops-ai/internal/security"
	"fmt"
	"io"
	"log/slog"
	"net/http/httptest"
	"testing"
)

func TestQueueStatusAndLinkedRequesterEmail(t *testing.T) {
	db := replyTestDB(t)
	ctx := security.WithPrincipal(context.Background(), security.Principal{ID: "op", Role: "operator", TenantID: "queue-status"})
	base, _ := knowledge.Load(t.TempDir())
	h := NewTicketsHandler(db, slog.New(slog.NewTextHandler(io.Discard, nil)), analyzer.New(base))
	_, _ = db.Exec(ctx, `INSERT INTO workspaces(tenant_id,name,token_prefix,token_hash) VALUES('queue-status','Synthetic','x','queue-status-hash')`)
	_, err := db.Exec(ctx, `INSERT INTO workspace_members(id,tenant_id,display_name,email,role,token_hash) VALUES('linked-member','queue-status','Employee','linked@example.org','employee','synthetic-hash')`)
	if err != nil {
		t.Fatal(err)
	}
	var id int
	err = db.QueryRow(ctx, `INSERT INTO tickets(tenant_id,requester_id,user_email,raw_text,status) VALUES('queue-status','linked-member','tg:123456','Synthetic','assigned_to_operator') RETURNING id`).Scan(&id)
	if err != nil {
		t.Fatal(err)
	}
	read := func(filter string) []map[string]any {
		w := httptest.NewRecorder()
		h.List(w, httptest.NewRequest("GET", "/api/tickets?limit=1&queue_status="+filter, nil).WithContext(ctx))
		if w.Code != 200 {
			t.Fatal(w.Body.String())
		}
		var rows []map[string]any
		if err := json.Unmarshal(w.Body.Bytes(), &rows); err != nil {
			t.Fatal(err)
		}
		return rows
	}
	rows := read("new")
	if len(rows) != 1 || rows[0]["display_status"] != "new" || rows[0]["requester_email"] != "linked@example.org" {
		t.Fatalf("new bot ticket not classified with linked email: %+v", rows)
	}
	if _, err = db.Exec(ctx, `INSERT INTO tickets(tenant_id,user_email,raw_text,status,operator_reply) VALUES('queue-status','other@example.org','Later','assigned_to_operator','Reply')`); err != nil {
		t.Fatal(err)
	}
	if rows = read("new"); len(rows) != 1 || rows[0]["id"] != float64(id) {
		t.Fatal("status filtered after page limit", rows)
	}
	if _, err = db.Exec(ctx, `INSERT INTO ticket_messages(tenant_id,ticket_id,role,body,source_key) VALUES('queue-status',$1,'operator','Reply','test')`, id); err != nil {
		t.Fatal(err)
	}
	if len(read("new")) != 0 {
		t.Fatal("handled ticket still new")
	}
	if len(read("in_progress")) != 1 {
		t.Fatal("in progress filter failed")
	}
	if _, err = db.Exec(ctx, `UPDATE tickets SET status='auto_resolved' WHERE id=$1`, id); err != nil {
		t.Fatal(err)
	}
	if rows = read("closed"); len(rows) != 1 || rows[0]["display_status"] != "closed" {
		t.Fatal("automatic closure missing", rows)
	}
	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", fmt.Sprintf("/api/tickets/%d", id), nil).WithContext(ctx)
	r.SetPathValue("id", fmt.Sprint(id))
	h.Detail(w, r)
	var detail map[string]any
	json.Unmarshal(w.Body.Bytes(), &detail)
	if w.Code != 200 || detail["requester_email"] != "linked@example.org" || detail["user_email"] != "tg:123456" {
		t.Fatal("detail identity lost", w.Body.String())
	}
}
