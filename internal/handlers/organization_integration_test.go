package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"flowops-ai/internal/database"
	"flowops-ai/internal/knowledge"
	"flowops-ai/internal/security"
	"flowops-ai/internal/settings"
)

func TestOrganizationRegistrationSeedsIsolatedWorkspace(t *testing.T) {
	db := replyTestDB(t)
	t.Chdir(t.TempDir())
	handler := security.NewWorkspaceHandler(db)
	invalid := httptest.NewRecorder()
	handler.Create(invalid, httptest.NewRequest("POST", "/api/v1/workspaces", strings.NewReader(`{"name":"Invalid","organization_type":"invalid"}`)))
	if invalid.Code != 400 {
		t.Fatalf("invalid type: %d", invalid.Code)
	}
	for _, kind := range []string{"education", "bakery", "bank", "other", ""} {
		body, _ := json.Marshal(map[string]string{"name": "Organization " + kind, "organization_type": kind})
		result := httptest.NewRecorder()
		handler.Create(result, httptest.NewRequest("POST", "/api/v1/workspaces", strings.NewReader(string(body))))
		if result.Code != 201 {
			t.Fatalf("%s: %d %s", kind, result.Code, result.Body.String())
		}
		var created struct {
			Tenant string `json:"tenant_id"`
			Token  string `json:"token"`
		}
		if err := json.Unmarshal(result.Body.Bytes(), &created); err != nil {
			t.Fatal(err)
		}
		template, _ := settings.Organization(kind)
		var persisted string
		var raw []byte
		if err := db.QueryRow(context.Background(), `SELECT w.organization_type,s.builder_config FROM workspaces w JOIN app_settings s USING(tenant_id) WHERE w.tenant_id=$1`, created.Tenant).Scan(&persisted, &raw); err != nil {
			t.Fatal(err)
		}
		if persisted != template.Type {
			t.Fatalf("persisted type %s", persisted)
		}
		var config settings.BuilderConfig
		if err := json.Unmarshal(raw, &config); err != nil {
			t.Fatal(err)
		}
		if len(config.Departments) != len(template.Builder.Departments) || config.StartTemplate != template.Builder.StartTemplate {
			t.Fatal("incorrect starter settings")
		}
		base, err := knowledge.Load(filepath.Join("knowledge_base", created.Tenant))
		if err != nil {
			t.Fatal(err)
		}
		docs := base.Documents()
		if len(docs) != len(template.Documents) {
			t.Fatal("incorrect document count")
		}
		for _, doc := range docs {
			if !strings.Contains(doc.Content, "Требует адаптации") {
				t.Fatal("missing notice")
			}
		}
		mux := http.NewServeMux()
		mux.HandleFunc("GET /api/v1/auth/me", handler.Me)
		request := httptest.NewRequest("GET", "/api/v1/auth/me", nil)
		request.Header.Set("Authorization", "Bearer "+created.Token)
		profile := httptest.NewRecorder()
		security.ProtectOperatorTenants(mux, db).ServeHTTP(profile, request)
		if profile.Code != 200 || !strings.Contains(profile.Body.String(), `"type":"`+template.Type+`"`) {
			t.Fatalf("profile: %d %s", profile.Code, profile.Body.String())
		}
		// Repeated migrations preserve edits and never add or rewrite starter files.
		first := filepath.Join("knowledge_base", created.Tenant, template.Documents[0].Slug+".md")
		if err := os.WriteFile(first, []byte("# Organization's edited regulation"), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(context.Background(), `UPDATE app_settings SET builder_config='{"signature":"Custom signature"}' WHERE tenant_id=$1`, created.Tenant); err != nil {
			t.Fatal(err)
		}
		if err := database.Migrate(context.Background(), db); err != nil {
			t.Fatal(err)
		}
		edited, err := os.ReadFile(first)
		if err != nil || string(edited) != "# Organization's edited regulation" {
			t.Fatal("migration changed regulations")
		}
		if err := db.QueryRow(context.Background(), `SELECT builder_config->>'signature' FROM app_settings WHERE tenant_id=$1`, created.Tenant).Scan(&persisted); err != nil || persisted != "Custom signature" {
			t.Fatal("migration changed settings")
		}
	}
}
