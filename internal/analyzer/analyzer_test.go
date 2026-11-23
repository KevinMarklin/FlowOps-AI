package analyzer

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"flowops-ai/internal/knowledge"
	"flowops-ai/internal/security"
)

func TestAnalyzeVPNSummaryAndRAGSource(t *testing.T) {
	allowTestProviders(t)
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
	allowTestProviders(t)
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

func TestAnalyzeWithGeminiMockAndTrailingSlash(t *testing.T) {
	allowTestProviders(t)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "it_vpn.md"), []byte("# VPN\nИнструкция для ошибки 809."), 0600); err != nil {
		t.Fatal(err)
	}
	base, err := knowledge.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	server := providerTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
	allowTestProviders(t)
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
	allowTestProviders(t)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "it_vpn.md"), []byte("# VPN\nОшибка сертификата для VPN."), 0600); err != nil {
		t.Fatal(err)
	}
	base, err := knowledge.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	server := providerTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
	allowTestProviders(t)
	t.Setenv("AI_API_KEY", "AIza-test-key")
	t.Setenv("AI_BASE_URL", "https://openrouter.ai/api/v1/")
	t.Setenv("AI_MODEL", "openai/gpt-4o-mini")
	baseURL, model := aiConfig()
	if baseURL != "https://generativelanguage.googleapis.com/v1beta/openai" || model != "gemini-3.8-flash" {
		t.Fatalf("unexpected Google config: %q %q", baseURL, model)
	}
}

func TestHeuristicRoutesAllDepartmentsAndLeavesUnknownGeneral(t *testing.T) {
	allowTestProviders(t)
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
	allowTestProviders(t)
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
	allowTestProviders(t)
	dir := t.TempDir()
	base, err := knowledge.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	var calls []string
	server := providerTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
	allowTestProviders(t)
	t.Setenv("GEMINI_API_KEY", "AIza-from-gemini")
	t.Setenv("AI_API_KEY", "")
	if got := googleAPIKey(); got != "AIza-from-gemini" {
		t.Fatalf("unexpected key: %q", got)
	}
}

