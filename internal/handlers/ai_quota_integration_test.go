package handlers

import (
	"context"
	"flowops-ai/internal/analyzer"
	"flowops-ai/internal/knowledge"
	"flowops-ai/internal/security"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync/atomic"
	"testing"
)

func TestAIQuotaPersistsAcrossAnalyzerInstances(t *testing.T) {
	db := replyTestDB(t)
	ctx := security.WithPrincipal(context.Background(), security.Principal{TenantID: "quota-org", ID: "quota-person", Role: "admin"})
	if _, err := db.Exec(ctx, `INSERT INTO workspaces(tenant_id,name,token_prefix,token_hash) VALUES('quota-org','Q','x','quota-org');INSERT INTO app_settings(tenant_id,platform_config) VALUES('quota-org','{"ai":{"mode":"partial","daily_limit":1}}')`); err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int64
	fixture := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		io.WriteString(w, `{"choices":[{"message":{"content":"{\"ok\":true}"}}]}`)
	}))
	defer fixture.Close()
	original := http.DefaultTransport
	target, _ := url.Parse(fixture.URL)
	http.DefaultTransport = yandexFixtureTransport{target: target, transport: fixture.Client().Transport}
	t.Cleanup(func() { http.DefaultTransport = original })
	t.Setenv("FLOWOPS_AI_PROVIDERS", "yandex")
	t.Setenv("YANDEX_FOLDER_ID", "quota-folder")
	t.Setenv("YANDEX_API_KEY", "synthetic")
	base, _ := knowledge.Load(t.TempDir())
	for i := 0; i < 2; i++ {
		a := analyzer.New(base).WithAIPolicy(db)
		_, used, err := a.GenerateStructured(ctx, "Return JSON", "Public synthetic quota check")
		if err != nil {
			t.Fatal(err)
		}
		if used != (i == 0) {
			t.Fatal("quota not shared between instances", i, used)
		}
	}
	if calls.Load() != 1 {
		t.Fatal("extra provider call", calls.Load())
	}
	var count int
	if err := db.QueryRow(ctx, `SELECT calls FROM organization_ai_usage WHERE tenant_id='quota-org'`).Scan(&count); err != nil || count != 1 {
		t.Fatal(count, err)
	}
}
