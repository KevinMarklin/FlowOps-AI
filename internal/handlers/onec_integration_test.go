package handlers

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"flowops-ai/internal/security"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestOneCConfigPersistenceIsolationAndRead(t *testing.T) {
	db := replyTestDB(t)
	h := NewIntegrationsHandler(db)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u, p, ok := r.BasicAuth()
		if !ok || u != "reader" || p != "password" {
			t.Error("missing upstream credential")
		}
		if strings.HasSuffix(r.URL.Path, "$metadata") {
			w.Write([]byte(`<Edmx><EntitySet Name="Catalog_Test"/></Edmx>`))
		} else {
			w.Write([]byte(`{"value":[{"id":"demo"}]}`))
		}
	}))
	defer upstream.Close()
	t.Setenv("FLOWOPS_1C_ALLOWED_URLS", upstream.URL)
	t.Setenv("FLOWOPS_1C_SECRET_KEY", base64.StdEncoding.EncodeToString(make([]byte, 32)))
	principal := security.Principal{ID: "owner", TenantID: "default", Role: "admin"}
	call := func(p security.Principal, method, path, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r = r.WithContext(security.WithPrincipal(context.Background(), p))
		w := httptest.NewRecorder()
		h.OneC(w, r)
		return w
	}
	config := map[string]any{"base_url": upstream.URL, "mode": "odata", "auth": "basic", "username": "reader", "secret": "password", "health_path": ""}
	raw, _ := json.Marshal(config)
	w := call(principal, "PUT", "/api/v1/integrations/1c", string(raw))
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), "password") {
		t.Fatal("secret echoed")
	}
	var stored string
	if err := db.QueryRow(context.Background(), `SELECT secret_ciphertext FROM onec_connections WHERE tenant_id='default'`).Scan(&stored); err != nil || stored == "" || strings.Contains(stored, "password") {
		t.Fatal("not encrypted", err)
	}
	config["secret"] = ""
	raw, _ = json.Marshal(config)
	w = call(principal, "PUT", "/api/v1/integrations/1c", string(raw))
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	for _, path := range []string{"/api/v1/integrations/1c/check", "/api/v1/integrations/1c/data?resource=Catalog_Test&limit=10"} {
		method := "GET"
		if strings.HasSuffix(path, "check") {
			method = "POST"
		}
		w = call(principal, method, path, "{}")
		if w.Code != 200 {
			t.Fatal(path, w.Code, w.Body.String())
		}
	}
	other := principal
	other.TenantID = "other"
	w = call(other, "GET", "/api/v1/integrations/1c", "")
	if strings.Contains(w.Body.String(), upstream.URL) || strings.Contains(w.Body.String(), `"has_secret":true`) {
		t.Fatal("cross tenant leak", w.Body.String())
	}
	denied := principal
	denied.Role = "operator"
	if w = call(denied, "GET", "/api/v1/integrations/1c", ""); w.Code != 403 {
		t.Fatal("operator allowed")
	}
	denied = principal
	empty := []string{}
	denied.Permissions = &empty
	if w = call(denied, "GET", "/api/v1/integrations/1c", ""); w.Code != 403 {
		t.Fatal("permission ignored")
	}
	config["base_url"] = upstream.URL + "/different"
	raw, _ = json.Marshal(config)
	if w = call(principal, "PUT", "/api/v1/integrations/1c", string(raw)); w.Code != 400 {
		t.Fatal("unregistered service accepted")
	}
	config["base_url"] = upstream.URL
	config["username"] = "different"
	raw, _ = json.Marshal(config)
	if w = call(principal, "PUT", "/api/v1/integrations/1c", string(raw)); w.Code != 400 {
		t.Fatal("password carried to another account")
	}
}
