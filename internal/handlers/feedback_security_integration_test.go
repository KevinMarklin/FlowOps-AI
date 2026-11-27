package handlers

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"flowops-ai/internal/security"
)

func TestHTTPFeedbackRejectsInvalidStatesAndForeignOwners(t *testing.T) {
	db := replyTestDB(t)
	h := &TicketsHandler{db: db, logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	ctx := security.WithPrincipal(context.Background(), security.Principal{ID: "employee-a", TenantID: "feedback-tenant", Role: "employee"})
	insert := func(owner, status string, rag, reply any) int {
		var id int
		if err := db.QueryRow(ctx, `INSERT INTO tickets(tenant_id,requester_id,raw_text,status,rag_answer,operator_reply) VALUES('feedback-tenant',$1,'Synthetic issue',$2,$3,$4) RETURNING id`, owner, status, rag, reply).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	post := func(id int, resolved bool) int {
		r := httptest.NewRequest("POST", fmt.Sprintf("/api/tickets/%d/feedback", id), strings.NewReader(fmt.Sprintf(`{"resolved":%t}`, resolved))).WithContext(ctx)
		r.SetPathValue("id", fmt.Sprint(id))
		w := httptest.NewRecorder()
		h.Feedback(w, r)
		return w.Code
	}
	for _, tc := range []struct {
		owner, status string
		rag, reply    any
		code          int
	}{
		{"employee-a", "assigned_to_operator", nil, nil, 409},
		{"employee-a", "closed", "Regulation", nil, 409},
		{"employee-a", "awaiting_confirmation", nil, nil, 409},
		{"employee-a", "awaiting_confirmation", "Regulation", "Operator answer", 409},
		{"employee-b", "awaiting_confirmation", "Regulation", nil, 404},
	} {
		id := insert(tc.owner, tc.status, tc.rag, tc.reply)
		if code := post(id, true); code != tc.code {
			t.Errorf("state=%s owner=%s got=%d want=%d", tc.status, tc.owner, code, tc.code)
		}
		var status string
		_ = db.QueryRow(ctx, `SELECT status FROM tickets WHERE id=$1`, id).Scan(&status)
		if status != tc.status {
			t.Error("ineligible ticket was changed")
		}
	}
	id := insert("employee-a", "awaiting_confirmation", "Regulation", nil)
	if post(id, true) != 200 || post(id, true) != 200 || post(id, false) != 409 {
		t.Fatal("feedback is not idempotent or opposite click overwrote decision")
	}
	var audits int
	if err := db.QueryRow(ctx, `SELECT COUNT(*) FROM audit_logs WHERE ticket_id=$1 AND event_type='TICKET_FEEDBACK'`, id).Scan(&audits); err != nil || audits != 1 {
		t.Fatal("feedback audit missing or duplicated")
	}
	id = insert("employee-a", "awaiting_confirmation", "Regulation", nil)
	var wg sync.WaitGroup
	codes := make(chan int, 2)
	for _, v := range []bool{true, false} {
		wg.Add(1)
		go func(v bool) { defer wg.Done(); codes <- post(id, v) }(v)
	}
	wg.Wait()
	close(codes)
	ok, conflict := 0, 0
	for code := range codes {
		if code == 200 {
			ok++
		}
		if code == 409 {
			conflict++
		}
	}
	if ok != 1 || conflict != 1 {
		t.Fatalf("opposite feedback race: success=%d conflict=%d", ok, conflict)
	}
	id = insert("employee-a", "awaiting_confirmation", "Regulation", nil)
	if _, err := db.Exec(ctx, `ALTER TABLE audit_logs ADD CONSTRAINT reject_feedback_audit CHECK(event_type<>'TICKET_FEEDBACK') NOT VALID`); err != nil {
		t.Fatal(err)
	}
	if post(id, true) != 500 {
		t.Fatal("failed audit accepted feedback")
	}
	var status string
	_ = db.QueryRow(ctx, `SELECT status FROM tickets WHERE id=$1`, id).Scan(&status)
	if status != "awaiting_confirmation" {
		t.Fatal("failed audit did not rollback")
	}
}
