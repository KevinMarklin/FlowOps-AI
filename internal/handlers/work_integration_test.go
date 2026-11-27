package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"flowops-ai/internal/analyzer"
	"flowops-ai/internal/knowledge"
	"flowops-ai/internal/security"
)

func TestOperatorWorkLeaseIsolationAndRoutingMetrics(t *testing.T) {
	db := replyTestDB(t)
	ctx := context.Background()
	_, err := db.Exec(ctx, `INSERT INTO workspaces(tenant_id,name,token_prefix,token_hash) VALUES('work-a','Synthetic','x','work-a-hash'),('work-b','Other','x','work-b-hash')`)
	if err != nil {
		t.Fatal(err)
	}
	var ids []int
	for _, tenant := range []string{"work-a", "work-a", "work-b"} {
		var id int
		if err := db.QueryRow(ctx, `INSERT INTO tickets(tenant_id,raw_text,department,status) VALUES($1,'Synthetic','IT','assigned_to_operator') RETURNING id`, tenant).Scan(&id); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	base, _ := knowledge.Load(t.TempDir())
	h := NewTicketsHandler(db, slog.New(slog.NewTextHandler(io.Discard, nil)), analyzer.New(base))
	p := security.Principal{ID: "op-a", Role: "operator", TenantID: "work-a"}
	call := func(id int, body string, principal security.Principal) *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", fmt.Sprintf("/api/tickets/%d/work", id), strings.NewReader(body))
		r.SetPathValue("id", fmt.Sprint(id))
		r = r.WithContext(security.WithPrincipal(ctx, principal))
		w := httptest.NewRecorder()
		h.Work(w, r)
		return w
	}
	_, _ = db.Exec(ctx, `UPDATE tickets SET status='awaiting_confirmation',rag_answer='Synthetic rule' WHERE id=$1`, ids[0])
	if w := call(ids[0], `{"action":"start"}`, p); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	var taken string
	if db.QueryRow(ctx, `SELECT status FROM tickets WHERE id=$1`, ids[0]).Scan(&taken) != nil || taken != "assigned_to_operator" {
		t.Fatal("work did not claim pending ticket")
	}
	_, _ = db.Exec(ctx, `UPDATE ticket_work_sessions SET last_seen_at=NOW()-INTERVAL '2 minutes'`)
	w := call(ids[0], `{"action":"heartbeat"}`, p)
	var measurement struct {
		Seconds float64 `json:"active_seconds"`
	}
	if json.Unmarshal(w.Body.Bytes(), &measurement) != nil || w.Code != 200 || measurement.Seconds < 29.9 || measurement.Seconds > 30.1 {
		t.Fatalf("unbounded heartbeat: %s", w.Body.String())
	}
	_, _ = db.Exec(ctx, `UPDATE ticket_work_sessions SET last_seen_at=NOW()-INTERVAL '10 seconds'`)
	if call(ids[0], `{"action":"pause"}`, p).Code != 200 {
		t.Fatal("pause failed")
	}
	if call(ids[0], `{"action":"heartbeat"}`, p).Code != 409 {
		t.Fatal("heartbeat resumed paused timer")
	}
	if call(ids[2], `{"action":"start"}`, p).Code != 404 {
		t.Fatal("foreign ticket accepted")
	}
	employee := p
	employee.Role = "employee"
	if call(ids[0], `{"action":"start"}`, employee).Code != 403 || security.AllowRequest("employee", "POST", fmt.Sprintf("/api/tickets/%d/work", ids[0])) {
		t.Fatal("employee can alter measurement")
	}
	if call(ids[0], `{"action":"heartbeat","seconds":9999}`, p).Code != 400 {
		t.Fatal("client-provided time accepted")
	}
	if call(ids[0], `{"action":"start"} {"action":"heartbeat"}`, p).Code != 400 {
		t.Fatal("multiple JSON values accepted")
	}
	var wg sync.WaitGroup
	statuses := make(chan int, 2)
	for _, id := range ids[:2] {
		wg.Add(1)
		go func(id int) { defer wg.Done(); statuses <- call(id, `{"action":"start"}`, p).Code }(id)
	}
	wg.Wait()
	close(statuses)
	for status := range statuses {
		if status != 200 {
			t.Fatal("parallel start failed")
		}
	}
	var active int
	if err := db.QueryRow(ctx, `SELECT COUNT(*) FROM ticket_work_sessions WHERE tenant_id='work-a' AND running=TRUE`).Scan(&active); err != nil || active != 1 {
		t.Fatal("multiple timers active")
	}
	for _, dept := range []string{"HR", "HR", "IT"} {
		r := httptest.NewRequest("PATCH", "/api/tickets/1", strings.NewReader(fmt.Sprintf(`{"department":%q}`, dept)))
		r.SetPathValue("id", fmt.Sprint(ids[0]))
		r = r.WithContext(security.WithPrincipal(ctx, p))
		w := httptest.NewRecorder()
		h.Update(w, r)
		if w.Code != 200 {
			t.Fatal(w.Body.String())
		}
	}
	if _, err := db.Exec(ctx, `WITH added AS (INSERT INTO tickets(tenant_id,raw_text,department,status,rag_answer) VALUES('work-a','Manually reviewed','IT','auto_resolved','Synthetic rule') RETURNING id) INSERT INTO ticket_work_sessions(tenant_id,operator_id,ticket_id,active_seconds) SELECT 'work-a','op-other',id,1 FROM added`); err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("GET", "/api/stats", nil).WithContext(security.WithPrincipal(ctx, p))
	w = httptest.NewRecorder()
	h.Stats(w, r)
	var stats statsResponse
	if json.Unmarshal(w.Body.Bytes(), &stats) != nil || w.Code != 200 || stats.OperatorActiveSeconds < 39.9 || stats.RoutingCorrectedTickets != 1 || stats.RoutingCorrections != 2 || stats.AutoResolved != 0 {
		t.Fatalf("bad measured stats: %s", w.Body.String())
	}
	// Closure atomically stops every operator's timer for the ticket.
	for _, id := range ids[:2] {
		r := httptest.NewRequest("PATCH", "/api/tickets/1", strings.NewReader(`{"status":"closed"}`))
		r.SetPathValue("id", fmt.Sprint(id))
		r = r.WithContext(security.WithPrincipal(ctx, p))
		w := httptest.NewRecorder()
		h.Update(w, r)
		if w.Code != 200 {
			t.Fatal(w.Body.String())
		}
	}
	if db.QueryRow(ctx, `SELECT COUNT(*) FROM ticket_work_sessions WHERE running=TRUE`).Scan(&active) != nil || active != 0 {
		t.Fatal("closed ticket timer running")
	}
}
