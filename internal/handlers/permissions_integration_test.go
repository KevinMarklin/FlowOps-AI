package handlers

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"flowops-ai/internal/security"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
)

func TestCustomPermissionsAreEnforcedByMiddleware(t *testing.T) {
	db := replyTestDB(t)
	ctx := context.Background()
	token := "permission-regression-token"
	sum := sha256.Sum256([]byte(token))
	_, err := db.Exec(ctx, `INSERT INTO workspaces(tenant_id,name,token_prefix,token_hash) VALUES('permission-a','Permissions','x','permission-a'); INSERT INTO workspace_members(id,tenant_id,display_name,email,role,token_hash) VALUES('permission-person','permission-a','Person','permissions@example.org','operator','placeholder'); INSERT INTO organization_access_roles(tenant_id,id,name,base_role,scope,permissions) VALUES('permission-a','reader','Reader','operator','organization','["ticket_view","ticket_close"]'); INSERT INTO organization_member_roles(tenant_id,member_id,role_id) VALUES('permission-a','permission-person','reader')`)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(ctx, `UPDATE workspace_members SET token_hash=$1 WHERE id='permission-person'`, hex.EncodeToString(sum[:])); err != nil {
		t.Fatal(err)
	}
	var id int
	if err = db.QueryRow(ctx, `INSERT INTO tickets(tenant_id,raw_text,requester_id) VALUES('permission-a','Synthetic','permission-person') RETURNING id`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	protected := security.ProtectOperatorTenants(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) }), db)
	check := func(method, path, body string, want int) {
		t.Helper()
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		protected.ServeHTTP(w, r)
		if w.Code != want {
			t.Fatalf("%s %s: %d want %d %s", method, path, w.Code, want, w.Body.String())
		}
	}
	path := "/api/tickets/" + strconv.Itoa(id)
	check("GET", path, "", 200)
	check("PATCH", path, `{"status":"closed","closure_reason":"solved"}`, 200)
	check("PATCH", path, `{"operator_reply":"Changed"}`, 403)
	check("POST", path+"/messages", `{}`, 403)
	check("GET", "/api/v1/keys", "", 403)
	check("PUT", "/api/v1/widget/config", `{}`, 403)
	check("GET", "/api/analytics/explore?format=csv", "", 403)
	check("GET", "/api/v1/auth/me", "", 200)
	if _, err = db.Exec(ctx, `UPDATE organization_access_roles SET permissions='[]' WHERE tenant_id='permission-a' AND id='reader'`); err != nil {
		t.Fatal(err)
	}
	check("GET", path, "", 403)
	if _, err = db.Exec(ctx, `UPDATE organization_access_roles SET permissions='["ticket_edit"]' WHERE tenant_id='permission-a' AND id='reader'`); err != nil {
		t.Fatal(err)
	}
	h := NewTicketsHandler(db, nil, nil)
	router := http.NewServeMux()
	router.HandleFunc("POST /api/tickets/{id}/feedback", h.Feedback)
	actual := security.ProtectOperatorTenants(router, db)
	r := httptest.NewRequest("POST", path+"/feedback", strings.NewReader(`{"resolved":true}`))
	r.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	actual.ServeHTTP(w, r)
	if w.Code != 403 {
		t.Fatal("feedback bypassed close permission", w.Code, w.Body.String())
	}
	if _, err = db.Exec(ctx, `UPDATE organization_access_roles SET scope='resources',ticket_ids=jsonb_build_array($1::integer),permissions='["ticket_view"]' WHERE tenant_id='permission-a' AND id='reader'`, id); err != nil {
		t.Fatal(err)
	}
	check("GET", path, "", 200)
	var foreign int
	if err = db.QueryRow(ctx, `INSERT INTO tickets(tenant_id,raw_text,requester_id) VALUES('permission-a','Other requester','other') RETURNING id`).Scan(&foreign); err != nil {
		t.Fatal(err)
	}
	check("GET", "/api/tickets/"+strconv.Itoa(foreign), "", 404)
	r = httptest.NewRequest("POST", "/api/tickets/queue/clear", nil).WithContext(security.WithPrincipal(ctx, security.Principal{ID: "permission-person", TenantID: "permission-a", Role: "operator"}))
	w = httptest.NewRecorder()
	h.ClearQueue(w, r)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"hidden_count":1`) {
		t.Fatal("clear queue leaked hidden tickets", w.Code, w.Body.String())
	}
}
