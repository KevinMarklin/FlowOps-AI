package handlers

import (
	"context"
	"encoding/json"
	"flowops-ai/internal/analyzer"
	"flowops-ai/internal/knowledge"
	"flowops-ai/internal/security"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestKnowledgeApprovalVersionsAndRAGIsolation(t *testing.T) {
	db := replyTestDB(t)
	ctx := context.Background()
	_, err := db.Exec(ctx, `INSERT INTO workspaces(tenant_id,name,token_prefix,token_hash) VALUES('kb-a','A','x','kb-a'),('kb-b','B','x','kb-b')`)
	if err != nil {
		t.Fatal(err)
	}
	base, _ := knowledge.Load(t.TempDir())
	h := NewKnowledgeHandler(base, db)
	call := func(tenant, role, method, id, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, "/api/v1/kb/articles", strings.NewReader(body)).WithContext(security.WithPrincipal(ctx, security.Principal{ID: role, Role: role, TenantID: tenant}))
		w := httptest.NewRecorder()
		if id == "" {
			h.Articles(w, r)
		} else {
			r.SetPathValue("id", id)
			h.ChangeArticle(w, r)
		}
		return w
	}
	d := ArticleDocument{Title: "Проверка принтера", Department: "IT", Keywords: []string{"принтер", "печать"}, Content: "Проверьте питание принтера и подключение к сети. Если проблема сохраняется, обратитесь в техническую поддержку."}
	raw, _ := json.Marshal(d)
	w := call("kb-a", "operator", "POST", "", string(raw))
	if w.Code != 201 {
		t.Fatal(w.Body.String())
	}
	var a ManagedArticle
	json.Unmarshal(w.Body.Bytes(), &a)
	rag := func(tenant string) []knowledge.Document {
		t.Helper()
		b, e := base.TenantBase(tenant)
		if e != nil {
			t.Fatal(e)
		}
		return b.Documents()
	}
	if len(rag("kb-a")) != 0 {
		t.Fatal("draft indexed")
	}
	act := func(role, action string) {
		t.Helper()
		body, _ := json.Marshal(map[string]any{"revision": a.Revision, "action": action, "document": d, "restore_revision": 1})
		w = call("kb-a", role, "PUT", a.ID, string(body))
		if w.Code != 200 {
			t.Fatal(action, w.Body.String())
		}
		json.Unmarshal(w.Body.Bytes(), &a)
	}
	act("operator", "submit")
	body, _ := json.Marshal(map[string]any{"revision": a.Revision, "action": "publish"})
	w = call("kb-a", "operator", "PUT", a.ID, string(body))
	if w.Code != 403 {
		t.Fatal("operator approval", w.Code)
	}
	act("admin", "publish")
	if len(rag("kb-a")) != 1 || len(rag("kb-b")) != 0 {
		t.Fatal("publication isolation")
	}
	old := d.Content
	d.Content = "Новая редакция: проверьте состояние кабеля принтера и сообщите о результате."
	act("operator", "edit")
	if docs := rag("kb-a"); len(docs) != 1 || docs[0].Content != old {
		t.Fatal("unapproved edit entered RAG", docs)
	}
	act("admin", "archive")
	if len(rag("kb-a")) != 0 {
		t.Fatal("archive still indexed")
	}
	act("operator", "restore")
	if len(rag("kb-a")) != 0 {
		t.Fatal("restore auto-published")
	}
	body, _ = json.Marshal(map[string]any{"revision": a.Revision, "action": "submit"})
	w = call("kb-b", "admin", "PUT", a.ID, string(body))
	if w.Code != 404 {
		t.Fatal("cross tenant article", w.Code)
	}
	// The production smart-import hook persists drafts without publishing them.
	t.Setenv("FLOWOPS_AI_PROVIDERS", "")
	t.Setenv("GIGACHAT_AUTH_KEY", "")
	smart := NewSmartHandler(base, analyzer.New(base)).WithDraftStore(h.ImportDraft)
	r := httptest.NewRequest("POST", "/api/v1/kb/smart-import", strings.NewReader(`{"raw_text":"Проверка принтера. Проверьте питание принтера и подключение кабеля. Если ошибка остаётся, обратитесь в техническую поддержку.","default_department":"IT"}`)).WithContext(security.WithPrincipal(ctx, security.Principal{ID: "admin", Role: "admin", TenantID: "kb-a"}))
	w = httptest.NewRecorder()
	smart.SmartImport(w, r)
	if w.Code != 201 {
		t.Fatal(w.Body.String())
	}
	if len(rag("kb-a")) != 0 {
		t.Fatal("smart import auto-published")
	}

}
