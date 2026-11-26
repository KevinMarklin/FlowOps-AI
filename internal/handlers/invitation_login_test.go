package handlers

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"flowops-ai/internal/security"
)

func TestInvitationCodeInLoginOffersActivation(t *testing.T) {
	db := replyTestDB(t)
	ctx := context.Background()
	hash := func(value string) string { sum := sha256.Sum256([]byte(value)); return hex.EncodeToString(sum[:]) }
	code := "usr_" + strings.Repeat("test-invite-", 4)
	if _, err := db.Exec(ctx, `INSERT INTO workspaces(tenant_id,name,token_prefix,token_hash) VALUES('invite-company','Invite company','owner',$1)`, hash("owner-fixture")); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `INSERT INTO workspace_members(id,tenant_id,display_name,email,role,token_hash) VALUES('invite-person','invite-company','Employee','employee@example.org','employee',$1)`, hash(code)); err != nil {
		t.Fatal(err)
	}
	handler := security.NewPasswordHandler(db)
	call := func(email, password string) *httptest.ResponseRecorder {
		body, _ := json.Marshal(map[string]string{"email": email, "password": password})
		r := httptest.NewRequest("POST", "/api/v1/auth/login", strings.NewReader(string(body)))
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		handler.Login(w, r)
		return w
	}
	response := call("employee@example.org", code)
	var result struct {
		ActivationRequired bool `json:"activation_required"`
	}
	_ = json.Unmarshal(response.Body.Bytes(), &result)
	if response.Code != 409 || !result.ActivationRequired {
		t.Fatalf("valid invitation entered in password field did not offer activation: status=%d body=%s", response.Code, response.Body.String())
	}
	if len(response.Result().Cookies()) != 0 {
		t.Fatal("invitation must not skip password setup")
	}
	if strings.Contains(response.Body.String(), code) {
		t.Fatal("invitation echoed in response")
	}
	for _, c := range []struct{ email, password string }{{"other@example.org", code}, {"employee@example.org", "usr_" + strings.Repeat("wrong", 9)}} {
		denied := call(c.email, c.password)
		if denied.Code != 401 || strings.Contains(denied.Body.String(), "activation_required") {
			t.Fatal("wrong invitation/email disclosed activation")
		}
	}
	const password = "Regression test password with many words 2026!"
	body, _ := json.Marshal(map[string]string{"code": code, "email": "employee@example.org", "password": password})
	r := httptest.NewRequest("POST", "/api/v1/auth/activate", strings.NewReader(string(body)))
	r.Header.Set("Content-Type", "application/json")
	activated := httptest.NewRecorder()
	handler.Activate(activated, r)
	if activated.Code != 200 || len(activated.Result().Cookies()) != 2 {
		t.Fatalf("offered invitation could not activate account: status=%d", activated.Code)
	}
	if signed := call("employee@example.org", password); signed.Code != 200 {
		t.Fatalf("permanent password failed after activation: status=%d", signed.Code)
	}
	if reused := call("employee@example.org", code); reused.Code != 401 {
		t.Fatal("used invitation was accepted again")
	}
}
