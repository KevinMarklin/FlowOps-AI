package analyzer

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"flowops-ai/internal/knowledge"
	"flowops-ai/internal/security"
)

type Result struct {
	Department       string   `json:"department"`
	Priority         string   `json:"priority"`
	ExtractedSummary string   `json:"extracted_summary"`
	RAGAnswer        *string  `json:"rag_answer"`
	PIIMaskedCount   int      `json:"pii_masked_count"`
	PIIMaskedTypes   []string `json:"pii_masked_types"`
}

type Analyzer struct {
	base           *knowledge.Base
	client         *http.Client
	insecureClient *http.Client
	modelMu        sync.RWMutex
	googleModel    string
	tokenMu        sync.Mutex
	gigaToken      string
	gigaExpiresAt  time.Time
	gigaAuthKey    string
	gigaOAuthURL   string
	providerMu     sync.RWMutex
	activeMode     string
	activeName     string
	activeModel    string
}

func (a *Analyzer) ResetKnowledge() error { return a.base.Reset() }

func New(base *knowledge.Base) *Analyzer {
	a := &Analyzer{base: base, client: &http.Client{Timeout: 15 * time.Second}, insecureClient: newInsecureGigaChatClient(15 * time.Second)}
	a.setProviderForConfig()
	mode, name, model := a.ActiveProvider()
	if mode == "llm" {
		if name == "GigaChat (РФ контур)" {
			name = "GigaChat"
		}
		slog.Info("[AI] Active provider: " + name + " (model: " + model + ")")
	}
	return a
}

func (a *Analyzer) Analyze(ctx context.Context, rawText string) Result {
	return a.AnalyzeWithSender(ctx, rawText, "")
}

func (a *Analyzer) AnalyzeWithSender(ctx context.Context, rawText, sender string) Result {
	maskInput := rawText
	if strings.TrimSpace(sender) != "" {
		maskInput += "\nОтправитель: " + sender
	}
	masked := security.MaskPII(maskInput)
	piiSentToAI := false
	if config := loadGigaChatConfig(); config.authKey != "" {
		piiSentToAI = true
		if result, err := a.analyzeWithGigaChat(ctx, masked.MaskedText); err == nil && a.validForText(result, masked.MaskedText) {
			result = unmaskResult(result, masked)
			result.PIIMaskedCount, result.PIIMaskedTypes = masked.MaskedCount, masked.MaskedTypes
			result = a.normalizeRAGSource(result, rawText)
			a.setActiveProvider("llm", "GigaChat (РФ контур)", config.model)
			slog.Info("ticket analyzed", "mode", "llm", "provider", "gigachat", "model", config.model, "department", result.Department, "priority", result.Priority, "rag_found", result.RAGAnswer != nil)
			return result
		} else if err != nil {
			slog.Warn("GigaChat analysis failed, trying Gemini fallback")
			a.setProviderForConfig()
		} else {
			slog.Warn("GigaChat returned invalid analysis, trying Gemini fallback")
			a.setProviderForConfig()
		}
	}
	if apiKey := googleAPIKey(); apiKey != "" {
		piiSentToAI = true
		if result, err := a.analyzeWithGoogle(ctx, masked.MaskedText, apiKey); err == nil && a.validForText(result, masked.MaskedText) {
			result = unmaskResult(result, masked)
			result.PIIMaskedCount, result.PIIMaskedTypes = masked.MaskedCount, masked.MaskedTypes
			result = a.normalizeRAGSource(result, rawText)
			a.setActiveProvider("llm", "Gemini", a.currentGoogleModel())
			slog.Info("ticket analyzed", "mode", "llm", "provider", "google_ai_studio", "model", a.currentGoogleModel(), "department", result.Department, "priority", result.Priority, "rag_found", result.RAGAnswer != nil)
			return result
		} else if err != nil {
			slog.Warn("Google AI Studio analysis failed, using heuristic fallback", "error", "provider request failed")
			a.setActiveProvider("heuristic_fallback", "Локальный анализатор", "")
		} else {
			a.setActiveProvider("heuristic_fallback", "Локальный анализатор", "")
		}
	} else if os.Getenv("AI_API_KEY") != "" {
		piiSentToAI = true
		if result, err := a.analyzeWithAI(ctx, masked.MaskedText); err == nil && a.validForText(result, masked.MaskedText) {
			result = unmaskResult(result, masked)
			result.PIIMaskedCount, result.PIIMaskedTypes = masked.MaskedCount, masked.MaskedTypes
			result = a.normalizeRAGSource(result, rawText)
			_, model := aiConfig()
			provider := "Gemini"
			if !strings.Contains(strings.ToLower(model), "gemini") {
				provider = "AI API"
			}
			a.setActiveProvider("llm", provider, model)
			slog.Info("ticket analyzed", "mode", "llm", "department", result.Department, "priority", result.Priority, "rag_found", result.RAGAnswer != nil)
			return result
		} else if err != nil {
			slog.Warn("LLM analysis failed, using heuristic fallback", "error", "provider request failed")
			a.setActiveProvider("heuristic_fallback", "Локальный анализатор", "")
		} else {
			a.setActiveProvider("heuristic_fallback", "Локальный анализатор", "")
		}
	}
	result := a.analyzeHeuristically(rawText)
	result = unmaskResult(result, masked)
	result.PIIMaskedTypes = masked.MaskedTypes
	if piiSentToAI {
		result.PIIMaskedCount, result.PIIMaskedTypes = masked.MaskedCount, masked.MaskedTypes
	}
	a.setActiveProvider("heuristic_fallback", "Локальный анализатор", "")
	slog.Info("ticket analyzed", "mode", "fallback", "department", result.Department, "priority", result.Priority, "rag_found", result.RAGAnswer != nil)
	return result
}

