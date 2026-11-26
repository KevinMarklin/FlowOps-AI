package handlers

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"flowops-ai/internal/analyzer"
	"flowops-ai/internal/knowledge"
	"flowops-ai/internal/security"
	"golang.org/x/crypto/bcrypt"
)

func TestEmailPasswordsSessionsAndRevocation(t *testing.T) {
	db := replyTestDB(t)
	ctx := context.Background()
	t.Setenv("FLOWOPS_AI_PROVIDERS", "")
	t.Setenv("FLOWOPS_COOKIE_SECURE", "false")
	digest := func(value string) string { hash := sha256.Sum256([]byte(value)); return hex.EncodeToString(hash[:]) }
	for _, company := range []string{"password-a", "password-b"} {
		if _, err := db.Exec(ctx, `INSERT INTO workspaces(tenant_id,name,token_prefix,token_hash) VALUES($1,$1,'owner',$2)`, company, digest("owner-"+company)); err != nil {
			t.Fatal(err)
		}
	}
	for _, m := range []struct{ id, tenant, email, role string }{{"emp-a", "password-a", "person@example.org", "employee"}, {"emp-b", "password-b", "person@example.org", "employee"}, {"analyst", "password-a", "analyst@example.org", "analyst"}} {
		if _, err := db.Exec(ctx, `INSERT INTO workspace_members(id,tenant_id,display_name,email,role,token_hash) VALUES($1,$2,$1,$3,$4,$5)`, m.id, m.tenant, m.email, m.role, digest("invite-"+m.id)); err != nil {
			t.Fatal(err)
		}
	}
	base, err := knowledge.Load(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	tickets := NewTicketsHandler(db, slog.New(slog.NewTextHandler(io.Discard, nil)), analyzer.New(base))
	members := security.NewWorkspaceHandler(db)
	passwords := security.NewPasswordHandler(db)
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v1/auth/login", passwords.Login)
	mux.HandleFunc("POST /api/v1/auth/activate", passwords.Activate)
	mux.HandleFunc("POST /api/v1/auth/logout", passwords.Logout)
	mux.HandleFunc("GET /api/v1/auth/me", members.Me)
	mux.HandleFunc("PATCH /api/v1/members/{id}", members.UpdateMember)
	mux.HandleFunc("POST /api/v1/members/{id}/rotate", members.RotateMember)
	mux.HandleFunc("GET /api/tickets", tickets.List)
	mux.HandleFunc("POST /api/tickets", tickets.Create)
	mux.HandleFunc("GET /api/stats", tickets.Stats)
	mux.Handle("POST /api/v1/ingest", security.ProtectIngestWithKeys(http.HandlerFunc(tickets.Ingest), "", db))
	app := security.ProtectOperatorTenants(mux, db)
	call := func(method, path string, body any, cookies []*http.Cookie, csrf, origin, bearer string, expectedMember ...string) *httptest.ResponseRecorder {
		t.Helper()
		raw, _ := json.Marshal(body)
		r := httptest.NewRequest(method, path, strings.NewReader(string(raw)))
		r.Header.Set("Content-Type", "application/json")
		if len(expectedMember) > 0 {
			r.Header.Set("X-FlowOps-Member", expectedMember[0])
		}
		for _, cookie := range cookies {
			r.AddCookie(cookie)
		}
		if csrf != "" {
			r.Header.Set("X-CSRF-Token", csrf)
		}
		if origin != "" {
			r.Header.Set("Origin", origin)
		}
		if bearer != "" {
			r.Header.Set("Authorization", "Bearer "+bearer)
		}
		w := httptest.NewRecorder()
		app.ServeHTTP(w, r)
		return w
	}
	expect := func(w *httptest.ResponseRecorder, status int) {
		t.Helper()
		if w.Code != status {
			t.Fatalf("got %d want %d: %s", w.Code, status, w.Body.String())
		}
	}
	const password = "Synthetic password 2026!"
	activate := func(code, email string) *httptest.ResponseRecorder {
		return call("POST", "/api/v1/auth/activate", map[string]string{"code": code, "email": email, "password": password}, nil, "", "", "")
	}
	csrfOf := func(cookies []*http.Cookie) string {
		for _, c := range cookies {
			if c.Name == "flowops_csrf" {
				return c.Value
			}
		}
		return ""
	}
	expect(call("POST", "/api/v1/auth/activate", map[string]string{"code": "invite-emp-a", "email": "person@example.org", "password": "short"}, nil, "", "", ""), 400)
	expect(activate("invite-emp-a", "attacker@example.org"), 401)
	activated := activate("invite-emp-a", "person@example.org")
	expect(activated, 200)
	cookies := activated.Result().Cookies()
	csrf := csrfOf(cookies)
	if csrf == "" {
		t.Fatal("missing CSRF cookie")
	}
	for _, c := range cookies {
		if c.Name == "flowops_session" && (!c.HttpOnly || c.SameSite != http.SameSiteStrictMode || c.MaxAge != 8*60*60 || c.Secure) {
			t.Fatalf("unsafe local cookie attributes: %+v", *c)
		}
	}
	if strings.Contains(activated.Body.String(), password) || strings.Contains(activated.Body.String(), "token_hash") {
		t.Fatal("credential leaked in response")
	}
	var hash string
	if db.QueryRow(ctx, `SELECT password_hash FROM workspace_members WHERE id='emp-a'`).Scan(&hash) != nil {
		t.Fatal("password not stored")
	}
	if hash == password || bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)) != nil {
		t.Fatal("password not adaptively hashed")
	}
	cost, _ := bcrypt.Cost([]byte(hash))
	if cost < 12 {
		t.Fatal("bcrypt cost too low")
	}
	expect(activate("invite-emp-a", "person@example.org"), 401)
	me := call("GET", "/api/v1/auth/me", nil, cookies, "", "", "")
	expect(me, 200)
	if !strings.Contains(me.Body.String(), `"role":"employee"`) {
		t.Fatal("wrong activated role")
	}
	body := map[string]string{"raw_text": "Не работает VPN", "user_email": "victim@example.org"}
	expect(call("POST", "/api/tickets", body, cookies, "", "", ""), 403)
	expect(call("POST", "/api/tickets", body, cookies, csrf, "https://attacker.example", ""), 403)
	created := call("POST", "/api/tickets", body, cookies, csrf, "", "")
	expect(created, 201)
	if !strings.Contains(created.Body.String(), `"user_email":"person@example.org"`) {
		t.Fatal("requester spoofed under cookie session")
	}
	expect(call("POST", "/api/v1/auth/logout", map[string]string{}, cookies, "", "", ""), 403)
	expect(call("GET", "/api/v1/auth/me", nil, cookies, "", "", ""), 200)
	expect(call("POST", "/api/v1/auth/logout", map[string]string{}, cookies, csrf, "", ""), 200)
	expect(call("GET", "/api/v1/auth/me", nil, cookies, "", "", ""), 401)
	login := func(email, pw, company string) *httptest.ResponseRecorder {
		return call("POST", "/api/v1/auth/login", map[string]string{"email": email, "password": pw, "workspace": company}, nil, "", "", "")
	}
	wrong := login("person@example.org", "wrong-password", "")
	unknown := login("nobody@example.org", "wrong-password", "")
	expect(wrong, 401)
	expect(unknown, 401)
	if wrong.Body.String() != unknown.Body.String() {
		t.Fatal("login errors disclose account existence")
	}
	signed := login("PERSON@example.org", password, "")
	expect(signed, 200)
	cookies = signed.Result().Cookies()
	csrf = csrfOf(cookies)
	expect(call("GET", "/api/v1/auth/me", nil, cookies, "", "", ""), 200)
	expect(call("PATCH", "/api/v1/members/emp-a", map[string]string{"role": "analyst"}, nil, "", "", "owner-password-a"), 200)
	expect(call("GET", "/api/stats", nil, cookies, "", "", ""), 200)
	expect(call("GET", "/api/tickets", nil, cookies, "", "", ""), 403)
	expect(call("PATCH", "/api/v1/members/emp-a", map[string]string{"role": "employee"}, nil, "", "", "owner-password-a"), 200)
	expect(activate("invite-emp-b", "person@example.org"), 200)
	choice := login("person@example.org", password, "")
	expect(choice, 409)
	if !strings.Contains(choice.Body.String(), "password-a") || !strings.Contains(choice.Body.String(), "password-b") {
		t.Fatal("missing authenticated company choice")
	}
	if strings.Contains(login("person@example.org", "wrong-password", "").Body.String(), "password-b") {
		t.Fatal("company list disclosed without password")
	}
	selected := login("person@example.org", password, "password-b")
	expect(selected, 200)
	ownB := call("GET", "/api/tickets", nil, selected.Result().Cookies(), "", "", "")
	expect(ownB, 200)
	if strings.TrimSpace(ownB.Body.String()) != "[]" {
		t.Fatal("company selection crossed tenant boundary")
	}
	rotated := call("POST", "/api/v1/members/emp-a/rotate", nil, nil, "", "", "owner-password-a")
	expect(rotated, 200)
	expect(call("GET", "/api/v1/auth/me", nil, cookies, "", "", ""), 401)
	expect(login("person@example.org", password, "password-a"), 401)
	var code struct {
		Token string `json:"token"`
	}
	_ = json.Unmarshal(rotated.Body.Bytes(), &code)
	reactivated := activate(code.Token, "person@example.org")
	expect(reactivated, 200)
	fresh := reactivated.Result().Cookies()
	expect(call("GET", "/api/v1/auth/me", nil, cookies, "", "", ""), 401)
	expect(call("GET", "/api/v1/auth/me", nil, fresh, "", "", ""), 200)
	expect(call("PATCH", "/api/v1/members/emp-a", map[string]bool{"active": false}, nil, "", "", "owner-password-a"), 200)
	expect(call("GET", "/api/v1/auth/me", nil, fresh, "", "", ""), 401)
	expect(call("PATCH", "/api/v1/members/emp-a", map[string]bool{"active": true}, nil, "", "", "owner-password-a"), 200)
	expect(call("GET", "/api/v1/auth/me", nil, fresh, "", "", ""), 401)
	owner := activate("owner-password-a", "owner@example.org")
	expect(owner, 200)
	ownerMe := call("GET", "/api/v1/auth/me", nil, owner.Result().Cookies(), "", "", "")
	expect(ownerMe, 200)
	if !strings.Contains(ownerMe.Body.String(), `"role":"admin"`) {
		t.Fatal("bootstrap owner lost admin")
	}
	adminCookies := owner.Result().Cookies()
	// Cookies are shared by browser tabs. A tab opened as an employee must not
	// silently adopt an administrator who signs in through another tab.
	changedAccount := call("GET", "/api/v1/auth/me", nil, adminCookies, "", "", "", "emp-a")
	expect(changedAccount, 409)
	if !strings.Contains(changedAccount.Body.String(), `"code":"session_changed"`) {
		t.Fatal("account switch was not identified explicitly")
	}
	expect(call("GET", "/api/stats", nil, adminCookies, "", "", "", "emp-a"), 409)
	expect(call("POST", "/api/v1/auth/logout", nil, adminCookies, csrfOf(adminCookies), "", "", "emp-a"), 409)
	var activatedOwner struct {
		User security.Principal `json:"user"`
	}
	if err := json.Unmarshal(owner.Body.Bytes(), &activatedOwner); err != nil {
		t.Fatal(err)
	}
	expect(call("GET", "/api/v1/auth/me", nil, adminCookies, "", "", "", activatedOwner.User.ID), 200)
	expect(call("POST", "/api/v1/ingest", map[string]string{"channel": "web", "sender": "synthetic@example.org", "message": "Не работает VPN"}, adminCookies, csrfOf(adminCookies), "", ""), 201)
	recovered := activate("owner-password-a", "owner@example.org")
	expect(recovered, 200)
	expect(call("GET", "/api/v1/auth/me", nil, adminCookies, "", "", ""), 401)
	expect(call("GET", "/api/v1/auth/me", nil, recovered.Result().Cookies(), "", "", ""), 200)
	// Expiry is checked server-side, not trusted to the browser's cookie age.
	if _, err = db.Exec(ctx, `UPDATE auth_sessions SET expires_at=$1`, time.Now().Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	expect(call("GET", "/api/v1/auth/me", nil, recovered.Result().Cookies(), "", "", ""), 401)
}

func TestPasswordLoginRateAndOriginBoundary(t *testing.T) {
	db := replyTestDB(t)
	h := security.NewPasswordHandler(db)
	r := httptest.NewRequest("POST", "/api/v1/auth/login", strings.NewReader(`{"email":"nobody@example.org","password":"wrong"}`))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Origin", "https://attacker.example")
	w := httptest.NewRecorder()
	h.Login(w, r)
	if w.Code != 403 {
		t.Fatal("cross-site login accepted")
	}
	for i := 0; i < 16; i++ {
		r = httptest.NewRequest("POST", "/api/v1/auth/login", strings.NewReader(`{"email":"nobody@example.org","password":"wrong"}`))
		r.Header.Set("Content-Type", "application/json")
		w = httptest.NewRecorder()
		h.Login(w, r)
	}
	if w.Code != 429 {
		t.Fatalf("no login rate limit: %d", w.Code)
	}
}
