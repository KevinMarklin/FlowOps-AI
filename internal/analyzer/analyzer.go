package analyzer

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"flowops-ai/internal/knowledge"
	"flowops-ai/internal/security"
	"flowops-ai/internal/settings"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Result struct {
	AutoResolve      bool     `json:"-"`
	Department       string   `json:"department"`
	Priority         string   `json:"priority"`
	ExtractedSummary string   `json:"extracted_summary"`
	RAGAnswer        *string  `json:"rag_answer"`
	PIIMaskedCount   int      `json:"pii_masked_count"`
	PIIMaskedTypes   []string `json:"pii_masked_types"`
}

type Analyzer struct {
	policyDB       *pgxpool.Pool
	providerLoader func() []approvedProvider
	llmSlots       chan struct{}
	budgetMu       sync.Mutex
	budgetStart    time.Time
	budgetCount    int
	base           *knowledge.Base
	client         *http.Client
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
	builderConfig  settings.Loader
}

func (a *Analyzer) ResetKnowledge() error { return a.base.Reset() }
func (a *Analyzer) TenantKnowledge(ctx context.Context) (*knowledge.Base, error) {
	base, err := a.base.TenantBase(security.TenantID(ctx))
	if err != nil {
		return nil, err
	}
	policy, err := a.aiPolicy(ctx)
	if err != nil {
		return nil, err
	}
	if len(policy.Sources) > 0 {
		base = base.SelectSources(policy.Sources)
	}
	return base, nil
}

func New(base *knowledge.Base) *Analyzer {
	a := &Analyzer{llmSlots: make(chan struct{}, 4), base: base, client: &http.Client{Timeout: 15 * time.Second, CheckRedirect: rejectProviderRedirect}}
	a.setProviderForConfig()
	mode, name, model := a.ActiveProvider()
	if mode == "llm" {
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
	policy, policyErr := a.aiPolicy(ctx)
	if policyErr != nil || settings.Disabled(policy.Classification) || policy.Mode == "manual" || policy.DenyPersonalData && masked.MaskedCount > 0 {
		providers = nil
	}
	if len(providers) > 0 && a.acquireGovernedSlot(ctx) {
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
			result = a.attachSafeAnswer(ctx, result, rawText, builderConfig)
			a.setActiveProvider("llm", provider.name, provider.model())
			slog.Info(fmt.Sprintf("[AI] Analyzed by: %s | department=%s | priority=%s | duration_ms=%d", providerName, result.Department, result.Priority, time.Since(started).Milliseconds()))
			return a.applyAIPolicy(ctx, result)
		}
	}
	if !sentToModel && masked.MaskedCount > 0 {
		slog.Info(fmt.Sprintf("[SECURITY] PII masked: count=%d types=[%s]", masked.MaskedCount, strings.Join(masked.MaskedTypes, ", ")))
	}
	// External capacity limits must not disable local classification for every channel.
	result := a.analyzeHeuristicallyForBuilder(rawText, builderConfig)
	result = a.attachSafeAnswer(ctx, result, rawText, builderConfig)
	result = unmaskResult(result, masked)
	result.PIIMaskedTypes = masked.MaskedTypes
	if sentToModel {
		result.PIIMaskedCount = masked.MaskedCount
	}
	a.setActiveProvider("heuristic_fallback", "Локальный анализатор", "")
	slog.Info(fmt.Sprintf("[AI] Analyzed by: Local Heuristic (fallback) | department=%s | priority=%s | duration_ms=%d", result.Department, result.Priority, time.Since(started).Milliseconds()))
	return a.applyAIPolicy(ctx, result)
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
	policy, policyErr := a.aiPolicy(ctx)
	if policyErr != nil || settings.Disabled(policy.Classification) || policy.Mode == "manual" || policy.DenyPersonalData && masked.MaskedCount > 0 {
		providers = nil
	}
	if len(providers) > 0 && a.acquireGovernedSlot(ctx) {
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
			result = a.attachSafeAnswer(ctx, result, message, builderConfig)
			a.setActiveProvider("llm", provider.name, provider.model())
			return a.applyAIPolicy(ctx, result)
		}
	}
	result := a.analyzeHeuristicallyForBuilder(message, builderConfig)
	result = a.attachSafeAnswer(ctx, result, message, builderConfig)
	result = unmaskResult(result, masked)
	result.PIIMaskedCount = masked.MaskedCount
	result.PIIMaskedTypes = masked.MaskedTypes
	a.setActiveProvider("heuristic_fallback", "Локальный анализатор", "")
	return a.applyAIPolicy(ctx, result)
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
	case hasAny(text, "проектор", "личный кабинет", "образовательный портал", "учётн", "учетн", "wi-fi", "wifi", "вайфай", "вай-фай", "vpn", "впн", "l2tp", "ipsec", "парол", "1с", "1c", "компьютер", "систем", "ошибк", "сервер", "vps", "хостер", "502", "сайт", "база данных", "бд", "database"):
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
	// Organization-specific departments can be more precise than the legacy
	// classifier (for example, an education certificate is not an HR request).
	text := strings.ToLower(rawText)
	legacyScore := 0
	for _, department := range config.Departments {
		switch department.Name {
		case "IT", "Billing", "HR", "Accounting", "Facilities":
			if score := departmentTextScore(text, department.Name+" "+department.Description); score > legacyScore {
				legacyScore = score
			}
		}
	}
	bestSpecialized, specializedScore := "", legacyScore
	for _, department := range config.Departments {
		switch department.Name {
		case "Academic", "Production", "Supply", "Security":
			if score := departmentTextScore(text, department.Description); score > specializedScore {
				bestSpecialized, specializedScore = department.Name, score
			}
		}
	}
	if bestSpecialized != "" {
		result.Department = bestSpecialized
		return result
	}
	for _, department := range config.Departments {
		if result.Department == department.Name {
			return result
		}
	}
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
