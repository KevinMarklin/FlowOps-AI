package handlers

import (
	"context"
	"encoding/json"
	"flowops-ai/internal/knowledge"
	"flowops-ai/internal/security"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestProcessAnalyticsObservedStagesAndRoutingLoop(t *testing.T) {
	db := replyTestDB(t)
	ctx := context.Background()
	if _, err := db.Exec(ctx, `INSERT INTO workspaces(tenant_id,name,token_prefix,token_hash) VALUES('process-observed','P','x','process-observed')`); err != nil {
		t.Fatal(err)
	}
	start := time.Now().UTC().Add(-time.Hour).Truncate(time.Microsecond)
	var id int
	if err := db.QueryRow(ctx, `INSERT INTO tickets(tenant_id,raw_text,status,department,created_at) VALUES('process-observed','Synthetic route','new','IT',$1) RETURNING id`, start).Scan(&id); err != nil {
		t.Fatal(err)
	}
	for _, step := range []struct {
		sql, kind string
		minutes   int
	}{{`UPDATE tickets SET status='assigned_to_operator' WHERE id=$1`, "status_changed", 10}, {`UPDATE tickets SET status='waiting_employee' WHERE id=$1`, "status_changed", 20}, {`UPDATE tickets SET department='HR' WHERE id=$1`, "department_changed", 25}, {`UPDATE tickets SET department='IT' WHERE id=$1`, "department_changed", 30}, {`UPDATE tickets SET status='closed' WHERE id=$1`, "status_changed", 40}} {
		if _, err := db.Exec(ctx, step.sql, id); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(ctx, `UPDATE ticket_events SET occurred_at=$3 WHERE id=(SELECT MAX(id) FROM ticket_events WHERE tenant_id='process-observed' AND ticket_id=$1 AND event_type=$2)`, id, step.kind, start.Add(time.Duration(step.minutes)*time.Minute)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(ctx, `UPDATE ticket_events SET occurred_at=$2 WHERE tenant_id='process-observed' AND ticket_id=$1 AND event_type='closed'`, id, start.Add(40*time.Minute)); err != nil {
		t.Fatal(err)
	}
	h := NewTicketsHandler(db, nil, nil)
	r := httptest.NewRequest("GET", "/api/analytics/processes", nil).WithContext(security.WithPrincipal(ctx, security.Principal{ID: "owner", TenantID: "process-observed", Role: "admin"}))
	w := httptest.NewRecorder()
	h.ProcessAnalytics(w, r)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	var data struct {
		Waiting float64 `json:"waiting_seconds"`
		Loops   int     `json:"loop_tickets"`
		Routes  []struct {
			Path string `json:"path"`
		} `json:"routes"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &data); err != nil {
		t.Fatal(err)
	}
	if data.Waiting != 1200 || data.Loops != 1 || len(data.Routes) != 1 || data.Routes[0].Path != "IT → HR → IT" {
		t.Fatal("wrong observed intervals/routes", w.Body.String())
	}
}

func TestKnowledgeExpiryPersonalIsolationAndReviewSourceValidation(t *testing.T) {
	db := replyTestDB(t)
	ctx := context.Background()
	if _, err := db.Exec(ctx, `INSERT INTO workspaces(tenant_id,name,token_prefix,token_hash) VALUES('kb-policy','K','x','kb-policy')`); err != nil {
		t.Fatal(err)
	}
	base, _ := knowledge.Load(t.TempDir())
	kh := NewKnowledgeHandler(base, db)
	past := time.Now().Add(-time.Hour)
	for _, item := range []struct {
		id, kind string
		expires  *time.Time
	}{{"permanent", "permanent", nil}, {"expired", "incident", &past}, {"personal", "personal", nil}} {
		d := ArticleDocument{Title: "Synthetic printer instruction", Department: "IT", Keywords: []string{"printer"}, Content: "Check printer power and contact the local technical support team if printing does not resume.", Kind: item.kind, ExpiresAt: item.expires}
		raw, _ := json.Marshal(d)
		if _, err := db.Exec(ctx, `INSERT INTO kb_articles(id,tenant_id,author_id,state,published_revision,document) VALUES($1,'kb-policy','owner','published',1,$2)`, item.id, raw); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(ctx, `INSERT INTO kb_article_versions(tenant_id,article_id,revision,state,document,actor) VALUES('kb-policy',$1,1,'published',$2,'owner')`, item.id, raw); err != nil {
			t.Fatal(err)
		}
	}
	snapshot, err := base.TenantBase("kb-policy")
	if err != nil || len(snapshot.Documents()) != 1 || snapshot.Documents()[0].Filename != "kb-managed-permanent.md" {
		t.Fatal("expired/personal knowledge entered RAG", err)
	}
	if validArticle(ArticleDocument{Kind: "incident"}) == nil {
		t.Fatal("incident without TTL accepted")
	}
	principal := security.Principal{ID: "owner", Role: "admin", TenantID: "kb-policy"}
	request := httptest.NewRequest("PUT", "/api/v1/kb/articles/personal", strings.NewReader(`{"revision":1,"action":"publish"}`)).WithContext(security.WithPrincipal(ctx, principal))
	request.SetPathValue("id", "personal")
	w := httptest.NewRecorder()
	kh.ChangeArticle(w, request)
	if w.Code != 409 && w.Code != 400 {
		t.Fatal("personal publication accepted", w.Code)
	}
	h := NewTicketsHandler(db, nil, nil)
	h.structuredGenerator = func(context.Context, string, string) (string, bool, error) {
		return `{"findings":[{"article_ids":["permanent","foreign"],"kind":"conflict","description":"Synthetic conflict"}]}`, true, nil
	}
	request = httptest.NewRequest("POST", "/api/v1/kb/checks", strings.NewReader(`{"article_ids":["permanent","expired"]}`)).WithContext(security.WithPrincipal(ctx, principal))
	w = httptest.NewRecorder()
	h.KnowledgeChecks(w, request)
	if w.Code != 502 {
		t.Fatal("invented knowledge source accepted", w.Code, w.Body.String())
	}
	h.structuredGenerator = func(context.Context, string, string) (string, bool, error) { return `{"findings":[]}`, true, nil }
	request = httptest.NewRequest("POST", "/api/v1/kb/checks", strings.NewReader(`{"article_ids":["permanent","expired"]}`)).WithContext(security.WithPrincipal(ctx, principal))
	w = httptest.NewRecorder()
	h.KnowledgeChecks(w, request)
	if w.Code != 200 {
		t.Fatal("valid review not saved", w.Code, w.Body.String())
	}
}