func TestAnalyzeSendsMaskedTextToGeminiAndUnmasksSummary(t *testing.T) {
	allowTestProviders(t)
	base, err := knowledge.Load(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	server := providerTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Contents []struct {
				Parts []struct {
					Text string `json:"text"`
				} `json:"parts"`
			} `json:"contents"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		prompt := body.Contents[0].Parts[0].Text
		if strings.Contains(prompt, "ivanov@example.ru") || strings.Contains(prompt, "+7 999 123-45-67") || !strings.Contains(prompt, "[EMAIL_1]") || !strings.Contains(prompt, "[PHONE_1]") {
			t.Errorf("Gemini received unmasked PII: %q", prompt)
		}
		_, _ = w.Write([]byte(`{"candidates":[{"content":{"parts":[{"text":"{\"department\":\"General\",\"priority\":\"medium\",\"extracted_summary\":\"[EMAIL_1] обращается по номеру [PHONE_1]\",\"rag_answer\":null}"}]}}]}`))
	}))
	defer server.Close()
	t.Setenv("GIGACHAT_AUTH_KEY", "")
	t.Setenv("GEMINI_API_KEY", "gemini-test-key")
	t.Setenv("AI_API_KEY", "")
	t.Setenv("AI_GOOGLE_BASE_URL", server.URL)
	result := New(base).Analyze(context.Background(), "Пишите ivanov@example.ru, телефон +7 999 123-45-67")
	if !strings.Contains(result.ExtractedSummary, "ivanov@example.ru") || !strings.Contains(result.ExtractedSummary, "+7 999 123-45-67") || result.PIIMaskedCount != 2 {
		t.Fatalf("PII was not restored or counted: %#v", result)
	}
	if !strings.Contains(result.ExtractedSummary, "ivanov@example.ru") {
		t.Fatal("email placeholder not restored")
	}
	if strings.Contains(security.MaskPII("email ivanov@example.ru").MaskedText, "ivanov@example.ru") {
		t.Fatal("email masking regression")
	}
}

func TestAnalyzeDoesNotSendSenderEmailToGemini(t *testing.T) {
	allowTestProviders(t)
	base, err := knowledge.Load(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	server := providerTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		data, _ := io.ReadAll(r.Body)
		if strings.Contains(string(data), "sender.private@example.ru") {
			t.Errorf("Gemini request contains sender email: %s", data)
		}
		_, _ = w.Write([]byte(`{"candidates":[{"content":{"parts":[{"text":"{\"department\":\"General\",\"priority\":\"medium\",\"extracted_summary\":\"Вопрос от [EMAIL_1]\",\"rag_answer\":null}"}]}}]}`))
	}))
	defer server.Close()
	t.Setenv("GIGACHAT_AUTH_KEY", "")
	t.Setenv("GEMINI_API_KEY", "gemini-test-key")
	t.Setenv("AI_API_KEY", "")
	t.Setenv("AI_GOOGLE_BASE_URL", server.URL)
	result := New(base).Analyze(context.Background(), "Вопрос от sender.private@example.ru")
	if !strings.Contains(result.ExtractedSummary, "sender.private@example.ru") || result.PIIMaskedCount != 1 {
		t.Fatalf("expected sender value restored for display: %#v", result)
	}
}

func TestHeuristicFallbackReturnsPIIMetrics(t *testing.T) {
	allowTestProviders(t)
	base, err := knowledge.Load(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("GIGACHAT_AUTH_KEY", "")
	t.Setenv("GEMINI_API_KEY", "")
	t.Setenv("AI_API_KEY", "")
	result := New(base).Analyze(context.Background(), "Иванов Иван Иванович звонит по номеру 8 (999) 123-45-67")
	if result.PIIMaskedCount != 0 || len(result.PIIMaskedTypes) < 1 || !strings.Contains(strings.Join(result.PIIMaskedTypes, ","), "ФИО") || !strings.Contains(result.ExtractedSummary, "Иванов Иван Иванович") {
		t.Fatalf("heuristic path did not return restored PII metrics: %#v", result)
	}
}

func TestGigaChatOAuthCachingAndCompletionParsing(t *testing.T) {
	allowTestProviders(t)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "it_vpn.md"), []byte("# VPN\nИнструкция по восстановлению VPN для ошибки 809."), 0600); err != nil {
		t.Fatal(err)
	}
	base, err := knowledge.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	tokenRequests, completionRequests := 0, 0
	server := providerTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/oauth":
			tokenRequests++
			if r.Method != http.MethodPost || r.Header.Get("Authorization") != "Basic base64-auth-key" || r.Header.Get("Content-Type") != "application/x-www-form-urlencoded" {
				t.Errorf("unexpected OAuth request: %s %v", r.Method, r.Header)
			}
			if _, err := url.ParseQuery("scope=" + r.FormValue("scope")); err != nil || r.FormValue("scope") != "test-scope" {
				t.Errorf("unexpected OAuth scope: %q", r.FormValue("scope"))
			}
			if !validUUIDv4(r.Header.Get("RqUID")) {
				t.Errorf("invalid OAuth RqUID: %q", r.Header.Get("RqUID"))
			}
			_ = json.NewEncoder(w).Encode(gigachatTokenResponse{AccessToken: "access-token", ExpiresAt: time.Now().Add(30 * time.Minute).UnixMilli()})
		case "/v1/chat/completions":
			completionRequests++
			if r.Header.Get("Authorization") != "Bearer access-token" || !validUUIDv4(r.Header.Get("X-Request-ID")) {
				t.Errorf("unexpected completion headers: %v", r.Header)
			}
			var request struct {
				Model       string  `json:"model"`
				Temperature float64 `json:"temperature"`
				Messages    []struct {
					Content string `json:"content"`
				} `json:"messages"`
			}
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				t.Errorf("decode completion body: %v", err)
			}
			if request.Model != "GigaChat-Pro" || request.Temperature != 0.1 {
				t.Errorf("unexpected completion request: %#v", request)
			}
			allPrompt := ""
			for _, message := range request.Messages {
				allPrompt += message.Content
			}
			if strings.Contains(allPrompt, "ivanov@example.ru") || strings.Contains(allPrompt, "+7 (999) 123-45-67") || !strings.Contains(allPrompt, "[EMAIL_1]") || !strings.Contains(allPrompt, "[PHONE_1]") {
				t.Errorf("GigaChat received unmasked PII: %q", allPrompt)
			}
			content := "```json\n{\"department\":\"Billing\",\"priority\":\"medium\",\"extracted_summary\":\"Заявка от [EMAIL_1], телефон [PHONE_1]\",\"proposed_solution\":null,\"matched_article\":null}\n```"
			_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]string{"content": content}}}})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	t.Setenv("GIGACHAT_AUTH_KEY", "base64-auth-key")
	t.Setenv("GIGACHAT_SCOPE", "test-scope")
	t.Setenv("GIGACHAT_OAUTH_URL", server.URL+"/oauth")
	t.Setenv("GIGACHAT_BASE_URL", server.URL+"/v1")
	t.Setenv("GIGACHAT_MODEL", "GigaChat-Pro")
	t.Setenv("GIGACHAT_INSECURE_TLS", "false")
	t.Setenv("GEMINI_API_KEY", "")
	t.Setenv("AI_API_KEY", "")
	a := New(base)
	for i := 0; i < 2; i++ {
		result := a.Analyze(context.Background(), "Заявка от ivanov@example.ru, телефон +7 (999) 123-45-67")
		if result.Department != "Billing" || result.Priority != "medium" || !strings.Contains(result.ExtractedSummary, "ivanov@example.ru") || !strings.Contains(result.ExtractedSummary, "+7 (999) 123-45-67") || result.PIIMaskedCount != 2 || result.RAGAnswer != nil {
			t.Fatalf("unexpected GigaChat result: %#v", result)
		}
	}
	if tokenRequests != 1 || completionRequests != 2 {
		t.Fatalf("unexpected request counts: OAuth=%d completions=%d", tokenRequests, completionRequests)
	}
	mode, provider, model := a.ActiveProvider()
	if mode != "llm" || provider != "GigaChat (РФ контур)" || model != "GigaChat-Pro" {
		t.Fatalf("unexpected active provider: %q %q %q", mode, provider, model)
	}
}

