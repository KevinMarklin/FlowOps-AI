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

func TestTicketFieldsAttachmentsAndClosure(t *testing.T) {
	db := replyTestDB(t)
	ctx := context.Background()
	if _, err := db.Exec(ctx, `INSERT INTO workspaces(tenant_id,name,token_prefix,token_hash) VALUES('form-a','Form','x','form-a'); INSERT INTO organization_ticket_forms(tenant_id,definition) VALUES('form-a','{"fields":[{"id":"urgent","label":"Urgent","type":"boolean","required":true}]}')`); err != nil {
		t.Fatal(err)
	}
	var id int
	if err := db.QueryRow(ctx, `INSERT INTO tickets(tenant_id,raw_text,status,requester_id) VALUES('form-a','Printer','new','person') RETURNING id`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	base, _ := knowledge.Load(t.TempDir())
	h := NewTicketsHandler(db, slog.New(slog.NewTextHandler(io.Discard, nil)), analyzer.New(base))
	call := func(method, tenant, suffix, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, "/api/tickets/"+strconv.Itoa(id)+suffix, strings.NewReader(body)).WithContext(security.WithPrincipal(ctx, security.Principal{ID: "person", Role: "employee", TenantID: tenant}))
		r.SetPathValue("id", strconv.Itoa(id))
		r.Header.Set("X-Filename", "report.txt")
		if strings.HasPrefix(suffix, "/attachments/") {
			r.SetPathValue("attachment", strings.TrimPrefix(suffix, "/attachments/"))
		}
		w := httptest.NewRecorder()
		if strings.HasPrefix(suffix, "/fields") {
			h.CustomFields(w, r)
		} else {
			h.Attachments(w, r)
		}
		return w
	}
	if w := call("PUT", "form-a", "/fields", `{"revision":0,"form_revision":1,"values":{"urgent":false}}`); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	if w := call("PUT", "form-a", "/fields", `{"revision":0,"form_revision":1,"values":{"urgent":true}}`); w.Code != 409 {
		t.Fatal("stale fields accepted", w.Code, w.Body.String())
	}
	w := call("POST", "form-a", "/attachments", "Plain synthetic attachment")
	if w.Code != 201 {
		t.Fatal(w.Code, w.Body.String())
	}
	var file struct {
		ID string `json:"id"`
	}
	json.Unmarshal(w.Body.Bytes(), &file)
	if w = call("GET", "form-a", "/attachments/"+file.ID, ""); w.Code != 200 || w.Body.String() != "Plain synthetic attachment" || w.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatal("download failed", w.Code)
	}
	if w = call("GET", "other", "/attachments/"+file.ID, ""); w.Code != 404 {
		t.Fatal("foreign attachment visible", w.Code)
	}
	if _, err := db.Exec(ctx, `UPDATE tickets SET status='closed' WHERE id=$1`, id); err != nil {
		t.Fatal(err)
	}
	if w = call("POST", "form-a", "/attachments", "More text"); w.Code != 409 {
		t.Fatal("closed attachment accepted", w.Code)
	}
	if w = call("PUT", "form-a", "/fields", `{"revision":1,"form_revision":1,"values":{"urgent":true}}`); w.Code != 409 {
		t.Fatal("closed fields accepted", w.Code)
	}
}
