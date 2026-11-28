package handlers

import (
	"context"
	"encoding/json"
	"flowops-ai/internal/security"
	"flowops-ai/internal/settings"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestStructureCyclesTenantMembershipAndRouting(t *testing.T) {
	db := replyTestDB(t)
	ctx := context.Background()
	for _, tenant := range []string{"structure-a", "structure-b"} {
		if _, err := db.Exec(ctx, `INSERT INTO workspaces(tenant_id,name,token_prefix,token_hash) VALUES($1,$1,'x',$1)`, tenant); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(ctx, `INSERT INTO workspace_members(id,tenant_id,display_name,email,role,token_hash) VALUES('person-a','structure-a','A','a@example.org','employee','person-a'),('person-b','structure-b','B','b@example.org','employee','person-b')`); err != nil {
		t.Fatal(err)
	}
	h := NewIntegrationsHandler(db)
	call := func(tenant, role, method, id string, u OrganizationUnit) (int, OrganizationUnit, string) {
		raw, _ := json.Marshal(u)
		r := httptest.NewRequest(method, "/api/v1/organization/units", strings.NewReader(string(raw))).WithContext(security.WithPrincipal(ctx, security.Principal{ID: "admin", Role: role, TenantID: tenant}))
		if id != "" {
			r.SetPathValue("id", id)
		}
		w := httptest.NewRecorder()
		h.OrganizationUnits(w, r)
		var out OrganizationUnit
		json.Unmarshal(w.Body.Bytes(), &out)
		return w.Code, out, w.Body.String()
	}
	code, parent, body := call("structure-a", "admin", "POST", "", OrganizationUnit{Name: "Филиал", Kind: "branch", Active: true})
	if code != 201 {
		t.Fatal(body)
	}
	code, child, body := call("structure-a", "admin", "POST", "", OrganizationUnit{Name: "Поддержка сети", Kind: "workgroup", RoutingCode: "NetworkSupport", ParentID: parent.ID, Active: true, Members: []UnitMembership{{MemberID: "person-a", Relation: "manager"}}})
	if code != 201 {
		t.Fatal(body)
	}
	parent.ParentID = child.ID
	if code, _, _ = call("structure-a", "admin", "PUT", parent.ID, parent); code != 400 {
		t.Fatal("cycle accepted", code)
	}
	parent.ParentID = ""
	if code, _, _ = call("structure-b", "admin", "PUT", child.ID, child); code != 404 {
		t.Fatal("cross tenant unit")
	}
	child.Members = []UnitMembership{{MemberID: "person-b", Relation: "member"}}
	if code, _, _ = call("structure-a", "admin", "PUT", child.ID, child); code != 400 {
		t.Fatal("cross tenant member")
	}
	config, err := settings.LoadBuilderConfig(ctx, func(context.Context) ([]byte, error) {
		var raw []byte
		e := db.QueryRow(ctx, `SELECT builder_config FROM app_settings WHERE tenant_id='structure-a'`).Scan(&raw)
		return raw, e
	})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, d := range config.Departments {
		if d.Name == "NetworkSupport" {
			found = true
		}
	}
	if !found {
		t.Fatal("structure not integrated with routing")
	}
	parent.Active = false
	if code, _, body = call("structure-a", "admin", "PUT", parent.ID, parent); code != 200 {
		t.Fatal(body)
	}
	var active bool
	if err = db.QueryRow(ctx, `SELECT active FROM organization_units WHERE tenant_id='structure-a' AND id=$1`, child.ID).Scan(&active); err != nil || active {
		t.Fatal("descendant still active", err)
	}
	if code, _, _ = call("structure-a", "employee", "POST", "", OrganizationUnit{Name: "Bad", Kind: "unit", Active: true}); code != 403 {
		t.Fatal("employee structure mutation")
	}
}
