package handlers

import (
	"context"
	"encoding/json"
	"flowops-ai/internal/analyzer"
	"flowops-ai/internal/delivery"
	"flowops-ai/internal/knowledge"
	"flowops-ai/internal/security"
	"fmt"
	"io"
	"log/slog"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestClosureReasonsAreValidatedPersistedAndTenantScoped(t *testing.T) {
	db := replyTestDB(t)
	ctx := security.WithPrincipal(context.Background(), security.Principal{ID: "op", Role: "operator", TenantID: "closure-a"})
	var id int
	if err := db.QueryRow(ctx, `INSERT INTO tickets(tenant_id,raw_text,status) VALUES('closure-a','Synthetic','assigned_to_operator') RETURNING id`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	base, _ := knowledge.Load(t.TempDir())
	h := NewTicketsHandler(db, slog.New(slog.NewTextHandler(io.Discard, nil)), analyzer.New(base))
	update := func(body string, tenant string) int {
		r := httptest.NewRequest("PATCH", fmt.Sprintf("/api/tickets/%d", id), strings.NewReader(body))
		r.SetPathValue("id", fmt.Sprint(id))
		r = r.WithContext(security.WithTenantID(ctx, tenant))
		w := httptest.NewRecorder()
		h.Update(w, r)
		return w.Code
	}
	for _, body := range []string{`{"status":"closed","closure_reason":"invalid"}`, `{"priority":"high","closure_reason":"solved"}`} {
		if code := update(body, "closure-a"); code != 400 {
			t.Fatalf("invalid reason accepted: %d", code)
		}
	}
	if code := update(`{"status":"closed","closure_reason":"solved"}`, "closure-b"); code != 404 {
		t.Fatalf("cross tenant close: %d", code)
	}
	tx, err := db.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err = delivery.EnqueueReply(ctx, tx, id, "bot", "tg:synthetic", "", "Synthetic reply"); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if code := update(`{"status":"closed","closure_reason":"solved"}`, "closure-a"); code != 200 {
		t.Fatalf("close: %d", code)
	}
	// A later delivery acknowledgement must not reopen an explicitly closed ticket.
	tx, err = db.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err = delivery.RefreshTicket(ctx, tx, id); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	var status string
	if err = db.QueryRow(ctx, `SELECT status FROM tickets WHERE id=$1`, id).Scan(&status); err != nil || status != "closed" {
		t.Fatalf("closed ticket reopened after delivery refresh: %s %v", status, err)
	}
	var reason, actor string
	if err := db.QueryRow(ctx, `SELECT details,actor FROM audit_logs WHERE ticket_id=$1 AND event_type='TICKET_CLOSED'`, id).Scan(&reason, &actor); err != nil || reason != "solved" || actor != "op" {
		t.Fatalf("closure audit: %s %s %v", reason, actor, err)
	}
	r := httptest.NewRequest("GET", fmt.Sprintf("/api/tickets/%d", id), nil).WithContext(ctx)
	r.SetPathValue("id", fmt.Sprint(id))
	w := httptest.NewRecorder()
	h.Detail(w, r)
	var detail struct {
		Status string `json:"status"`
		Reason string `json:"closure_reason"`
	}
	if err = json.Unmarshal(w.Body.Bytes(), &detail); err != nil || w.Code != 200 || detail.Status != "closed" || detail.Reason != "solved" {
		t.Fatalf("reopened detail lost closure: %s %v", w.Body.String(), err)
	}
	// Repeated closes count the current ticket once and use its latest recorded reason.
	if code := update(`{"status":"closed","closure_reason":"duplicate"}`, "closure-a"); code != 200 {
		t.Fatal(code)
	}
	for _, tenant := range []string{"closure-a", "closure-b"} {
		w := httptest.NewRecorder()
		r := httptest.NewRequest("GET", "/api/stats", nil).WithContext(security.WithTenantID(ctx, tenant))
		h.Stats(w, r)
		var stats statsResponse
		if w.Code != 200 {
			t.Fatal(w.Body.String())
		}
		if err := json.Unmarshal(w.Body.Bytes(), &stats); err != nil {
			t.Fatal(err)
		}
		if tenant == "closure-a" && (stats.ClosureReasons["duplicate"] != 1 || stats.ClosureReasons["solved"] != 0) {
			t.Fatalf("double counted: %+v", stats.ClosureReasons)
		}
		if tenant == "closure-b" && len(stats.ClosureReasons) != 0 {
			t.Fatal("leaked closure reasons")
		}
	}
}
