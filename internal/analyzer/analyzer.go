package analyzer

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
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
	"flowops-ai/internal/settings"
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
	llmSlots      chan struct{}
	budgetMu      sync.Mutex
	budgetStart   time.Time
	budgetCount   int
	base          *knowledge.Base
	client        *http.Client
	modelMu       sync.RWMutex
	googleModel   string
	tokenMu       sync.Mutex
	gigaToken     string
	gigaExpiresAt time.Time
	gigaAuthKey   string
	gigaOAuthURL  string
	providerMu    sync.RWMutex
	activeMode    string
	activeName    string
	activeModel   string
	builderConfig settings.Loader
}

func (a *Analyzer) ResetKnowledge() error { return a.base.Reset() }
func (a *Analyzer) TenantKnowledge(ctx context.Context) (*knowledge.Base, error) {
	return a.base.TenantBase(security.TenantID(ctx))
}

func New(base *knowledge.Base) *Analyzer {
	a := &Analyzer{llmSlots: make(chan struct{}, 4), base: base, client: &http.Client{Timeout: 15 * time.Second, CheckRedirect: rejectProviderRedirect}}
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

func (a *Analyzer) WithBuilderConfig(loader settings.Loader) *Analyzer {
	a.builderConfig = loader
	return a
}

func (a *Analyzer) currentBuilderConfig(ctx context.Context) settings.BuilderConfig {
	return settings.LoadOrDefault(ctx, a.builderConfig)
}

func (a *Analyzer) Analyze(ctx context.Context, rawText string) Result {
	return a.AnalyzeWithSender(ctx, rawText, "")
}

func (a *Analyzer) AnalyzeWithSender(ctx context.Context, rawText, sender string) Result {
	started := time.Now()
	if reason := security.CheckPromptInput(rawText, security.MaxTicketBytes); reason != "" {
		return reviewResult(reason)
	}
	if reason := security.CheckPromptInput(sender, 512); reason != "" {
		return reviewResult("unsafe_sender")
	}
	// Sender identities are routing metadata; never append them to the model input.
	masked := security.MaskPII(rawText)
	builderConfig := a.currentBuilderConfig(ctx)
	providers := a.approvedProviders()
	if !sensitiveModelInputAllowed(rawText, masked) {
		providers = nil
	}
	sentToModel := false
	if len(providers) > 0 && a.acquireModelSlot() {
		defer func() { <-a.llmSlots }()
		modelCtx, cancel := context.WithTimeout(ctx, 8*time.Second)
		defer cancel()
		for i, provider := range providers {
			if i > 0 && os.Getenv("FLOWOPS_AI_ALLOW_FALLBACK") != "true" {
				break
			}
			providerName := strings.TrimSuffix(provider.name, " (РФ контур)")
			if !sentToModel && masked.MaskedCount > 0 {
				slog.Info(fmt.Sprintf("[SECURITY] PII masked: count=%d types=[%s]", masked.MaskedCount, strings.Join(masked.MaskedTypes, ", ")))
			}
			slog.Info(fmt.Sprintf("[AI] Requesting %s (model: %s)...", providerName, provider.model()))
			sentToModel = true
			result, err := provider.analyze(modelCtx, masked.MaskedText, builderConfig)
			if err == nil && !a.valid(result, builderConfig) {
				err = fmt.Errorf("provider returned an invalid or unsafe analysis result")
			}
			if err != nil {
				slog.Error(fmt.Sprintf("[AI] %s failed: %s -> switching to fallback", providerName, security.SafeLogError(err)))
				continue
			}
			result = unmaskResult(result, masked)
			result.PIIMaskedCount, result.PIIMaskedTypes = masked.MaskedCount, masked.MaskedTypes
			result = a.attachSafeAnswer(ctx, result, rawText)
			a.setActiveProvider("llm", provider.name, provider.model())
			slog.Info(fmt.Sprintf("[AI] Analyzed by: %s | department=%s | priority=%s | duration_ms=%d", providerName, result.Department, result.Priority, time.Since(started).Milliseconds()))
			return result
		}
	}
	if !sentToModel && masked.MaskedCount > 0 {
		slog.Info(fmt.Sprintf("[SECURITY] PII masked: count=%d types=[%s]", masked.MaskedCount, strings.Join(masked.MaskedTypes, ", ")))
	}
	// External capacity limits must not disable local classification for every channel.
	result := a.analyzeHeuristicallyForBuilder(rawText, builderConfig)
	result = a.attachSafeAnswer(ctx, result, rawText)
	result = unmaskResult(result, masked)
	result.PIIMaskedTypes = masked.MaskedTypes
	if sentToModel {
		result.PIIMaskedCount = masked.MaskedCount
	}
	a.setActiveProvider("heuristic_fallback", "Локальный анализатор", "")
	slog.Info(fmt.Sprintf("[AI] Analyzed by: Local Heuristic (fallback) | department=%s | priority=%s | duration_ms=%d", result.Department, result.Priority, time.Since(started).Milliseconds()))
	return result
}

// AnalyzeSandbox runs the same local/LLM classification and RAG lookup as a
// ticket without persisting state or dispatching messages.
func (a *Analyzer) AnalyzeSandbox(ctx context.Context, message string) Result {
	if reason := security.CheckPromptInput(message, security.MaxTicketBytes); reason != "" {
		return reviewResult(reason)
	}
	masked := security.MaskPII(message)
	builderConfig := a.currentBuilderConfig(ctx)
	providers := a.approvedProviders()
	if !sensitiveModelInputAllowed(message, masked) {
		providers = nil
	}
	if len(providers) > 0 && a.acquireModelSlot() {
		defer func() { <-a.llmSlots }()
		modelCtx, cancel := context.WithTimeout(ctx, 8*time.Second)
		defer cancel()
		for i, provider := range providers {
			if i > 0 && os.Getenv("FLOWOPS_AI_ALLOW_FALLBACK") != "true" {
				break
			}
			result, err := provider.analyze(modelCtx, masked.MaskedText, builderConfig)
			if err == nil && !a.valid(result, builderConfig) {
				err = fmt.Errorf("provider returned an invalid analysis result")
			}
			if err != nil {
				continue
			}
			result = unmaskResult(result, masked)
			result.PIIMaskedCount = masked.MaskedCount
			result.PIIMaskedTypes = masked.MaskedTypes
			result = a.attachSafeAnswer(ctx, result, message)
			a.setActiveProvider("llm", provider.name, provider.model())
			return result
		}
	}
	result := a.analyzeHeuristicallyForBuilder(message, builderConfig)
	result = a.attachSafeAnswer(ctx, result, message)
	result = unmaskResult(result, masked)
	result.PIIMaskedCount = masked.MaskedCount
	result.PIIMaskedTypes = masked.MaskedTypes
	a.setActiveProvider("heuristic_fallback", "Локальный анализатор", "")
	return result
}

func unmaskResult(result Result, masked security.MaskResult) Result {
	result.ExtractedSummary = security.UnmaskPII(result.ExtractedSummary, masked.Replacements)
	return result
}

func (a *Analyzer) setProviderForConfig() {
	providers := a.approvedProviders()
	if len(providers) > 0 {
		a.setActiveProvider("llm", providers[0].name, providers[0].model())
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

func (a *Analyzer) analyzeWithGoogle(ctx context.Context, rawText, apiKey string, builderConfig settings.BuilderConfig) (Result, error) {
	model := a.currentGoogleModel()
	result, status, err := a.googleGenerate(ctx, rawText, apiKey, model, builderConfig)
	if err == nil {
		return result, nil
	}
	if status != http.StatusNotFound {
		return Result{}, fmt.Errorf("Google AI request failed: %w", err)
	}
	discovered, discoverErr := a.discoverGoogleModel(ctx, apiKey)
	if discoverErr != nil {
		return Result{}, fmt.Errorf("Google AI model discovery failed for %s: %w", model, discoverErr)
	}
	a.modelMu.Lock()
	a.googleModel = discovered
	a.modelMu.Unlock()
	result, _, err = a.googleGenerate(ctx, rawText, apiKey, discovered, builderConfig)
	return result, err
}

func (a *Analyzer) googleGenerate(ctx context.Context, rawText, apiKey, model string, builderConfig settings.BuilderConfig) (Result, int, error) {
	base := strings.TrimRight(strings.TrimSpace(os.Getenv("AI_GOOGLE_BASE_URL")), "/")
	if base == "" {
		base = "https://generativelanguage.googleapis.com/v1beta"
	}
	endpoint := base + "/models/" + url.PathEscape(strings.TrimPrefix(model, "models/")) + ":generateContent?key=" + url.QueryEscape(apiKey)
	payload := map[string]any{
		"systemInstruction": map[string]any{"parts": []map[string]string{{"text": classificationPolicyFor(builderConfig)}}},
		"contents":          []map[string]any{{"role": "user", "parts": []map[string]string{{"text": classificationInput(rawText)}}}},
		"generationConfig":  map[string]any{"responseMimeType": "application/json", "temperature": 0, "maxOutputTokens": 512},
	}
	body, _ := json.Marshal(payload)
	req, err := newProviderRequest(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
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
	responseBody, readErr := readProviderBody(resp.Body)
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
	if err := json.Unmarshal(responseBody, &envelope); err != nil {
		return Result{}, resp.StatusCode, fmt.Errorf("invalid Google AI response JSON: %s", security.SafeLogError(err))
	}
	if len(envelope.Candidates) == 0 || len(envelope.Candidates[0].Content.Parts) == 0 {
		return Result{}, resp.StatusCode, fmt.Errorf("invalid Google AI response: missing candidates or content")
	}
	result, err := decodeModelResultForDepartments(envelope.Candidates[0].Content.Parts[0].Text, builderConfig.Departments)
	if err != nil {
		return Result{}, resp.StatusCode, err
	}
	slog.Info("Google AI response received", "status", resp.StatusCode, "duration_ms", duration, "model", model)
	return result, resp.StatusCode, nil
}

func (a *Analyzer) discoverGoogleModel(ctx context.Context, apiKey string) (string, error) {
	base := strings.TrimRight(strings.TrimSpace(os.Getenv("AI_GOOGLE_BASE_URL")), "/")
	if base == "" {
		base = "https://generativelanguage.googleapis.com/v1beta"
	}
	req, err := newProviderRequest(ctx, http.MethodGet, base+"/models?key="+url.QueryEscape(apiKey), nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("x-goog-api-key", apiKey)
	resp, err := a.client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	data, _ := readProviderBody(resp.Body)
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
	case hasAny(text, "командиров", "авансовый отчёт", "авансовый отчет", "авансового отчёта", "авансового отчета", "подотч", "зарплат", "аванс", "расчетн", "бухгалтер", "налогов", "налоговый", "вычет", "акт сверки", "сверк"):
		department = "Accounting"
	// Specific equipment terms outrank generic "card" and "error" markers.
	case hasAny(text, "принтер", "картридж", "пропуск", "канцеляр", "ахо", "офис", "кондиционер", "мебел"):
		department = "Facilities"
	case hasAny(text, "оплат", "списан", "деньги", "возврат", "счет", "счёт", "баланс", "карт", "тариф", "платеж", "платёж", "чек"):
		department = "Billing"
	case hasAny(text, "wi-fi", "wifi", "вайфай", "вай-фай", "vpn", "впн", "l2tp", "ipsec", "парол", "1с", "1c", "компьютер", "систем", "ошибк", "сервер", "vps", "хостер", "502", "сайт", "база данных", "бд", "database"):
		department = "IT"
	case hasAny(text, "справк", "отпуск", "кадр", "ндфл", "места работы", "hr"):
		department = "HR"
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
	return result
}

func (a *Analyzer) analyzeHeuristicallyForBuilder(rawText string, config settings.BuilderConfig) Result {
	result := a.analyzeHeuristically(rawText)
	if config.Departments == nil {
		return result
	}
	if len(config.Departments) == 0 {
		result.Department = "General"
		return result
	}
	for _, department := range config.Departments {
		if result.Department == department.Name {
			return result
		}
	}
	text := strings.ToLower(rawText)
	best, bestScore := "", 0
	for _, department := range config.Departments {
		score := departmentTextScore(text, department.Name+" "+department.Description)
		if score > bestScore {
			best, bestScore = department.Name, score
		}
	}
	if bestScore > 0 {
		result.Department = best
	}
	return result
}

func departmentTextScore(text, label string) int {
	textWords := strings.FieldsFunc(strings.ToLower(text), func(r rune) bool {
		return !(r >= 'а' && r <= 'я' || r >= 'А' && r <= 'Я' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9')
	})
	labelWords := strings.FieldsFunc(strings.ToLower(label), func(r rune) bool {
		return !(r >= 'а' && r <= 'я' || r >= 'А' && r <= 'Я' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9')
	})
	score := 0
	for _, wanted := range labelWords {
		if len([]rune(wanted)) < 4 {
			continue
		}
		for _, present := range textWords {
			if len([]rune(present)) >= 4 && (strings.Contains(wanted, present) || strings.Contains(present, wanted)) {
				score++
				break
			}
		}
	}
	return score
}

func (a *Analyzer) analyzeWithAI(ctx context.Context, rawText string, builderConfig settings.BuilderConfig) (Result, error) {
	baseURL, model := aiConfig()
	if baseURL == "" {
		baseURL = "https://api.openai.com/v1"
	}
	if model == "" {
		model = "gpt-4o-mini"
	}
	return a.analyzeWithCompatible(ctx, rawText, builderConfig, baseURL, model, os.Getenv("AI_API_KEY"), "")
}

func (a *Analyzer) analyzeWithCompatible(ctx context.Context, rawText string, builderConfig settings.BuilderConfig, baseURL, model, key, project string) (Result, error) {
	payload := map[string]any{"model": model, "temperature": 0, "max_tokens": 512, "response_format": map[string]string{"type": "json_object"}, "messages": []map[string]string{{"role": "system", "content": classificationPolicyFor(builderConfig)}, {"role": "user", "content": classificationInput(rawText)}}}
	body, _ := json.Marshal(payload)
	req, err := newProviderRequest(ctx, http.MethodPost, baseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return Result{}, err
	}
	req.Header.Set("Authorization", "Bearer "+key)
	if project != "" {
		req.Header.Set("OpenAI-Project", project)
		req.Header.Set("x-data-logging-enabled", "false")
	}
	req.Header.Set("Content-Type", "application/json")
	started := time.Now()
	resp, err := a.client.Do(req)
	if err != nil {
		slog.Error("LLM request failed", "duration_ms", time.Since(started).Milliseconds())
		return Result{}, err
	}
	defer resp.Body.Close()
	responseBody, readErr := readProviderBody(resp.Body)
	duration := time.Since(started).Milliseconds()
	if readErr != nil {
		slog.Error("LLM response read failed", "status", resp.StatusCode, "duration_ms", duration, "error", security.SafeLogError(readErr))
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
	result, err := decodeModelResultForDepartments(envelope.Choices[0].Message.Content, builderConfig.Departments)
	if err != nil {
		return Result{}, err
	}
	slog.Info("LLM response received", "status", resp.StatusCode, "duration_ms", duration)
	return result, nil
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

func (a *Analyzer) valid(result Result, configs ...settings.BuilderConfig) bool {
	if len(configs) > 0 && configs[0].Departments != nil {
		found := false
		for _, department := range configs[0].Departments {
			if result.Department == department.Name {
				found = true
				break
			}
		}
		if !found && result.Department != "General" && result.Department != "Unassigned" {
			return false
		}
	} else {
		switch result.Department {
		case "IT", "Billing", "HR", "Accounting", "Facilities", "General", "Unassigned":
		default:
			return false
		}
	}
	return (result.Priority == "low" || result.Priority == "medium" || result.Priority == "high") && len(result.ExtractedSummary) <= 1024 && security.SafeAnswerText(result.ExtractedSummary) && result.RAGAnswer == nil
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
