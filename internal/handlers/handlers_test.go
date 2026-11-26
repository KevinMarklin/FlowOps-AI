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

	"flowops-ai/internal/analyzer"
	"flowops-ai/internal/knowledge"
	"flowops-ai/internal/security"
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

func TestSmartImportMasksPIIAndSanitizesGeneratedFilename(t *testing.T) {
	t.Setenv("FLOWOPS_AI_ALLOW_SENSITIVE", "true")
	operatorToken := strings.Repeat("x", 32)
	t.Setenv("FLOWOPS_OPERATOR_TOKEN", operatorToken)
	dir := t.TempDir()
	base, err := knowledge.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	a := analyzer.New(base)
	h := NewSmartHandler(base, a)
	called := false
	h.generator = func(_ context.Context, system, input string) (string, bool, error) {
		called = true
		if strings.Contains(input, "ivanov@example.ru") || !strings.Contains(input, "[EMAIL_1]") {
			t.Fatalf("LLM input was not PII masked: %q", input)
		}
		if !strings.Contains(system, "строго на русском") {
			t.Fatalf("unexpected system prompt: %q", system)
		}
		return `[{"title":"../../внешний путь","department":"IT","keywords":["процедура"],"content_markdown":"Шаг 1: следуйте процедуре."}]`, true, nil
	}
	wrapped := security.ProtectOperatorWrites(http.HandlerFunc(h.SmartImport), operatorToken)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/kb/smart-import", jsonBody(map[string]any{"raw_text": "Для вопросов пишите ivanov@example.ru", "default_department": "IT"}))
	req.Header.Set("Authorization", "Bearer "+strings.Repeat("x", 32))
	res := httptest.NewRecorder()
	wrapped.ServeHTTP(res, req)
	if res.Code != http.StatusCreated {
		t.Fatalf("import failed: %d %s", res.Code, res.Body.String())
	}
	if !called {
		t.Fatal("configured LLM generator was not called")
	}
	var payload struct {
		Articles []knowledgeDocument `json:"articles"`
	}
	if err := json.Unmarshal(res.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.Articles) != 1 {
		t.Fatalf("unexpected articles: %s", res.Body.String())
	}
	filename := payload.Articles[0].Filename
	if filepath.IsAbs(filename) || strings.Contains(filename, "..") || strings.ContainsAny(filename, "/\\") {
		t.Fatalf("unsafe filename: %q", filename)
	}
	if _, err := os.Stat(filepath.Join(dir, filename)); err != nil {
		t.Fatalf("sanitized article not saved: %v", err)
	}
	if strings.Contains(payload.Articles[0].Content, "ivanov@example.ru") {
		t.Fatal("personal data was imported into the KB article")
	}
}

func TestSmartImportRequiresOperatorAndFallsBackByParagraph(t *testing.T) {
	operatorToken := strings.Repeat("z", 32)
	t.Setenv("FLOWOPS_OPERATOR_TOKEN", operatorToken)
	base, err := knowledge.Load(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	h := NewSmartHandler(base, analyzer.New(base))
	h.generator = func(context.Context, string, string) (string, bool, error) { return "", false, nil }
	wrapped := security.ProtectOperatorWrites(http.HandlerFunc(h.SmartImport), operatorToken)
	request := httptest.NewRequest(http.MethodPost, "/api/v1/kb/smart-import", jsonBody(map[string]any{"raw_text": "## VPN\nПодключите профиль VPN.\n\n## Почта\nПроверьте папку Спам.", "default_department": "IT"}))
	response := httptest.NewRecorder()
	wrapped.ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated import: %d", response.Code)
	}
	request = httptest.NewRequest(http.MethodPost, "/api/v1/kb/smart-import", jsonBody(map[string]any{"raw_text": "## VPN\nПодключите профиль VPN.\n\n## Почта\nПроверьте папку Спам.", "default_department": "IT"}))
	request.Header.Set("Authorization", "Bearer "+strings.Repeat("z", 32))
	response = httptest.NewRecorder()
	wrapped.ServeHTTP(response, request)
	if response.Code != http.StatusCreated || len(base.Documents()) != 2 {
		t.Fatalf("fallback import failed: %d %s", response.Code, response.Body.String())
	}
}

func TestSmartImportMasksPhoneBeforeLLM(t *testing.T) {
	t.Setenv("FLOWOPS_AI_ALLOW_SENSITIVE", "true")
	operatorToken := strings.Repeat("m", 32)
	t.Setenv("FLOWOPS_OPERATOR_TOKEN", operatorToken)
	base, err := knowledge.Load(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	h := NewSmartHandler(base, analyzer.New(base))
	h.generator = func(_ context.Context, _ string, input string) (string, bool, error) {
		if strings.Contains(input, "+7 999 123-45-67") || !strings.Contains(input, "[PHONE_1]") {
			t.Fatalf("phone PII reached generator unmasked: %q", input)
		}
		return `[{"title":"Контакты","department":"General","keywords":["контакты"],"content_markdown":"Свяжитесь с администратором."}]`, true, nil
	}
	request := httptest.NewRequest(http.MethodPost, "/api/v1/kb/smart-import", jsonBody(map[string]any{"raw_text": "Телефон администратора +7 999 123-45-67", "default_department": "General"}))
	request.Header.Set("Authorization", "Bearer "+operatorToken)
	response := httptest.NewRecorder()
	h.SmartImport(response, request)
	if response.Code != http.StatusCreated {
		t.Fatalf("masked phone import failed: %d %s", response.Code, response.Body.String())
	}
}

func TestSandboxPreviewRendersChannelWithoutTicketPersistence(t *testing.T) {
	operatorToken := strings.Repeat("p", 32)
	t.Setenv("FLOWOPS_OPERATOR_TOKEN", operatorToken)
	base, err := knowledge.Load(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := base.Add("IT", "VPN подключение", []string{"VPN", "809"}, "## Настройка VPN\n\nПерезапустите клиент."); err != nil {
		t.Fatal(err)
	}
	h := NewSmartHandler(base, analyzer.New(base))
	wrapped := security.ProtectOperatorWrites(http.HandlerFunc(h.Preview), operatorToken)
	request := httptest.NewRequest(http.MethodPost, "/api/v1/settings/preview", jsonBody(map[string]any{"message": "Не подключается VPN, ошибка 809", "channel": "telegram"}))
	request.Header.Set("Authorization", "Bearer "+strings.Repeat("p", 32))
	response := httptest.NewRecorder()
	wrapped.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("preview failed: %d %s", response.Code, response.Body.String())
	}
	var result map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result["auto_replied"] != true || result["matched_kb_title"] != "VPN подключение" || !strings.Contains(result["rendered_preview"].(string), "Настройка VPN") {
		t.Fatalf("unexpected preview: %#v", result)
	}
	request = httptest.NewRequest(http.MethodPost, "/api/v1/settings/preview", jsonBody(map[string]any{"message": "VPN 809", "channel": "email"}))
	request.Header.Set("Authorization", "Bearer "+strings.Repeat("p", 32))
	response = httptest.NewRecorder()
	wrapped.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("email preview failed: %d %s", response.Code, response.Body.String())
	}
	var emailResult map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &emailResult); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(emailResult["rendered_preview"].(string), "<!doctype html>") {
		t.Fatalf("email preview missing HTML: %#v", emailResult)
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
