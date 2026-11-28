package handlers

import (
	"context"
	"encoding/json"
	"flowops-ai/internal/security"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestAccessRoleDefinitionsAreTenantBoundAndRevisionChecked(t *testing.T) {
	db := replyTestDB(t)
	ctx := context.Background()
	if _, err := db.Exec(ctx, `INSERT INTO workspaces(tenant_id,name,token_prefix,token_hash) VALUES('role-a','A','x','role-a'),('role-b','B','x','role-b')`); err != nil {
		t.Fatal(err)
	}
	h := NewIntegrationsHandler(db)
	call := func(tenant, role, body string) (int, AccessRole) {
		r := httptest.NewRequest("POST", "/api/v1/organization/roles", strings.NewReader(body)).WithContext(security.WithPrincipal(ctx, security.Principal{ID: "admin", TenantID: tenant, Role: role}))
		w := httptest.NewRecorder()
		h.AccessRoles(w, r)
		var out AccessRole
		json.Unmarshal(w.Body.Bytes(), &out)
		return w.Code, out
	}
	body := `{"id":"support","name":"Support","base_role":"operator","scope":"own","revision":0}`
	code, created := call("role-a", "admin", body)
	if code != 201 || created.Revision != 1 {
		t.Fatal("create", code, created)
	}
	if code, _ = call("role-a", "operator", body); code != 403 {
		t.Fatal("operator defined policy", code)
	}
	if code, _ = call("role-a", "admin", body); code != 409 {
		t.Fatal("duplicate accepted", code)
	}
	update := `{"id":"support","name":"Changed","base_role":"operator","scope":"organization","revision":1}`
	if code, _ = call("role-b", "admin", update); code != 409 {
		t.Fatal("cross tenant update", code)
	}
	code, updated := call("role-a", "admin", update)
	if code != 200 || updated.Revision != 2 {
		t.Fatal("update", code, updated)
	}
	if code, _ = call("role-a", "admin", update); code != 409 {
		t.Fatal("stale revision", code)
	}
}

func TestScopedRoleTicketVisibility(t *testing.T) {
	db := replyTestDB(t)
	ctx := context.Background()
	_, err := db.Exec(ctx, `INSERT INTO workspaces(tenant_id,name,token_prefix,token_hash) VALUES('scope-a','A','x','scope-a');
 INSERT INTO workspace_members(id,tenant_id,display_name,email,role,token_hash) VALUES('scope-person','scope-a','P','scope@example.org','operator','scope-token');
 INSERT INTO organization_units(tenant_id,id,name,kind,routing_code) VALUES('scope-a','root','Root','branch',''),('scope-a','child','Child','department','IT');
 UPDATE organization_units SET parent_id='root' WHERE tenant_id='scope-a' AND id='child';
 INSERT INTO organization_access_roles(tenant_id,id,name,base_role,scope,unit_ids) VALUES('scope-a','scoped','Scoped','operator','units','["root"]');
 INSERT INTO organization_member_roles(tenant_id,member_id,role_id) VALUES('scope-a','scope-person','scoped')`)
	if err != nil {
		t.Fatal(err)
	}
	check := func(department, requester string, want bool) {
		t.Helper()
		var got bool
		if err := db.QueryRow(ctx, `SELECT COALESCE(flowops_ticket_visible('scope-a','scope-person',$1,$2),false)`, department, requester).Scan(&got); err != nil || got != want {
			t.Fatalf("visibility %s %s: %v %v", department, requester, got, err)
		}
	}
	check("IT", "other", true)
	check("HR", "other", false)
	check("HR", "scope-person", true)
	if _, err = db.Exec(ctx, `UPDATE organization_access_roles SET scope='own',unit_ids='[]' WHERE tenant_id='scope-a' AND id='scoped'`); err != nil {
		t.Fatal(err)
	}
	check("IT", "other", false)
	if _, err = db.Exec(ctx, `DELETE FROM organization_member_roles WHERE tenant_id='scope-a' AND member_id='scope-person'`); err != nil {
		t.Fatal(err)
	}
	check("IT", "other", true)
}
