package analyzer

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"flowops-ai/internal/knowledge"
)

func TestAnalyzeVPNSummaryAndRAGSource(t *testing.T) {
	dir := t.TempDir()
	content := "# VPN\nИнструкция для ошибки 809."
	if err := os.WriteFile(filepath.Join(dir, "it_vpn.md"), []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	base, err := knowledge.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	result := New(base).Analyze(context.Background(), "Срочно! Не работает корпоративный VPN, ошибка 809.")
	if result.Department != "IT" || result.Priority != "high" {
		t.Fatalf("unexpected classification: %#v", result)
	}
	if !strings.Contains(result.ExtractedSummary, "Не работает корпоративный VPN") || !strings.Contains(result.ExtractedSummary, "код ошибки: 809") {
		t.Fatalf("summary lost the problem details: %q", result.ExtractedSummary)
	}
	if result.RAGAnswer == nil || !strings.Contains(*result.RAGAnswer, "[Источник:") || !strings.Contains(*result.RAGAnswer, "it_vpn.md") {
		t.Fatalf("unexpected RAG answer: %v", result.RAGAnswer)
	}
}

func TestAnalyzeUnknownTopicHasNoRAGAnswer(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "it_vpn.md"), []byte("# VPN\nОшибка 809."), 0600); err != nil {
		t.Fatal(err)
	}
	base, err := knowledge.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	result := New(base).Analyze(context.Background(), "Упал продакшен-кластер Kubernetes после деплоя, нужна помощь девопса.")
	if result.RAGAnswer != nil || result.Department != "Unassigned" {
		t.Fatalf("unexpected result: %#v", result)
	}
}

func TestCleanJSONContent(t *testing.T) {
	content := "Ответ:\n```json\n{\"department\":\"IT\",\"priority\":\"high\"}\n```\n"
	var result Result
	if err := json.Unmarshal([]byte(cleanJSONContent(content)), &result); err != nil {
		t.Fatal(err)
	}
	if result.Department != "IT" || result.Priority != "high" {
		t.Fatalf("unexpected result: %#v", result)
	}
}

func TestAnalyzeWithGeminiMockAndTrailingSlash(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "it_vpn.md"), []byte("# VPN\nИнструкция для ошибки 809."), 0600); err != nil {
		t.Fatal(err)
	}
	base, err := knowledge.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" || r.Method != http.MethodPost {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		content := "```json\n{\"department\":\"IT\",\"priority\":\"high\",\"extracted_summary\":\"VPN 809\",\"rag_answer\":\"Инструкция. [Источник: " + filepath.ToSlash(filepath.Join(dir, "it_vpn.md")) + "]\"}\n```"
		payload, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"message": map[string]string{"content": content}}}})
		_, _ = w.Write(payload)
	}))
	defer server.Close()
	t.Setenv("AI_API_KEY", "test-key")
	t.Setenv("AI_BASE_URL", server.URL+"/v1/")
	t.Setenv("AI_MODEL", "gemini-2.5-flash")
	result := New(base).Analyze(context.Background(), "Не работает VPN, ошибка 809")
	if result.Department != "IT" || result.Priority != "high" || result.RAGAnswer == nil {
		t.Fatalf("unexpected result: %#v", result)
	}
}

func TestAnalyzeFallsBackAfterNetworkError(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "it_vpn.md"), []byte("# VPN\nОшибка 809."), 0600); err != nil {
		t.Fatal(err)
	}
	base, err := knowledge.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("AI_API_KEY", "test-key")
	t.Setenv("AI_BASE_URL", "http://127.0.0.1:1/v1")
	t.Setenv("AI_MODEL", "gemini-2.5-flash")
	result := New(base).Analyze(context.Background(), "Срочно, не работает VPN, ошибка 809")
	if result.Department != "IT" || result.RAGAnswer == nil {
		t.Fatalf("expected heuristic fallback, got %#v", result)
	}
}

