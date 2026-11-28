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
	"strconv"
	"strings"
	"testing"
)

func TestCopilotSourcesIsolationAndDraftOnly(t *testing.T) {
	db := replyTestDB(t)
	ctx := context.Background()
	base, _ := knowledge.Load(t.TempDir())
	h := NewTicketsHandler(db, slog.New(slog.NewTextHandler(io.Discard, nil)), analyzer.New(base))
	var id int
	if err := db.QueryRow(ctx, `INSERT INTO tickets(tenant_id,user_email,raw_text,department,status) VALUES('copilot-a','x@example.org','Принтер не печатает','IT','assigned_to_operator') RETURNING id`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	call := func(tenant, role, method, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, "/api/tickets/1/copilot", strings.NewReader(body)).WithContext(security.WithPrincipal(ctx, security.Principal{ID: role, Role: role, TenantID: tenant}))
		r.SetPathValue("id", strconv.Itoa(id))
		w := httptest.NewRecorder()
		h.Copilot(w, r)
		return w
	}
	if w := call("copilot-b", "operator", "GET", ""); w.Code != 404 {
		t.Fatal("copilot tenant leak")
	}
	if w := call("copilot-a", "employee", "GET", ""); w.Code != 403 {
		t.Fatal("employee copilot")
	}
	h.structuredGenerator = func(context.Context, string, string) (string, bool, error) {
		return `{"summary":"Принтер не печатает","reply":"Проверьте подключение принтера.","questions":[],"department":"IT","source_ids":[]}`, true, nil
	}
	w := call("copilot-a", "operator", "POST", `{"action":"reply"}`)
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	var output map[string]any
	json.Unmarshal(w.Body.Bytes(), &output)
	if output["requires_human_confirmation"] != true {
		t.Fatal("unconfirmed reply")
	}
	var messages int
	if err := db.QueryRow(ctx, `SELECT COUNT(*) FROM ticket_messages WHERE tenant_id='copilot-a' AND ticket_id=$1 AND role='operator'`, id).Scan(&messages); err != nil || messages != 0 {
		t.Fatal("copilot sent a message", messages, err)
	}
	h.structuredGenerator = func(context.Context, string, string) (string, bool, error) {
		return `{"summary":"x","source_ids":["ticket:99999"]}`, true, nil
	}
	if w = call("copilot-a", "operator", "POST", `{"action":"reply"}`); w.Code != 502 {
		t.Fatal("forged source", w.Body.String())
	}
}
