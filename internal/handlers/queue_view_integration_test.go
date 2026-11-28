package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"flowops-ai/internal/analyzer"
	"flowops-ai/internal/knowledge"
	"flowops-ai/internal/models"
	"flowops-ai/internal/security"
)

func TestPersonalQueueClearKeepsDataAndSearchFindsHiddenTickets(t *testing.T) {
	db := replyTestDB(t)
	ctx := security.WithTenantID(context.Background(), "personal-company")
	base, err := knowledge.Load(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err = base.Add("IT", "Памятка", []string{"оборудование"}, "Проверьте подключение."); err != nil {
		t.Fatal(err)
	}
	h := NewTicketsHandler(db, slog.New(slog.NewTextHandler(io.Discard, nil)), analyzer.New(base))
	actor := func(id, role string) context.Context {
		return security.WithPrincipal(ctx, security.Principal{ID: id, Role: role, TenantID: "personal-company"})
	}
	insert := func(tenant, owner, text string) int {
		t.Helper()
		var id int
		if err := db.QueryRow(ctx, `INSERT INTO tickets(tenant_id,requester_id,user_email,raw_text,status) VALUES($1,$2,'fixture@example.org',$3,'assigned_to_operator') RETURNING id`, tenant, owner, text).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	first := insert("personal-company", "employee", "Запрос выписки выданных документов за ноябрь")
	second := insert("personal-company", "employee", "НЕИСПРАВНА МЫШЬ")
	insert("other-company", "other-employee", "Секретная заявка другой компании")
	list := func(context context.Context, query string) []models.Ticket {
		t.Helper()
		w := httptest.NewRecorder()
		r := httptest.NewRequest("GET", "/api/tickets?limit=1&q="+url.QueryEscape(query), nil).WithContext(context)
		h.List(w, r)
		if w.Code != 200 {
			t.Fatalf("list: %d %s", w.Code, w.Body.String())
		}
		var result []models.Ticket
		if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		return result
	}
	clear := func(context context.Context) int {
		t.Helper()
		w := httptest.NewRecorder()
		h.ClearQueue(w, httptest.NewRequest("POST", "/api/tickets/queue/clear", strings.NewReader("{}")).WithContext(context))
		return w.Code
	}
	opA, opB, admin, employee := actor("operator-a", "operator"), actor("operator-b", "operator"), actor("admin", "admin"), actor("employee", "employee")
	if got := list(opA, ""); len(got) != 1 || got[0].ID != second {
		t.Fatal("fixture ordering")
	}
	if clear(opA) != 200 || len(list(opA, "")) != 0 {
		t.Fatal("personal clear did not hide tickets")
	}
	for _, other := range []context.Context{opB, admin, employee} {
		if len(list(other, "")) != 1 {
			t.Fatal("another user's queue was cleared")
		}
	}
	if clear(employee) != 403 {
		t.Fatal("employee cleared operator view")
	}
	if got := list(opA, fmt.Sprintf("ID %d", first)); len(got) != 1 || got[0].ID != first {
		t.Fatal("ID search cannot find hidden ticket")
	}
	if got := list(opA, "ВЫПИСКИ ВЫДАННЫХ ДОКУМЕНТОВ"); len(got) != 1 || got[0].ID != first {
		t.Fatal("phrase search cannot find hidden older ticket")
	}
	if got := list(opA, "мышь"); len(got) != 1 || got[0].ID != second {
		t.Fatal("lowercase search cannot match uppercase Cyrillic")
	}
	for _, query := range []string{"секретная заявка другой компании", "%", "_"} {
		if len(list(opA, query)) != 0 {
			t.Fatal("search leaked tenant or interpreted literal wildcard", query)
		}
	}
	var tickets int
	if err = db.QueryRow(ctx, `SELECT COUNT(*) FROM tickets WHERE tenant_id='personal-company'`).Scan(&tickets); err != nil || tickets != 2 || len(base.Documents()) != 1 {
		t.Fatal("personal clear deleted data", err)
	}
	if _, err = h.readDialogue(ctx, second, dialogueActor{id: "operator-a", operator: true}, 0); err != nil {
		t.Fatal(err)
	}
	if len(list(opA, "")) != 0 {
		t.Fatal("importing an old initial message resurfaced hidden ticket")
	}
	if _, err = h.appendDialogue(ctx, first, dialogueActor{id: "employee"}, "Нужен список документов за весь семестр", "follow-up", false); err != nil {
		t.Fatal(err)
	}
	if got := list(opA, ""); len(got) != 1 || got[0].ID != first || !got[0].UnreadEmployeeMessage {
		t.Fatal("new employee message did not resurface hidden ticket")
	}
	if clear(opA) != 200 || len(list(opA, "")) != 0 {
		t.Fatal("second clear failed")
	}
	if got := list(opA, "документов за весь семестр"); len(got) != 1 || got[0].ID != first {
		t.Fatal("search omitted dialogue text")
	}
	third := insert("personal-company", "employee", "Новое обращение после очистки")
	if got := list(opA, ""); len(got) != 1 || got[0].ID != third {
		t.Fatal("new ticket was hidden by an older clear")
	}
	if !security.AllowRequest("operator", "POST", "/api/tickets/queue/clear") || security.AllowRequest("admin", "DELETE", "/api/tickets/reset") {
		t.Fatal("queue/reset permissions incorrect")
	}
}