func unmaskResult(result Result, masked security.MaskResult) Result {
	result.ExtractedSummary = security.UnmaskPII(result.ExtractedSummary, masked.Replacements)
	if result.RAGAnswer != nil {
		answer := security.UnmaskPII(*result.RAGAnswer, masked.Replacements)
		result.RAGAnswer = &answer
	}
	return result
}

func (a *Analyzer) setProviderForConfig() {
	config := loadGigaChatConfig()
	if config.authKey != "" {
		a.setActiveProvider("llm", "GigaChat (РФ контур)", config.model)
		return
	}
	if googleAPIKey() != "" {
		a.setActiveProvider("llm", "Gemini", a.currentGoogleModel())
		return
	}
	a.setActiveProvider("heuristic_fallback", "Локальный анализатор", "")
}

func (a *Analyzer) setActiveProvider(mode, name, model string) {
	a.providerMu.Lock()
	a.activeMode, a.activeName, a.activeModel = mode, name, model
	a.providerMu.Unlock()
}

// ActiveProvider reports the provider used by the most recent successful
// analysis, initialized to the highest-priority configured provider.
func (a *Analyzer) ActiveProvider() (mode, name, model string) {
	a.providerMu.RLock()
	defer a.providerMu.RUnlock()
	return a.activeMode, a.activeName, a.activeModel
}

func googleAPIKey() string {
	if key := strings.TrimSpace(os.Getenv("GEMINI_API_KEY")); key != "" {
		return key
	}
	key := strings.TrimSpace(os.Getenv("AI_API_KEY"))
	if strings.HasPrefix(key, "AIza") || strings.Contains(strings.ToLower(os.Getenv("AI_BASE_URL")), "generativelanguage.googleapis.com") {
		return key
	}
	return ""
}

func (a *Analyzer) currentGoogleModel() string {
	a.modelMu.RLock()
	defer a.modelMu.RUnlock()
	if a.googleModel != "" {
		return a.googleModel
	}
	model := strings.TrimSpace(os.Getenv("AI_MODEL"))
	if model == "" {
		return "gemini-3.8-flash"
	}
	return model
}

