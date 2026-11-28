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
	"testing"
	"time"
)

func TestAnalyticsFilteredMetricsMissingDataAndPermissions(t *testing.T) {
	db := replyTestDB(t)
	ctx := context.Background()
	base, _ := knowledge.Load(t.TempDir())
	h := NewTicketsHandler(db, slog.New(slog.NewTextHandler(io.Discard, nil)), analyzer.New(base))
	created := time.Now().UTC().Add(-24 * time.Hour)
	_, err := db.Exec(ctx, `INSERT INTO app_settings(tenant_id,platform_config) VALUES('analytics-a','{"timezone":"Europe/Moscow","sla":{"clock":"calendar","first_response_minutes":60,"resolution_minutes":60}}')`)
	if err != nil {
		t.Fatal(err)
	}
	var id int
	err = db.QueryRow(ctx, `INSERT INTO tickets(tenant_id,user_email,raw_text,department,created_at,status) VALUES('analytics-a','x@example.org','Synthetic','IT',$1,'assigned_to_operator') RETURNING id`, created).Scan(&id)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(ctx, `INSERT INTO ticket_messages(tenant_id,ticket_id,role,body,source_key,created_at) VALUES('analytics-a',$1,'operator','Reply','test',$2)`, id, created.Add(30*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(ctx, `UPDATE tickets SET status='closed',operator_reply='Reply' WHERE id=$1`, id); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(ctx, `UPDATE ticket_events SET occurred_at=$2 WHERE ticket_id=$1 AND event_type='closed'`, id, created.Add(2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(ctx, `INSERT INTO tickets(tenant_id,user_email,raw_text,department,status,created_at) VALUES('analytics-a','x@example.org','Other','HR','new',$1),('analytics-b','private@example.org','Private','IT','closed',$1)`, created); err != nil {
		t.Fatal(err)
	}
	call := func(role, query string) (int, AnalyticsReport, string) {
		r := httptest.NewRequest("GET", "/api/analytics/explore"+query, nil).WithContext(security.WithPrincipal(ctx, security.Principal{ID: role, Role: role, TenantID: "analytics-a"}))
		w := httptest.NewRecorder()
		h.Analytics(w, r)
		var report AnalyticsReport
		if w.Code == 200 {
			json.Unmarshal(w.Body.Bytes(), &report)
		}
		return w.Code, report, w.Body.String()
	}
	code, r, body := call("admin", "?department=IT")
	if code != 200 {
		t.Fatal(body)
	}
	expect := func(key string, want float64) {
		t.Helper()
		m := r.Metrics[key]
		if m.Value == nil || *m.Value != want {
			t.Fatalf("%s %+v want %v", key, m, want)
		}
	}
	expect("total", 1)
	expect("resolution_mean", 7200)
	expect("first_response_mean", 1800)
	expect("sla_compliance", 0)
	expect("first_contact_resolution", 100)
	if r.Metrics["ai_cost"].Value != nil {
		t.Fatal("fabricated AI cost")
	}
	if len(r.Tickets) != 1 || r.Tickets[0].ID != id {
		t.Fatal("bad drilldown", r.Tickets)
	}
	code, r, body = call("analyst", "?department=IT")
	if code != 200 || len(r.Tickets) != 0 {
		t.Fatal("analyst content leak", body)
	}
	if code, _, _ = call("employee", ""); code != 403 {
		t.Fatal("employee analytics")
	}
	if code, _, _ = call("admin", "?organization=analytics-b"); code != 400 {
		t.Fatal("cross tenant filter")
	}
	code, r, body = call("admin", "?department=empty")
	if code != 200 || r.Metrics["deflection"].Value != nil || r.Metrics["resolution_mean"].Value != nil {
		t.Fatal("empty data fabricated", body)
	}
	code, _, body = call("admin", "?format=csv")
	if code != 200 || len(body) < 20 {
		t.Fatal("CSV failed")
	}
}
