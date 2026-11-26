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
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"

	"flowops-ai/internal/analyzer"
	"flowops-ai/internal/knowledge"
	"flowops-ai/internal/models"
	"flowops-ai/internal/security"
)

func TestRolesTenantIsolationAndRevocation(t *testing.T) {
	db := replyTestDB(t)
	ctx := context.Background()
	t.Setenv("FLOWOPS_AI_PROVIDERS", "")
	hash := func(token string) string { sum := sha256.Sum256([]byte(token)); return hex.EncodeToString(sum[:]) }
	for _, tenant := range []string{"company-a", "company-b"} {
		if _, err := db.Exec(ctx, `INSERT INTO workspaces(tenant_id,name,token_prefix,token_hash) VALUES($1,$1,'owner',$2)`, tenant, hash("owner-"+tenant)); err != nil {
			t.Fatal(err)
		}
	}
	for _, role := range []string{"employee", "operator", "analyst", "admin"} {
		if _, err := db.Exec(ctx, `INSERT INTO workspace_members(id,tenant_id,display_name,email,role,token_hash) VALUES($1,'company-a',$1,$2,$3,$4)`, role, role+"@example.org", role, hash("token-"+role)); err != nil {
			t.Fatal(err)
		}
	}
	base, err := knowledge.Load(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	h := NewTicketsHandler(db, slog.New(slog.NewTextHandler(io.Discard, nil)), analyzer.New(base))
	members := security.NewWorkspaceHandler(db)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/tickets", h.List)
	mux.HandleFunc("POST /api/tickets", h.Create)
	mux.HandleFunc("GET /api/tickets/{id}", h.Detail)
	mux.Handle("POST /api/tickets/{id}/feedback", security.ProtectIngestWithKeys(http.HandlerFunc(h.Feedback), "", db))
	mux.HandleFunc("GET /api/stats", h.Stats)
	mux.HandleFunc("GET /api/v1/auth/me", members.Me)
	mux.HandleFunc("GET /api/v1/members", members.Members)
	mux.HandleFunc("POST /api/v1/members", members.Members)
	mux.HandleFunc("PATCH /api/v1/members/{id}", members.UpdateMember)
	mux.HandleFunc("POST /api/v1/members/{id}/rotate", members.RotateMember)
	mux.Handle("POST /api/v1/ingest", security.ProtectIngestWithKeys(http.HandlerFunc(h.Ingest), "", db))
	mux.Handle("OPTIONS /api/v1/ingest", security.ProtectIngestWithKeys(http.HandlerFunc(h.Ingest), "", db))
	app := security.ProtectOperatorTenants(mux, db)
	call := func(token, method, path, body string) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		app.ServeHTTP(w, r)
		return w
	}
	expect := func(token, method, path, body string, status int) *httptest.ResponseRecorder {
		t.Helper()
		w := call(token, method, path, body)
		if w.Code != status {
			t.Fatalf("%s %s %s: %d want %d; %s", token, method, path, w.Code, status, w.Body.String())
		}
		return w
	}
	created := expect("token-employee", "POST", "/api/tickets", `{"user_email":"victim@example.org","raw_text":"Не работает VPN"}`, 201)
	var own models.Ticket
	if json.Unmarshal(created.Body.Bytes(), &own) != nil {
		t.Fatal("invalid created ticket")
	}
	if own.UserEmail != "employee@example.org" {
		t.Fatal("employee spoofed requester email")
	}
	var otherID, foreignID int
	if err = db.QueryRow(ctx, `INSERT INTO tickets(tenant_id,requester_id,user_email,raw_text,rag_answer,status) VALUES('company-a','other','employee@example.org','private','guide','awaiting_confirmation') RETURNING id`).Scan(&otherID); err != nil {
		t.Fatal(err)
	}
	if err = db.QueryRow(ctx, `INSERT INTO tickets(tenant_id,requester_id,raw_text) VALUES('company-b','employee','other-company') RETURNING id`).Scan(&foreignID); err != nil {
		t.Fatal(err)
	}
	list := expect("token-employee", "GET", "/api/tickets?email=victim@example.org", "", 200)
	var tickets []models.Ticket
	if json.Unmarshal(list.Body.Bytes(), &tickets) != nil || len(tickets) != 1 || tickets[0].ID != own.ID {
		t.Fatal("employee list leaked another person's ticket")
	}
	expect("token-employee", "GET", "/api/tickets/"+strconv.Itoa(otherID), "", 404)
	expect("token-employee", "POST", "/api/tickets/"+strconv.Itoa(otherID)+"/feedback", `{"resolved":true}`, 404)
	expect("token-employee", "GET", "/api/tickets/"+strconv.Itoa(foreignID), "", 404)
	if _, err = db.Exec(ctx, `UPDATE tickets SET rag_answer='guide',status='awaiting_confirmation' WHERE id=$1`, own.ID); err != nil {
		t.Fatal(err)
	}
	expect("token-employee", "POST", "/api/tickets/"+strconv.Itoa(own.ID)+"/feedback", `{"resolved":true}`, 200)
	expect("token-admin", "GET", "/api/tickets/"+strconv.Itoa(foreignID), "", 404)
	expect("token-employee", "GET", "/api/stats", "", 403)
	expect("token-operator", "GET", "/api/stats", "", 403)
	expect("token-analyst", "GET", "/api/stats", "", 200)
	expect("token-analyst", "GET", "/api/tickets", "", 403)
	expect("token-employee", "POST", "/api/v1/members", `{"role":"admin"}`, 403)
	expect("token-operator", "GET", "/api/v1/members", "", 403)
	expect("token-employee", "POST", "/api/v1/ingest", `{"channel":"web","sender":"victim@example.org","message":"bypass"}`, 403)
	expect("token-admin", "PATCH", "/api/v1/members/admin", `{"role":"employee"}`, 400)
	// Only the active owning company administrator can assign or revoke roles.
	expect("owner-company-b", "PATCH", "/api/v1/members/employee", `{"role":"admin"}`, 404)
	expect("token-admin", "PATCH", "/api/v1/members/employee", `{"role":"analyst"}`, 200)
	expect("token-employee", "GET", "/api/stats", "", 200)
	expect("token-employee", "GET", "/api/tickets", "", 403)
	expect("token-admin", "PATCH", "/api/v1/members/employee", `{"active":false}`, 200)
	expect("token-employee", "GET", "/api/v1/auth/me", "", 401)
	// Scoped integration keys bypass human login but cannot read dashboard data.
	_, err = db.Exec(ctx, `INSERT INTO api_keys(tenant_id,name,key_prefix,key_hash,scope) VALUES('company-a','adapter','adapter',$1,'ingest')`, hash("adapter-secret"))
	if err != nil {
		t.Fatal(err)
	}
	expect("adapter-secret", "POST", "/api/v1/ingest", `{"channel":"web","sender":"adapter@example.org","message":"Не работает VPN"}`, 201)
	expect("adapter-secret", "GET", "/api/tickets", "", 401)
	newMember := expect("token-admin", "POST", "/api/v1/members", `{"name":"New member","email":"NEW@example.org","role":"employee"}`, 201)
	var createdMember struct {
		ID    string `json:"id"`
		Token string `json:"token"`
	}
	if json.Unmarshal(newMember.Body.Bytes(), &createdMember) != nil || createdMember.Token == "" {
		t.Fatal("no personal code returned")
	}
	expect("token-admin", "POST", "/api/v1/members", `{"name":"Duplicate","email":"new@example.org","role":"employee"}`, 409)
	listed := expect("token-admin", "GET", "/api/v1/members", "", 200)
	if strings.Contains(listed.Body.String(), createdMember.Token) || strings.Contains(listed.Body.String(), "token_hash") {
		t.Fatal("member list leaked credential")
	}
	expect("owner-company-b", "POST", "/api/v1/members/"+createdMember.ID+"/rotate", "", 404)
	rotated := expect("token-admin", "POST", "/api/v1/members/"+createdMember.ID+"/rotate", "", 200)
	expect(createdMember.Token, "GET", "/api/v1/auth/me", "", 401)
	if json.Unmarshal(rotated.Body.Bytes(), &createdMember) != nil {
		t.Fatal("invalid rotated code")
	}
	expect(createdMember.Token, "GET", "/api/v1/auth/me", "", 200)
	if _, err = db.Exec(ctx, `INSERT INTO api_keys(tenant_id,name,key_prefix,key_hash,scope,allowed_origin) VALUES('company-a','widget','widget',$1,'widget','https://company.example')`, hash("widget-secret")); err != nil {
		t.Fatal(err)
	}
	for _, origin := range []string{"https://company.example", "https://attacker.example"} {
		r := httptest.NewRequest("OPTIONS", "/api/v1/ingest", nil)
		r.Header.Set("Origin", origin)
		r.Header.Set("Access-Control-Request-Headers", "authorization,content-type")
		w := httptest.NewRecorder()
		app.ServeHTTP(w, r)
		want := 403
		if origin == "https://company.example" {
			want = 204
		}
		if w.Code != want {
			t.Fatalf("preflight %s: %d", origin, w.Code)
		}
	}
}