func (a *Analyzer) analyzeWithGoogle(ctx context.Context, rawText, apiKey string) (Result, error) {
	model := a.currentGoogleModel()
	result, status, err := a.googleGenerate(ctx, rawText, apiKey, model)
	if err == nil {
		return result, nil
	}
	if status != http.StatusNotFound {
		return Result{}, fmt.Errorf("Google AI request failed with status %d", status)
	}
	discovered, discoverErr := a.discoverGoogleModel(ctx, apiKey)
	if discoverErr != nil {
		return Result{}, fmt.Errorf("Google AI model discovery failed for %s", model)
	}
	a.modelMu.Lock()
	a.googleModel = discovered
	a.modelMu.Unlock()
	result, _, err = a.googleGenerate(ctx, rawText, apiKey, discovered)
	return result, err
}

func (a *Analyzer) googleGenerate(ctx context.Context, rawText, apiKey, model string) (Result, int, error) {
	base := strings.TrimRight(strings.TrimSpace(os.Getenv("AI_GOOGLE_BASE_URL")), "/")
	if base == "" {
		base = "https://generativelanguage.googleapis.com/v1beta"
	}
	endpoint := base + "/models/" + url.PathEscape(strings.TrimPrefix(model, "models/")) + ":generateContent?key=" + url.QueryEscape(apiKey)
	docs := a.maskedKnowledgeDocuments()
	prompt := "Классифицируй обращение и верни только JSON с department (IT, Billing, HR, Accounting, Facilities, General, Unassigned), priority (low, medium, high), extracted_summary и rag_answer. Заполняй rag_answer только при прямом соответствии документа конкретной проблеме. Регламенты:\n" + strings.Join(docs, "\n\n") + "\nОбращение:\n" + rawText
	payload := map[string]any{
		"systemInstruction": map[string]any{"parts": []map[string]string{{"text": "Ты маршрутизатор FlowOps AI. Не подставляй нерелевантные регламенты и не выдумывай ответы."}}},
		"contents":          []map[string]any{{"role": "user", "parts": []map[string]string{{"text": prompt}}}},
		"generationConfig":  map[string]string{"responseMimeType": "application/json"},
	}
	body, _ := json.Marshal(payload)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return Result{}, 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-goog-api-key", apiKey)
	requestCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	started := time.Now()
	req = req.WithContext(requestCtx)
	resp, err := a.client.Do(req)
	if err != nil {
		return Result{}, 0, err
	}
	defer resp.Body.Close()
	responseBody, readErr := io.ReadAll(resp.Body)
	duration := time.Since(started).Milliseconds()
	if readErr != nil {
		return Result{}, resp.StatusCode, readErr
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		slog.Error("Google AI Studio API returned error", "status", resp.StatusCode, "duration_ms", duration)
		return Result{}, resp.StatusCode, fmt.Errorf("Google AI API status: %s", resp.Status)
	}
	var envelope struct {
		Candidates []struct {
			Content struct {
				Parts []struct {
					Text string `json:"text"`
				} `json:"parts"`
			} `json:"content"`
		} `json:"candidates"`
	}
	if err := json.Unmarshal(responseBody, &envelope); err != nil || len(envelope.Candidates) == 0 || len(envelope.Candidates[0].Content.Parts) == 0 {
		return Result{}, resp.StatusCode, fmt.Errorf("invalid Google AI response: %w", err)
	}
	content := cleanJSONContent(envelope.Candidates[0].Content.Parts[0].Text)
	var result Result
	if err := json.Unmarshal([]byte(content), &result); err != nil {
		return Result{}, resp.StatusCode, err
	}
	result.Department = knowledge.NormalizeDepartment(result.Department)
	if result.RAGAnswer != nil && strings.TrimSpace(*result.RAGAnswer) == "" {
		result.RAGAnswer = nil
	}
	slog.Info("Google AI response received", "status", resp.StatusCode, "duration_ms", duration, "model", model)
	return result, resp.StatusCode, nil
}