func TestGigaChatFailureFallsBackToGemini(t *testing.T) {
	allowTestProviders(t)
	base, err := knowledge.Load(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	gigaServer := providerTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"error":"temporarily unavailable"}`))
	}))
	defer gigaServer.Close()
	geminiServer := providerTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/models/gemini-test:generateContent" || r.URL.Query().Get("key") != "gemini-test-key" {
			t.Errorf("unexpected Gemini fallback request: %s %s", r.Method, r.URL.String())
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"candidates":[{"content":{"parts":[{"text":"{\"department\":\"Billing\",\"priority\":\"medium\",\"extracted_summary\":\"Оплата тарифа\",\"rag_answer\":null}"}]}}]}`))
	}))
	defer geminiServer.Close()
	t.Setenv("GIGACHAT_AUTH_KEY", "test-auth")
	t.Setenv("GIGACHAT_OAUTH_URL", gigaServer.URL+"/oauth")
	t.Setenv("GIGACHAT_BASE_URL", gigaServer.URL+"/v1")
	t.Setenv("GIGACHAT_INSECURE_TLS", "false")
	t.Setenv("GEMINI_API_KEY", "gemini-test-key")
	t.Setenv("AI_API_KEY", "")
	t.Setenv("AI_GOOGLE_BASE_URL", geminiServer.URL)
	t.Setenv("AI_MODEL", "gemini-test")
	result := New(base).Analyze(context.Background(), "Дважды списана оплата за тариф")
	if result.Department != "Billing" || result.ExtractedSummary != "Оплата тарифа" {
		t.Fatalf("expected Gemini fallback, got %#v", result)
	}
}

func validUUIDv4(value string) bool {
	if len(value) != 36 || value[8] != '-' || value[13] != '-' || value[18] != '-' || value[23] != '-' || value[14] != '4' {
		return false
	}
	return strings.ContainsRune("89ab", rune(value[19]))
}

func TestLiveGoogleAIStudioLLM(t *testing.T) {
	allowTestProviders(t)
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