func TestLLMEmptyRAGIsNotReplacedByUnrelatedHeuristicDocument(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "it_vpn.md"), []byte("# VPN\nОшибка сертификата для VPN."), 0600); err != nil {
		t.Fatal(err)
	}
	base, err := knowledge.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"{\"department\":\"General\",\"priority\":\"medium\",\"extracted_summary\":\"Мобильный банк\",\"rag_answer\":null}"}}]}`))
	}))
	defer server.Close()
	t.Setenv("AI_API_KEY", "test-key")
	t.Setenv("AI_BASE_URL", server.URL)
	t.Setenv("AI_MODEL", "gemini-2.5-flash")
	result := New(base).Analyze(context.Background(), "Не открывается мобильный банк на Android, ошибка сертификата SSL_PINNING_ERR")
	if result.RAGAnswer != nil {
		t.Fatalf("unrelated RAG answer returned: %v", result.RAGAnswer)
	}
}

func TestGoogleAIStudioConfigFallback(t *testing.T) {
	t.Setenv("AI_API_KEY", "AIza-test-key")
	t.Setenv("AI_BASE_URL", "https://openrouter.ai/api/v1/")
	t.Setenv("AI_MODEL", "openai/gpt-4o-mini")
	baseURL, model := aiConfig()
	if baseURL != "https://generativelanguage.googleapis.com/v1beta/openai" || model != "gemini-3.8-flash" {
		t.Fatalf("unexpected Google config: %q %q", baseURL, model)
	}
}

func TestHeuristicRoutesAllDepartmentsAndLeavesUnknownGeneral(t *testing.T) {
	base, err := knowledge.Load(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("AI_API_KEY", "")
	tests := []struct{ name, text, department, priority string }{
		{"billing", "Дважды списались деньги за тариф, нужен возврат", "Billing", "medium"},
		{"it", "Упал сервер хостера, ошибка 502, база данных недоступна", "IT", "high"},
		{"hr", "Оформление декретного отпуска и перевод в другой отдел", "HR", "medium"},
		{"accounting", "Нужен налоговый вычет и акт сверки", "Accounting", "medium"},
		{"facilities", "Сломался кондиционер, нужна новая мебель", "Facilities", "medium"},
		{"general", "Как организовать общий корпоративный клуб", "Unassigned", "medium"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result := New(base).Analyze(context.Background(), test.text)
			if result.Department != test.department || result.Priority != test.priority {
				t.Fatalf("got %#v", result)
			}
		})
	}
}

func TestHeuristicRoutesBillingSeparatelyFromIT(t *testing.T) {
	base, err := knowledge.Load(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("AI_API_KEY", "")
	result := New(base).Analyze(context.Background(), "С корпоративной карты дважды списалась оплата за тариф Pro, верните деньги на баланс")
	if result.Department != "Billing" || result.Priority != "medium" {
		t.Fatalf("unexpected billing result: %#v", result)
	}
}

func TestGoogleNativeRESTDiscoversFlashModelAfter404(t *testing.T) {
	dir := t.TempDir()
	base, err := knowledge.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	var calls []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls = append(calls, r.Method+" "+r.URL.Path)
		if r.URL.Path == "/v1beta/models/gemini-3.8-flash:generateContent" {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":{"message":"model unavailable"}}`))
			return
		}
		if r.URL.Path == "/v1beta/models" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"models":[{"name":"models/gemini-2.5-flash","supportedGenerationMethods":["generateContent"]}]}`))
			return
		}
		if r.URL.Path == "/v1beta/models/gemini-2.5-flash:generateContent" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"candidates":[{"content":{"parts":[{"text":"{\"department\":\"Billing\",\"priority\":\"medium\",\"extracted_summary\":\"Оплата\",\"rag_answer\":null}"}]}}]}`))
			return
		}
		http.NotFound(w, r)
	}))
	defer server.Close()
	t.Setenv("AI_API_KEY", "AIza-test")
	t.Setenv("GEMINI_API_KEY", "")
	t.Setenv("AI_BASE_URL", "")
	t.Setenv("AI_GOOGLE_BASE_URL", server.URL+"/v1beta")
	t.Setenv("AI_MODEL", "gemini-3.8-flash")
	result := New(base).Analyze(context.Background(), "Дважды списали оплату за тариф")
	if result.Department != "Billing" || len(calls) != 3 {
		t.Fatalf("unexpected result/calls: %#v %v", result, calls)
	}
}

func TestGeminiAPIKeyEnvironmentVariable(t *testing.T) {
	t.Setenv("GEMINI_API_KEY", "AIza-from-gemini")
	t.Setenv("AI_API_KEY", "")
	if got := googleAPIKey(); got != "AIza-from-gemini" {
		t.Fatalf("unexpected key: %q", got)
	}
}

func TestLiveGoogleAIStudioLLM(t *testing.T) {
	if os.Getenv("FLOWOPS_LIVE_LLM") != "1" {
		t.Skip("set FLOWOPS_LIVE_LLM=1 to run the live Google AI Studio check")
	}
	loadTestDotEnv(t)
	if !strings.HasPrefix(os.Getenv("AI_API_KEY"), "AIza") {
		t.Skip("AI_API_KEY is not a Google AI Studio key")
	}
	base, err := knowledge.Load(filepath.Join("..", "..", "knowledge_base"))
	if err != nil {
		t.Fatal(err)
	}
	result, err := New(base).analyzeWithAI(context.Background(), "Срочно! Не работает корпоративный VPN, ошибка 809.")
	if err != nil {
		t.Fatal(err)
	}
	if !New(base).valid(result) {
		t.Fatalf("live LLM returned invalid result: %#v", result)
	}
}

func loadTestDotEnv(t *testing.T) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", ".env"))
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if ok && os.Getenv(strings.TrimSpace(key)) == "" {
			_ = os.Setenv(strings.TrimSpace(key), strings.Trim(strings.TrimSpace(value), "\"'"))
		}
	}
}