func (a *Analyzer) discoverGoogleModel(ctx context.Context, apiKey string) (string, error) {
	base := strings.TrimRight(strings.TrimSpace(os.Getenv("AI_GOOGLE_BASE_URL")), "/")
	if base == "" {
		base = "https://generativelanguage.googleapis.com/v1beta"
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/models?key="+url.QueryEscape(apiKey), nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("x-goog-api-key", apiKey)
	resp, err := a.client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("models discovery status: %s: %s", resp.Status, strings.TrimSpace(string(data)))
	}
	var listing struct {
		Models []struct {
			Name      string   `json:"name"`
			Supported []string `json:"supportedGenerationMethods"`
		} `json:"models"`
	}
	if err := json.Unmarshal(data, &listing); err != nil {
		return "", err
	}
	for _, candidate := range listing.Models {
		name := strings.TrimPrefix(candidate.Name, "models/")
		if strings.Contains(strings.ToLower(name), "flash") && (len(candidate.Supported) == 0 || hasString(candidate.Supported, "generateContent")) {
			return name, nil
		}
	}
	return "", fmt.Errorf("no Flash model with generateContent found")
}

func hasString(values []string, expected string) bool {
	for _, value := range values {
		if value == expected {
			return true
		}
	}
	return false
}

func (a *Analyzer) analyzeHeuristically(rawText string) Result {
	text := strings.ToLower(rawText)
	department := "Unassigned"
	switch {
	case hasAny(text, "оплат", "списан", "деньги", "возврат", "счет", "счёт", "баланс", "карт", "тариф", "платеж", "платёж", "чек"):
		department = "Billing"
	case hasAny(text, "vpn", "впн", "l2tp", "ipsec", "парол", "1с", "1c", "компьютер", "систем", "ошибк", "сервер", "vps", "хостер", "502", "сайт", "база данных", "бд", "database"):
		department = "IT"
	case hasAny(text, "справк", "отпуск", "кадр", "ндфл", "места работы", "hr"):
		department = "HR"
	case hasAny(text, "зарплат", "аванс", "расчетн", "бухгалтер", "командиров", "налогов", "налоговый", "вычет", "акт сверки", "сверк"):
		department = "Accounting"
	case hasAny(text, "принтер", "картридж", "пропуск", "канцеляр", "ахо", "офис", "кондиционер", "мебел"):
		department = "Facilities"
	}
	priority := "medium"
	if hasAny(text, "срочно", "созвон", "блокирует работу", "не могу войти", "не работает", "502", "упал", "недоступна", "недоступен") {
		priority = "high"
	} else if hasAny(text, "когда", "подскажите", "информация") {
		priority = "low"
	}
	summary := summarize(rawText)
	if matches := regexp.MustCompile(`(?i)(?:ошибк[аи]?|код)\s*[:#]?\s*(\d{3,5})`).FindStringSubmatch(rawText); len(matches) > 1 && !strings.Contains(strings.ToLower(summary), "код ошибки") {
		summary = strings.TrimSuffix(summary, ".") + " (код ошибки: " + matches[1] + ")."
	}
	result := Result{Department: department, Priority: priority, ExtractedSummary: summary}
	if doc := a.base.FindRelevant(rawText, department); doc != nil {
		slog.Info("knowledge document found", "filename", doc.Filename, "department", doc.Department)
		if doc.Department != "General" && doc.Department != "" {
			result.Department = doc.Department
		}
		answer := strings.TrimSpace(doc.Content) + "\n\n[Источник: «" + doc.Title + "» (" + filepath.ToSlash(filepath.Join("knowledge_base", doc.Filename)) + ")]"
		result.RAGAnswer = &answer
	}
	if result.RAGAnswer == nil {
		slog.Info("knowledge document not found", "department", department)
	}
	return result
}

