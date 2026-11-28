package handlers

import (
	"context"
	"encoding/json"
	"flowops-ai/internal/security"
	"flowops-ai/internal/settings"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestOrganizationConfigIsolationRevisionAndValidation(t *testing.T) {
	db := replyTestDB(t)
	h := NewIntegrationsHandler(db)
	ctx := context.Background()
	for _, tenant := range []string{"org-a", "org-b"} {
		if _, err := db.Exec(ctx, `INSERT INTO workspaces(tenant_id,name,token_prefix,token_hash) VALUES($1,$1,'x',$1)`, tenant); err != nil {
			t.Fatal(err)
		}
	}
	call := func(role, tenant, method, body string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		r := httptest.NewRequest(method, "/api/v1/settings/organization", strings.NewReader(body)).WithContext(security.WithPrincipal(ctx, security.Principal{ID: "admin", Role: role, TenantID: tenant}))
		h.OrganizationConfig(w, r)
		return w
	}
	c := settings.DefaultPlatformConfig()
	c.Name = "New name"
	c.Economics.HourlyRate = 700
	c.ClosureReasons = []settings.Choice{{ID: "custom", Label: "Custom reason"}}
	raw, _ := json.Marshal(c)
	if w := call("employee", "org-a", "PUT", string(raw)); w.Code != 403 {
		t.Fatal(w.Code)
	}
	if w := call("admin", "org-a", "PUT", string(raw)); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	if w := call("admin", "org-a", "PUT", string(raw)); w.Code != 409 {
		t.Fatal("lost update", w.Code)
	}
	for _, tenant := range []string{"org-a", "org-b"} {
		w := call("admin", tenant, "GET", "")
		if w.Code != 200 {
			t.Fatal(w.Body.String())
		}
		var got settings.PlatformConfig
		json.Unmarshal(w.Body.Bytes(), &got)
		if tenant == "org-a" && (got.Name != "New name" || got.Revision != 1 || got.Economics.HourlyRate != 700) {
			t.Fatal(got)
		}
		if tenant == "org-b" && (got.Name != "org-b" || got.Revision != 0 || got.Economics.HourlyRate != 0) {
			t.Fatal("tenant leak", got)
		}
	}
	c.Revision = 1
	c.Timezone = "bad-zone"
	raw, _ = json.Marshal(c)
	if w := call("admin", "org-a", "PUT", string(raw)); w.Code != 400 {
		t.Fatal(w.Code)
	}
	var count int
	if err := db.QueryRow(ctx, `SELECT count(*) FROM audit_logs WHERE tenant_id='org-a' AND event_type='ORGANIZATION_SETTINGS_UPDATED'`).Scan(&count); err != nil || count != 1 {
		t.Fatal(fmt.Sprint(count, err))
	}
}
