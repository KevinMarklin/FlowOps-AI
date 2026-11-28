package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http/httptest"
	"strings"
	"testing"

	"flowops-ai/internal/analyzer"
	"flowops-ai/internal/knowledge"
	"flowops-ai/internal/models"
	"flowops-ai/internal/security"
)

func TestUnreadTicketsArePerOperatorAndDoNotAcknowledgeUnseenMessages(t *testing.T) {
	db := replyTestDB(t)
	ctx := security.WithTenantID(context.Background(), "unread-company")
	base, err := knowledge.Load(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	h := NewTicketsHandler(db, slog.New(slog.NewTextHandler(io.Discard, nil)), analyzer.New(base))
	var id int
	if err = db.QueryRow(ctx, `INSERT INTO tickets(tenant_id,requester_id,user_email,raw_text,status) VALUES('unread-company','employee','employee@example.org','Original question','assigned_to_operator') RETURNING id`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	operatorContext := func(actor, tenant, role string) context.Context {
		return security.WithPrincipal(ctx, security.Principal{ID: actor, TenantID: tenant, Role: role})
	}
	unread := func(actor string) bool {
		t.Helper()
		w := httptest.NewRecorder()
		r := httptest.NewRequest("GET", "/api/tickets", nil).WithContext(operatorContext(actor, "unread-company", "operator"))
		h.List(w, r)
		if w.Code != 200 {
			t.Fatalf("list %d: %s", w.Code, w.Body.String())
		}
		var tickets []models.Ticket
		if err := json.Unmarshal(w.Body.Bytes(), &tickets); err != nil {
			t.Fatal(err)
		}
		if len(tickets) != 1 {
			t.Fatal("unexpected list length", len(tickets))
		}
		return tickets[0].UnreadEmployeeMessage
	}
	mark := func(actor, tenant, role string, last int64) int {
		t.Helper()
		w := httptest.NewRecorder()
		r := httptest.NewRequest("POST", fmt.Sprintf("/api/tickets/%d/read", id), strings.NewReader(fmt.Sprintf(`{"last_message_id":%d}`, last))).WithContext(operatorContext(actor, tenant, role))
		r.SetPathValue("id", fmt.Sprint(id))
		h.MarkRead(w, r)
		return w.Code
	}
	if !unread("operator-a") {
		t.Fatal("new ticket lacks indicator")
	}
	thread, err := h.readDialogue(ctx, id, dialogueActor{id: "operator-a", operator: true}, 0)
	if err != nil {
		t.Fatal(err)
	}
	first := thread.Messages[len(thread.Messages)-1].ID
	if mark("operator-a", "unread-company", "operator", first) != 200 {
		t.Fatal("read failed")
	}
	if unread("operator-a") || !unread("operator-b") {
		t.Fatal("reading state crossed operators")
	}
	if _, err = h.appendDialogue(ctx, id, dialogueActor{id: "employee"}, "Additional information", "new-message", false); err != nil {
		t.Fatal(err)
	}
	if !unread("operator-a") {
		t.Fatal("new employee message lacks indicator")
	}
	if mark("operator-a", "unread-company", "operator", first) != 200 || !unread("operator-a") {
		t.Fatal("stale read cleared an unseen message")
	}
	thread, err = h.readDialogue(ctx, id, dialogueActor{id: "operator-a", operator: true}, 0)
	if err != nil {
		t.Fatal(err)
	}
	latest := thread.Messages[len(thread.Messages)-1].ID
	if mark("operator-a", "unread-company", "operator", latest) != 200 || unread("operator-a") {
		t.Fatal("latest read did not clear indicator")
	}
	if mark("operator-a", "unread-company", "operator", first) != 200 || unread("operator-a") {
		t.Fatal("older read regressed cursor")
	}
	if mark("operator-a", "other-company", "operator", latest) != 404 {
		t.Fatal("cross-tenant acknowledgment accepted")
	}
	if mark("employee", "unread-company", "employee", latest) != 403 {
		t.Fatal("employee can clear operator indicator")
	}
	if mark("operator-a", "unread-company", "operator", latest+999) != 400 {
		t.Fatal("foreign message cursor accepted")
	}
	if _, err = db.Exec(ctx, `UPDATE tickets SET status='closed' WHERE id=$1`, id); err != nil {
		t.Fatal(err)
	}
	if unread("operator-b") {
		t.Fatal("closed ticket still marked new")
	}
}
