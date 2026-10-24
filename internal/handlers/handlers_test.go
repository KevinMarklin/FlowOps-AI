package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"flowops-ai/internal/knowledge"
)

func TestKnowledgeAPIListCreateNormalizeAndFilter(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "it_vpn.md"), []byte("---\ndepartment: IT\nkeywords: vpn\n---\n# VPN\nVPN help"), 0600); err != nil {
		t.Fatal(err)
	}
	base, err := knowledge.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	handler := NewKnowledgeHandler(base)

	request := httptest.NewRequest(http.MethodPost, "/api/knowledge", jsonBody(map[string]any{
		"department": "Unassigned", "title": "Мобильный банк", "keywords": []string{"SSL_PINNING_ERR"}, "content": "Проверьте сертификат.",
	}))
	response := httptest.NewRecorder()
	handler.Create(response, request)
	if response.Code != http.StatusCreated {
		t.Fatalf("create status: %d, body: %s", response.Code, response.Body.String())
	}
	var created map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	if created["department"] != "General" {
		t.Fatalf("expected General, got %#v", created["department"])
	}

	request = httptest.NewRequest(http.MethodGet, "/api/knowledge?department=ит", nil)
	response = httptest.NewRecorder()
	handler.List(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("list status: %d", response.Code)
	}
	var filtered []map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &filtered); err != nil {
		t.Fatal(err)
	}
	if len(filtered) != 1 || filtered[0]["department"] != "IT" {
		t.Fatalf("unexpected filtered documents: %#v", filtered)
	}
}

func TestKnowledgeAPIRejectsInvalidJSONAndDepartmentIsRussianNormalized(t *testing.T) {
	base, err := knowledge.Load(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	handler := NewKnowledgeHandler(base)
	request := httptest.NewRequest(http.MethodPost, "/api/knowledge", jsonBody(map[string]any{
		"department": "АХО и офис", "title": "Принтер", "content": "Заменить картридж.",
	}))
	response := httptest.NewRecorder()
	handler.Create(response, request)
	if response.Code != http.StatusCreated || !contains(response.Body.String(), `"department":"Facilities"`) {
		t.Fatalf("expected Facilities response, got %d: %s", response.Code, response.Body.String())
	}

	request = httptest.NewRequest(http.MethodPost, "/api/knowledge", jsonBody("bad"))
	response = httptest.NewRecorder()
	handler.Create(response, request)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("expected bad request, got %d", response.Code)
	}
}

func TestKnowledgeLifecycleTTLResolveAndDelete(t *testing.T) {
	base, err := knowledge.Load(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	handler := NewKnowledgeHandler(base)
	request := httptest.NewRequest(http.MethodPost, "/api/knowledge", jsonBody(map[string]any{"department": "IT", "title": "Авария NL-1", "keywords": []string{"NL-1"}, "content": "Магистраль восстановлена", "kind": "incident", "ttl_seconds": 120}))
	response := httptest.NewRecorder()
	handler.Create(response, request)
	if response.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", response.Code, response.Body.String())
	}
	var created map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	filename := created["filename"].(string)
	if created["expires_at"] == nil || !strings.Contains(filename, "avariya_nl_1") {
		t.Fatalf("missing TTL or transliteration: %#v", created)
	}
	request = httptest.NewRequest(http.MethodPatch, "/api/knowledge/"+filename, jsonBody(map[string]any{"action": "resolve_incident", "resolution_note": "Переключили маршрут."}))
	request.SetPathValue("filename", filename)
	response = httptest.NewRecorder()
	handler.Update(response, request)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "Сбой по теме") || !strings.Contains(response.Body.String(), `"incident_state":"resolved"`) {
		t.Fatalf("resolve failed: %d %s", response.Code, response.Body.String())
	}
	request = httptest.NewRequest(http.MethodDelete, "/api/knowledge/"+filename, nil)
	request.SetPathValue("filename", filename)
	response = httptest.NewRecorder()
	handler.Delete(response, request)
	if response.Code != http.StatusOK || len(base.Documents()) != 0 {
		t.Fatalf("delete failed: %d %s %#v", response.Code, response.Body.String(), base.Documents())
	}
}

func jsonBody(value any) *strings.Reader {
	data, _ := json.Marshal(value)
	return strings.NewReader(string(data))
}

func contains(value, fragment string) bool { return strings.Contains(value, fragment) }
