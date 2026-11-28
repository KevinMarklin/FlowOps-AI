package handlers

import (
	"context"
	"encoding/json"
	"flowops-ai/internal/analyzer"
	"flowops-ai/internal/knowledge"
	"flowops-ai/internal/security"
	"io"
	"log/slog"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestOptimizationEvidenceDecisionsAndTenantIsolation(t *testing.T) {
	db := replyTestDB(t)
	ctx := context.Background()
	_, err := db.Exec(ctx, `INSERT INTO workspaces(tenant_id,name,token_prefix,token_hash) VALUES('opt-a','A','x','opt-a'),('opt-b','B','x','opt-b')`)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		if _, err = db.Exec(ctx, `INSERT INTO tickets(tenant_id,user_email,raw_text,department,status,operator_reply) VALUES('opt-a','x@example.org','Printer problem','IT','closed','Restarted')`); err != nil {
			t.Fatal(err)
		}
	}
	base, _ := knowledge.Load(t.TempDir())
	h := NewTicketsHandler(db, slog.New(slog.NewTextHandler(io.Discard, nil)), analyzer.New(base))
	call := func(tenant, role, method, id, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, "/api/optimization", strings.NewReader(body)).WithContext(security.WithPrincipal(ctx, security.Principal{ID: role, Role: role, TenantID: tenant}))
		w := httptest.NewRecorder()
		if id != "" {
			r.SetPathValue("id", id)
			h.DecideOpportunity(w, r)
		} else {
			h.Opportunities(w, r)
		}
		return w
	}
	w := call("opt-a", "admin", "GET", "", "")
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	var data struct {
		Items []Opportunity `json:"items"`
	}
	json.Unmarshal(w.Body.Bytes(), &data)
	if len(data.Items) != 2 {
		t.Fatal("missing evidence", w.Body.String())
	}
	item := data.Items[0]
	if item.Count != 3 || len(item.TicketIDs) != 3 {
		t.Fatal(item)
	}
	body := `{"state":"accepted","revision":0,"tasks":[{"id":"task","title":"Review procedure","state":"todo"}]}`
	w = call("opt-a", "admin", "PUT", item.ID, body)
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	w = call("opt-a", "admin", "PUT", item.ID, body)
	if w.Code != 409 {
		t.Fatal("lost update", w.Code)
	}
	w = call("opt-b", "admin", "PUT", item.ID, body)
	if w.Code != 404 {
		t.Fatal("tenant leak", w.Code)
	}
	w = call("opt-a", "analyst", "PUT", item.ID, body)
	if w.Code != 403 {
		t.Fatal("analyst mutation")
	}
	w = call("opt-a", "analyst", "GET", "", "")
	data.Items = nil
	json.Unmarshal(w.Body.Bytes(), &data)
	for _, item := range data.Items {
		if len(item.TicketIDs) > 0 {
			t.Fatal("analyst ticket detail leak")
		}
	}
	// A real-generator seam is injected only in the test. Foreign IDs from model
	// output must not become evidence or persistent groups.
	h.structuredGenerator = func(context.Context, string, string) (string, bool, error) {
		return `{"clusters":[{"title":"Wrong source","description":"Test","ticket_ids":[99991,99992,99993]}]}`, true, nil
	}
	sr := httptest.NewRequest("POST", "/api/optimization/semantic", strings.NewReader("{}")).WithContext(security.WithPrincipal(ctx, security.Principal{ID: "admin", Role: "admin", TenantID: "opt-a"}))
	sw := httptest.NewRecorder()
	h.SemanticOpportunities(sw, sr)
	if sw.Code != 502 {
		t.Fatal("unvalidated model IDs", sw.Code, sw.Body.String())
	}
	var stored int
	if err = db.QueryRow(ctx, `SELECT COUNT(*) FROM optimization_semantic_clusters WHERE tenant_id='opt-a'`).Scan(&stored); err != nil || stored != 0 {
		t.Fatal("invalid semantic group stored", stored, err)
	}

}