func (a *Analyzer) analyzeWithAI(ctx context.Context, rawText string) (Result, error) {
	baseURL, model := aiConfig()
	if baseURL == "" {
		baseURL = "https://api.openai.com/v1"
	}
	if model == "" {
		model = "gpt-4o-mini"
	}
	docs := a.maskedKnowledgeDocuments()
	prompt := "Классифицируй обращение. Верни JSON с department (IT, Billing, HR, Accounting, Facilities, General, Unassigned), priority (low, medium, high), extracted_summary и rag_answer. Заполняй rag_answer ТОЛЬКО если один документ прямо решает конкретную систему и проблему пользователя; иначе обязательно верни null или пустую строку. Нельзя выбирать документ по общим словам: ошибка, не работает, проблема, срочно, помогите, сертификат. Для Android, мобильного банка, SSL_PINNING_ERR или Kubernetes категорически запрещено подставлять it_vpn.md, если пользователь явно не пишет про VPN, ВПН, L2TP, IPsec или ошибку 809. Не выдумывай инструкции. Для подходящего документа добавь ссылку [Источник: точный путь]. Регламенты:\n" + strings.Join(docs, "\n\n")
	payload := map[string]any{"model": model, "response_format": map[string]string{"type": "json_object"}, "messages": []map[string]string{{"role": "system", "content": prompt}, {"role": "user", "content": rawText}}}
	body, _ := json.Marshal(payload)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return Result{}, err
	}
	req.Header.Set("Authorization", "Bearer "+os.Getenv("AI_API_KEY"))
	req.Header.Set("Content-Type", "application/json")
	started := time.Now()
	resp, err := a.client.Do(req)
	if err != nil {
		slog.Error("LLM request failed", "duration_ms", time.Since(started).Milliseconds())
		return Result{}, err
	}
	defer resp.Body.Close()
	responseBody, readErr := io.ReadAll(resp.Body)
	duration := time.Since(started).Milliseconds()
	if readErr != nil {
		slog.Error("LLM response read failed", "status", resp.StatusCode, "duration_ms", duration, "error", readErr)
		return Result{}, readErr
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		slog.Error("LLM API returned error", "status", resp.StatusCode, "duration_ms", duration)
		return Result{}, fmt.Errorf("AI API status: %s", resp.Status)
	}
	var envelope struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(responseBody, &envelope); err != nil || len(envelope.Choices) == 0 {
		slog.Error("invalid LLM response", "status", resp.StatusCode, "duration_ms", duration)
		return Result{}, fmt.Errorf("invalid AI response: %w", err)
	}
	var result Result
	content := cleanJSONContent(envelope.Choices[0].Message.Content)
	if err := json.Unmarshal([]byte(content), &result); err != nil {
		slog.Error("invalid LLM JSON", "status", resp.StatusCode, "duration_ms", duration)
		return Result{}, err
	}
	result.Department = knowledge.NormalizeDepartment(result.Department)
	if result.RAGAnswer != nil && strings.TrimSpace(*result.RAGAnswer) == "" {
		result.RAGAnswer = nil
	}
	slog.Info("LLM response received", "status", resp.StatusCode, "duration_ms", duration)
	return result, nil
}

func (a *Analyzer) maskedKnowledgeDocuments() []string {
	documents := a.base.Documents()
	result := make([]string, 0, len(documents))
	for _, doc := range documents {
		content := fmt.Sprintf("Файл: %s\nОтдел: %s\nЗаголовок: %s\nКлючевые слова: %s\n%s", doc.Path, doc.Department, doc.Title, strings.Join(doc.Keywords, ", "), doc.Content)
		result = append(result, security.MaskPII(content).MaskedText)
	}
	return result
}

func aiConfig() (string, string) {
	baseURL := strings.TrimRight(strings.TrimSpace(os.Getenv("AI_BASE_URL")), "/")
	model := strings.TrimSpace(os.Getenv("AI_MODEL"))
	apiKey := strings.TrimSpace(os.Getenv("AI_API_KEY"))
	if strings.HasPrefix(apiKey, "AIza") && (baseURL == "" || strings.Contains(strings.ToLower(baseURL), "openrouter")) {
		baseURL = "https://generativelanguage.googleapis.com/v1beta/openai"
		if model == "" || model == "openai/gpt-4o-mini" {
			slog.Warn("Google AI Studio key detected, switching LLM configuration", "base_url", baseURL, "model", "gemini-3.8-flash")
			model = "gemini-3.8-flash"
		}
	}
	return baseURL, model
}