func TestImportLongUnicodeHeadingDoesNotPanic(t *testing.T) {
	for _, title := range []string{strings.Repeat("Я", 130), strings.Repeat("界", 100), strings.Repeat("a", 400)} {
		articles := fallbackArticles("# "+title+"\nИнструкция для сотрудника", "IT")
		if len(articles) != 1 || len(articles[0].Title) > 240 || !utf8.ValidString(articles[0].Title) {
			t.Fatalf("invalid imported title: %+v", articles)
		}
	}
}

func TestSmartImportHonorsSensitiveDataPolicy(t *testing.T) {
	t.Setenv("FLOWOPS_AI_ALLOW_SENSITIVE", "false")
	for _, text := range []string{"Зарплата сотрудников и порядок выплаты премии", "Для вопросов пишите person@example.org"} {
		base, err := knowledge.Load(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		h := NewSmartHandler(base, analyzer.New(base))
		h.generator = func(context.Context, string, string) (string, bool, error) {
			t.Fatal("sensitive source reached external generator")
			return "", false, nil
		}
		r := httptest.NewRequest("POST", "/api/v1/kb/smart-import", jsonBody(map[string]any{"raw_text": text, "default_department": "General"}))
		w := httptest.NewRecorder()
		h.SmartImport(w, r)
		if w.Code != 201 || !strings.Contains(w.Body.String(), `"source":"fallback"`) {
			t.Fatalf("local import failed: %d %s", w.Code, w.Body.String())
		}
	}
}
