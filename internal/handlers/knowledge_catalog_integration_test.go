package handlers

import (
	"context"
	"encoding/json"
	"flowops-ai/internal/knowledge"
	"flowops-ai/internal/security"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestGovernedCatalogPreservesLegacyAndBlocksPublicationBypass(t *testing.T) {
	db := replyTestDB(t)
	ctx := security.WithPrincipal(context.Background(), security.Principal{ID: "admin", Role: "admin", TenantID: "catalog-a"})
	if _, err := db.Exec(ctx, `INSERT INTO workspaces(tenant_id,name,token_prefix,token_hash) VALUES('catalog-a','Catalog','x','catalog-a')`); err != nil {
		t.Fatal(err)
	}
	base, _ := knowledge.Load(t.TempDir())
	legacy, _ := base.TenantBase("catalog-a")
	if _, err := legacy.Add("IT", "Printer approved instruction", []string{"printer"}, "Check the printer power and contact support if the problem persists."); err != nil {
		t.Fatal(err)
	}
	h := NewKnowledgeHandler(base, db).WithGovernedWrites()
	w := httptest.NewRecorder()
	h.Articles(w, httptest.NewRequest("GET", "/api/v1/kb/articles", nil).WithContext(ctx))
	var listing struct {
		Items []ManagedArticle `json:"items"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &listing); err != nil || len(listing.Items) != 1 {
		t.Fatalf("legacy not catalogued: %s", w.Body.String())
	}
	inherited := listing.Items[0]
	if inherited.State != "published" || inherited.ApprovedBy != "inherited-existing-publication" {
		t.Fatal("legacy provenance or availability lost")
	}
	r := httptest.NewRequest("POST", "/api/knowledge", strings.NewReader(`{"department":"IT","title":"New printer instruction","keywords":["printer"],"content":"Check the printer network connection before contacting technical support."}`)).WithContext(ctx)
	w = httptest.NewRecorder()
	h.Create(w, r)
	if w.Code != 201 || !strings.Contains(w.Body.String(), `"state":"draft"`) {
		t.Fatal("old create path bypassed draft", w.Body.String())
	}
	snapshot, err := base.TenantBase("catalog-a")
	if err != nil || len(snapshot.Documents()) != 1 {
		t.Fatal("draft leaked into RAG", err)
	}
	r = httptest.NewRequest("PUT", "/api/v1/kb/articles/"+inherited.ID, strings.NewReader(`{"revision":1,"action":"archive"}`)).WithContext(ctx)
	r.SetPathValue("id", inherited.ID)
	w = httptest.NewRecorder()
	h.ChangeArticle(w, r)
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	snapshot, err = base.TenantBase("catalog-a")
	if err != nil || len(snapshot.Documents()) != 0 {
		t.Fatal("legacy file reactivated archived article", err)
	}
	// Restarting the catalogue must not recreate a withdrawn inherited publication.
	h = NewKnowledgeHandler(base, db).WithGovernedWrites()
	w = httptest.NewRecorder()
	h.Articles(w, httptest.NewRequest("GET", "/api/v1/kb/articles", nil).WithContext(ctx))
	snapshot, err = base.TenantBase("catalog-a")
	if err != nil || len(snapshot.Documents()) != 0 {
		t.Fatal("restart resurrected archived article", err)
	}
	for _, method := range []string{"PATCH", "DELETE"} {
		r = httptest.NewRequest(method, "/api/knowledge/printer.md", strings.NewReader(`{}`)).WithContext(ctx)
		w = httptest.NewRecorder()
		if method == "PATCH" {
			h.Update(w, r)
		} else {
			h.Delete(w, r)
		}
		if w.Code != 409 {
			t.Fatal("old mutation bypass", method, w.Code)
		}
	}
}

func TestArticleSearchFindsItemsBeyondFirstPage(t *testing.T) {
	db := replyTestDB(t)
	ctx := security.WithPrincipal(context.Background(), security.Principal{ID: "admin", Role: "admin", TenantID: "catalog-search"})
	if _, err := db.Exec(ctx, `INSERT INTO workspaces(tenant_id,name,token_prefix,token_hash) VALUES('catalog-search','Search','x','catalog-search'); INSERT INTO kb_articles(id,tenant_id,author_id,state,document,updated_at) SELECT 'search-'||n,'catalog-search','admin','draft',jsonb_build_object('title',CASE WHEN n=1 THEN 'Find oldest printer guide' ELSE 'Other guide '||n END,'department','IT','keywords',jsonb_build_array('printer'),'content','Check printer power and contact support.'),NOW()+n*INTERVAL '1 second' FROM generate_series(1,105) n`); err != nil {
		t.Fatal(err)
	}
	base, _ := knowledge.Load(t.TempDir())
	h := NewKnowledgeHandler(base, db)
	w := httptest.NewRecorder()
	h.Articles(w, httptest.NewRequest("GET", "/api/v1/kb/articles?search=oldest", nil).WithContext(ctx))
	var listing struct {
		Items []ManagedArticle `json:"items"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &listing); err != nil || len(listing.Items) != 1 || listing.Items[0].ID != "search-1" {
		t.Fatalf("server search missed old article %s", w.Body.String())
	}
}
