package handlers

import (
	"context"
	"encoding/json"
	"flowops-ai/internal/security"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestDashboardOwnershipSharingAndRevision(t *testing.T) {
	db := replyTestDB(t)
	h := NewTicketsHandler(db, nil, nil)
	ctx := context.Background()
	for _, tenant := range []string{"dash-a", "dash-b"} {
		if _, err := db.Exec(ctx, `INSERT INTO workspaces(tenant_id,name,token_prefix,token_hash) VALUES($1,$1,'x',$1)`, tenant); err != nil {
			t.Fatal(err)
		}
	}
	call := func(tenant, id, role, method, path, body string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		r := httptest.NewRequest(method, "/api/analytics/dashboards", strings.NewReader(body)).WithContext(security.WithPrincipal(ctx, security.Principal{ID: id, Role: role, TenantID: tenant}))
		if path != "" {
			r.SetPathValue("id", path)
			h.ChangeDashboard(w, r)
		} else {
			h.Dashboards(w, r)
		}
		return w
	}
	d := Dashboard{Name: "Personal", Config: DashboardConfig{Widgets: []DashboardWidget{{ID: "one", Type: "kpi", Metric: "total", Dimension: "department", Measure: "total", Width: 1, Height: 1}}}}
	raw, _ := json.Marshal(d)
	w := call("dash-a", "owner", "analyst", "POST", "", string(raw))
	if w.Code != 201 {
		t.Fatal(w.Body.String())
	}
	json.Unmarshal(w.Body.Bytes(), &d)
	w = call("dash-a", "other", "analyst", "GET", "", "")
	if w.Code != 200 || strings.Contains(w.Body.String(), d.ID) {
		t.Fatal("personal dashboard leak", w.Body.String())
	}
	w = call("dash-b", "owner", "admin", "GET", "", "")
	if strings.Contains(w.Body.String(), d.ID) {
		t.Fatal("cross tenant leak")
	}
	raw, _ = json.Marshal(d)
	w = call("dash-a", "other", "admin", "PUT", d.ID, string(raw))
	if w.Code != 404 {
		t.Fatal("personal edit by another owner", w.Code)
	}
	w = call("dash-a", "owner", "analyst", "PUT", d.ID, string(raw))
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	w = call("dash-a", "owner", "analyst", "PUT", d.ID, string(raw))
	if w.Code != 409 {
		t.Fatal("lost update", w.Code)
	}
	d.Shared = true
	d.ID = ""
	raw, _ = json.Marshal(d)
	w = call("dash-a", "owner", "analyst", "POST", "", string(raw))
	if w.Code != 403 {
		t.Fatal("analyst sharing", w.Code)
	}
	w = call("dash-a", "admin", "admin", "POST", "", string(raw))
	if w.Code != 201 {
		t.Fatal(w.Body.String())
	}
	json.Unmarshal(w.Body.Bytes(), &d)
	w = call("dash-a", "other", "analyst", "GET", "", "")
	if !strings.Contains(w.Body.String(), d.ID) {
		t.Fatal("shared unavailable")
	}
}
