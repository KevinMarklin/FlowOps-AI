package handlers

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"flowops-ai/internal/analyzer"
	"flowops-ai/internal/knowledge"
	"flowops-ai/internal/security"
	"io"
	"log/slog"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestRequiredAttachmentIsAtomicAtCreation(t *testing.T) {
	db := replyTestDB(t)
	ctx := security.WithPrincipal(context.Background(), security.Principal{ID: "upload-person", Role: "employee", TenantID: "upload-org", Email: "upload@example.org"})
	_, err := db.Exec(ctx, `INSERT INTO workspaces(tenant_id,name,token_prefix,token_hash) VALUES('upload-org','Upload','x','upload-org');INSERT INTO organization_ticket_forms(tenant_id,definition) VALUES('upload-org','{"fields":[{"id":"proof","label":"Proof","type":"attachment","required":true}]}')`)
	if err != nil {
		t.Fatal(err)
	}
	base, _ := knowledge.Load(t.TempDir())
	h := NewTicketsHandler(db, slog.New(slog.NewTextHandler(io.Discard, nil)), analyzer.New(base))
	call := func(body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", "/api/tickets", strings.NewReader(body)).WithContext(ctx)
		w := httptest.NewRecorder()
		h.Create(w, r)
		return w
	}
	if w := call(`{"raw_text":"Synthetic request","form_revision":1}`); w.Code != 400 {
		t.Fatal("missing attachment accepted", w.Code, w.Body.String())
	}
	body := `{"raw_text":"Synthetic request","form_revision":1,"attachments":{"proof":{"filename":"proof.txt","content":"` + base64.StdEncoding.EncodeToString([]byte("Synthetic attachment")) + `"}}}`
	w := call(body)
	if w.Code != 201 {
		t.Fatal(w.Code, w.Body.String())
	}
	var ticket struct {
		ID int `json:"id"`
	}
	json.Unmarshal(w.Body.Bytes(), &ticket)
	var valid bool
	if err = db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM ticket_attachments a JOIN ticket_custom_fields f ON f.ticket_id=a.ticket_id AND f.tenant_id=a.tenant_id WHERE a.ticket_id=$1 AND a.id=f.values->>'proof' AND a.content=$2)`, ticket.ID, []byte("Synthetic attachment")).Scan(&valid); err != nil || !valid {
		t.Fatal("file/field not saved atomically", err)
	}
}
func TestSLAPauseDoesNotEraseObservedFirstResponse(t *testing.T) {
	db := replyTestDB(t)
	ctx := context.Background()
	_, err := db.Exec(ctx, `INSERT INTO workspaces(tenant_id,name,token_prefix,token_hash) VALUES('pause-history','SLA','x','pause-history');INSERT INTO app_settings(tenant_id,platform_config) VALUES('pause-history','{"timezone":"UTC","sla":{"clock":"calendar","first_response_minutes":5,"resolution_minutes":30},"process":{"pause_statuses":["waiting_employee"]}}')`)
	if err != nil {
		t.Fatal(err)
	}
	var id int
	var before, after time.Time
	if err = db.QueryRow(ctx, `INSERT INTO tickets(tenant_id,raw_text,status,created_at) VALUES('pause-history','Late response','assigned_to_operator',(NOW() AT TIME ZONE 'UTC')-INTERVAL '2 hours') RETURNING id,first_response_due_at`).Scan(&id, &before); err != nil {
		t.Fatal(err)
	}
	for _, query := range []string{`INSERT INTO ticket_events(tenant_id,ticket_id,event_type,origin,occurred_at) VALUES('pause-history',$1,'response_available','observed',NOW()-INTERVAL '90 minutes')`, `UPDATE tickets SET status='waiting_employee' WHERE id=$1`, `UPDATE tickets SET sla_paused_at=NOW()-INTERVAL '1 hour' WHERE id=$1`, `UPDATE tickets SET status='assigned_to_operator' WHERE id=$1`} {
		if _, err = db.Exec(ctx, query, id); err != nil {
			t.Fatal(err)
		}
	}
	if err = db.QueryRow(ctx, `SELECT first_response_due_at FROM tickets WHERE id=$1`, id).Scan(&after); err != nil || !after.Equal(before) {
		t.Fatal("historical SLA changed", before, after, err)
	}
}
func TestRoutingSkipsRemovedDestination(t *testing.T) {
	db := replyTestDB(t)
	ctx := context.Background()
	_, err := db.Exec(ctx, `INSERT INTO workspaces(tenant_id,name,token_prefix,token_hash) VALUES('route-disabled','R','x','route-disabled');INSERT INTO app_settings(tenant_id,builder_config,platform_config) VALUES('route-disabled','{"departments":[{"name":"HR"}]}','{"process":{"routing":[{"id":"stale","match":{},"department":"Disabled"},{"id":"valid","match":{},"department":"HR"}]}}')`)
	if err != nil {
		t.Fatal(err)
	}
	var department string
	if err = db.QueryRow(ctx, `INSERT INTO tickets(tenant_id,raw_text,department) VALUES('route-disabled','Synthetic','General') RETURNING department`).Scan(&department); err != nil || department != "HR" {
		t.Fatal("invalid route selected", department, err)
	}
}

func TestNamedFormsValidateAndSnapshot(t *testing.T) {
	f := TicketFormDefinition{Forms: []NamedTicketForm{{ID: "purchase", Label: "Purchase", Fields: []CustomField{{ID: "sum", Label: "Sum", Type: "number", Required: true}}}}}
	if err := validForm(f); err != nil {
		t.Fatal(err)
	}
	f.Forms = append(f.Forms, f.Forms[0])
	if validForm(f) == nil {
		t.Fatal("duplicate form accepted")
	}
}

func TestRoutingBotChannelAndEmptyLists(t *testing.T) {
	db := replyTestDB(t)
	ctx := context.Background()
	_, err := db.Exec(ctx, `INSERT INTO workspaces(tenant_id,name,token_prefix,token_hash) VALUES('bot-rules','B','x','bot-rules');INSERT INTO app_settings(tenant_id,platform_config) VALUES('bot-rules','{"process":{"routing":null,"sla":null,"escalations":null}}')`)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(ctx, `INSERT INTO tickets(tenant_id,raw_text,status) VALUES('bot-rules','Empty configuration','new')`); err != nil {
		t.Fatal("null lists rejected", err)
	}
	if _, err = db.Exec(ctx, `UPDATE app_settings SET platform_config='{"process":{"routing":[{"id":"tg","department":"IT","match":{"channel":"telegram"}}]}}' WHERE tenant_id='bot-rules'`); err != nil {
		t.Fatal(err)
	}
	var department string
	if err = db.QueryRow(ctx, `INSERT INTO tickets(tenant_id,raw_text,user_email,channel,department) VALUES('bot-rules','TG synthetic','tg:123','bot','General') RETURNING department`).Scan(&department); err != nil || department != "IT" {
		t.Fatal(department, err)
	}
}
func TestConditionalFormRejectsArrayParents(t *testing.T) {
	f := TicketFormDefinition{Fields: []CustomField{{ID: "choices", Label: "Choices", Type: "multi", Options: []string{"a", "b"}}, {ID: "detail", Label: "Detail", Type: "text", ShowWhen: "choices", Equals: "a,b"}}}
	if validForm(f) == nil {
		t.Fatal("ambiguous array condition accepted")
	}
}