func cleanJSONContent(content string) string {
	start := strings.Index(content, "{")
	end := strings.LastIndex(content, "}")
	if start >= 0 && end >= start {
		return strings.TrimSpace(content[start : end+1])
	}
	return strings.TrimSpace(content)
}

func (a *Analyzer) valid(result Result) bool {
	result.Department = knowledge.NormalizeDepartment(result.Department)
	validDepartment := result.Department == "IT" || result.Department == "Billing" || result.Department == "HR" || result.Department == "Accounting" || result.Department == "Facilities" || result.Department == "General"
	validPriority := result.Priority == "low" || result.Priority == "medium" || result.Priority == "high"
	if !validDepartment || !validPriority || result.ExtractedSummary == "" {
		return false
	}
	if result.RAGAnswer == nil {
		return true
	}
	expectedFile := map[string]string{
		"IT":         "it_vpn.md",
		"HR":         "hr_certificates.md",
		"Accounting": "accounting_payments.md",
		"Facilities": "office_equipment.md",
	}[result.Department]
	for _, document := range a.base.Documents() {
		matchesDepartment := document.Department == result.Department
		matchesLegacyFile := expectedFile != "" && strings.HasSuffix(document.Path, "/"+expectedFile)
		prettySource := "[Источник: «" + document.Title + "» (" + filepath.ToSlash(filepath.Join("knowledge_base", document.Filename)) + ")]"
		if (matchesDepartment || matchesLegacyFile) && (strings.Contains(*result.RAGAnswer, "[Источник: "+document.Path+"]") || strings.Contains(*result.RAGAnswer, prettySource)) {
			return true
		}
	}
	return false
}

func (a *Analyzer) validForText(result Result, rawText string) bool {
	if !a.valid(result) {
		return false
	}
	if result.RAGAnswer == nil || strings.TrimSpace(*result.RAGAnswer) == "" {
		return true
	}
	document := a.base.FindRelevant(rawText, result.Department)
	if document == nil {
		return false
	}
	prettySource := prettySource(document)
	return strings.Contains(*result.RAGAnswer, "[Источник: "+document.Path+"]") || strings.Contains(*result.RAGAnswer, prettySource)
}

func (a *Analyzer) normalizeRAGSource(result Result, rawText string) Result {
	if result.RAGAnswer == nil {
		return result
	}
	document := a.base.FindRelevant(rawText, result.Department)
	if document == nil {
		return result
	}
	answer := strings.TrimSpace(*result.RAGAnswer)
	answer = strings.ReplaceAll(answer, "[Источник: "+document.Path+"]", prettySource(document))
	result.RAGAnswer = &answer
	return result
}

func prettySource(document *knowledge.Document) string {
	return "[Источник: «" + document.Title + "» (" + filepath.ToSlash(filepath.Join("knowledge_base", document.Filename)) + ")]"
}

func hasAny(text string, terms ...string) bool {
	for _, term := range terms {
		if strings.Contains(text, strings.ToLower(term)) {
			return true
		}
	}
	return false
}

func summarize(text string) string {
	text = strings.Join(strings.Fields(text), " ")
	text = regexp.MustCompile(`(?i)^(?:срочно|помогите|здравствуйте|добрый день)[!,.?\s:-]+`).ReplaceAllString(text, "")
	sentences := regexp.MustCompile(`[.!?]+`).Split(text, -1)
	parts := make([]string, 0, 2)
	for _, sentence := range sentences {
		sentence = strings.TrimSpace(sentence)
		if sentence != "" {
			parts = append(parts, sentence)
		}
		if len(parts) == 2 {
			break
		}
	}
	text = strings.Join(parts, ". ")
	if len([]rune(text)) > 240 {
		text = string([]rune(text)[:240])
	}
	return strings.TrimSpace(text) + "."
}
