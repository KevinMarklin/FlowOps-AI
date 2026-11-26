package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strings"

	"flowops-ai/internal/analyzer"
	"flowops-ai/internal/channels"
	"flowops-ai/internal/knowledge"
	"flowops-ai/internal/security"
	"flowops-ai/internal/settings"
)

type SmartHandler struct {
	base          *knowledge.Base
	analyzer      *analyzer.Analyzer
	generator     func(context.Context, string, string) (string, bool, error)
	builderConfig settings.Loader
}

func (h *SmartHandler) WithBuilderConfig(loader settings.Loader) *SmartHandler {
	h.builderConfig = loader
	return h
}

func NewSmartHandler(base *knowledge.Base, ticketAnalyzer *analyzer.Analyzer) *SmartHandler {
	h := &SmartHandler{base: base, analyzer: ticketAnalyzer}
	h.generator = func(ctx context.Context, prompt, input string) (string, bool, error) {
		return ticketAnalyzer.GenerateStructured(ctx, prompt, input)
	}
	return h
}

type importedArticle struct {
	Title      string   `json:"title"`
	Department string   `json:"department"`
	Keywords   []string `json:"keywords"`
	Content    string   `json:"content_markdown"`
}

const smartImportPrompt = `Ты аккуратный редактор корпоративной базы знаний. Разбей входной текст на 1–10 атомарных, самодостаточных статей: вопрос-ответ или пошаговая инструкция. Пиши строго на русском языке. Не добавляй фактов, которых нет в исходнике. Верни только JSON-массив объектов со строковыми полями title, department, content_markdown и массивом keywords. Не включай персональные данные или плейсхолдеры маскирования как реальные данные.`

func (h *SmartHandler) SmartImport(w http.ResponseWriter, r *http.Request) {
	base, err := h.base.TenantBase(security.TenantID(r.Context()))
	if err != nil {
		writeError(w, 500, "failed to load tenant knowledge base")
		return
	}
	var input struct {
		RawText           string `json:"raw_text"`
		DefaultDepartment string `json:"default_department"`
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, security.MaxDocumentBytes+4096))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil {
		writeError(w, 400, "invalid JSON body")
		return
	}
	input.RawText = strings.TrimSpace(input.RawText)
	if input.RawText == "" || len(input.RawText) > security.MaxDocumentBytes {
		writeError(w, 400, "raw_text is required and must fit within the document limit")
		return
	}
	if reason := security.CheckPromptInput(input.RawText, security.MaxDocumentBytes); reason != "" {
		writeError(w, 400, "source text requires manual security review")
		return
	}
	masked := security.MaskPII(input.RawText)
	articles, usedLLM, err := h.generateArticles(r.Context(), masked.MaskedText, input.DefaultDepartment)
	source := "gigachat"
	if err != nil && usedLLM {
		articles = fallbackArticles(masked.MaskedText, input.DefaultDepartment)
		source = "fallback"
	}
	if !usedLLM {
		source = "fallback"
	}
	if len(articles) < 1 || len(articles) > 10 {
		articles = fallbackArticles(masked.MaskedText, input.DefaultDepartment)
		source = "fallback"
	}
	if len(articles) == 0 {
		writeError(w, 400, "the input did not contain importable knowledge")
		return
	}
	created := make([]knowledgeDocument, 0, len(articles))
	for _, article := range articles {
		article.Title = strings.TrimSpace(article.Title)
		article.Department = strings.TrimSpace(article.Department)
		article.Content = strings.TrimSpace(article.Content)
		for i := range article.Keywords {
			article.Keywords[i] = strings.TrimSpace(article.Keywords[i])
		}
		if article.Department == "" {
			article.Department = input.DefaultDepartment
		}
		if len(article.Keywords) == 0 {
			article.Keywords = extractKeywords(article.Title + " " + article.Content)
		}
		doc, addErr := base.Add(article.Department, article.Title, article.Keywords, article.Content)
		if addErr != nil {
			continue
		}
		created = append(created, knowledgeDocument{Filename: doc.Filename, Department: doc.Department, Title: doc.Title, Keywords: doc.Keywords, Content: doc.Content, Kind: doc.Kind, IncidentState: doc.IncidentState, ExpiresAt: doc.ExpiresAt})
	}
	if len(created) == 0 {
		writeError(w, 400, "generated articles failed knowledge validation")
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"articles": created, "count": len(created), "source": source, "pii_masked_count": masked.MaskedCount, "pii_masked_types": masked.MaskedTypes})
}

func (h *SmartHandler) generateArticles(ctx context.Context, text, department string) ([]importedArticle, bool, error) {
	prompt := fmt.Sprintf("Отдел по умолчанию: %s\n\nМатериал:\n%s", department, text)
	answer, attempted, err := h.generator(ctx, smartImportPrompt, prompt)
	if err != nil || !attempted {
		return nil, attempted, err
	}
	var articles []importedArticle
	answer = strings.TrimSpace(strings.TrimPrefix(strings.TrimSuffix(strings.TrimSpace(answer), "```"), "```json"))
	if err := json.Unmarshal([]byte(answer), &articles); err != nil || len(articles) < 1 || len(articles) > 10 {
		return nil, true, fmt.Errorf("invalid structured article response")
	}
	return articles, true, nil
}

var markdownHeadingLine = regexp.MustCompile(`^#{1,6}\s+(.+?)\s*#*$`)

func fallbackArticles(text, department string) []importedArticle {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	blocks := regexp.MustCompile(`\n\s*\n+`).Split(text, -1)
	articles := make([]importedArticle, 0, 10)
	for _, block := range blocks {
		block = strings.TrimSpace(block)
		if block == "" {
			continue
		}
		lines := strings.Split(block, "\n")
		title := ""
		if match := markdownHeadingLine.FindStringSubmatch(strings.TrimSpace(lines[0])); len(match) > 1 {
			title = strings.TrimSpace(match[1])
			lines = lines[1:]
		}
		content := strings.TrimSpace(strings.Join(lines, "\n"))
		if title == "" {
			title = firstSentence(block, 96)
		}
		if content == "" {
			content = block
		}
		if len(title) > 240 {
			title = string([]rune(title)[:240])
		}
		articles = append(articles, importedArticle{Title: title, Department: department, Keywords: extractKeywords(title + " " + content), Content: content})
		if len(articles) == 10 {
			break
		}
	}
	return articles
}

func firstSentence(text string, max int) string {
	text = strings.Join(strings.Fields(text), " ")
	if i := strings.IndexAny(text, ".!?\n"); i > 0 {
		text = text[:i]
	}
	runes := []rune(text)
	if len(runes) > max {
		text = string(runes[:max])
	}
	return strings.TrimSpace(text)
}

func (h *SmartHandler) Preview(w http.ResponseWriter, r *http.Request) {
	base, err := h.base.TenantBase(security.TenantID(r.Context()))
	if err != nil {
		writeError(w, 500, "failed to load tenant knowledge base")
		return
	}
	var input struct {
		Message string `json:"message"`
		Channel string `json:"channel"`
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, security.MaxTicketBytes+1024))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil {
		writeError(w, 400, "invalid JSON body")
		return
	}
	input.Message = strings.TrimSpace(input.Message)
	if input.Message == "" || len(input.Message) > security.MaxTicketBytes {
		writeError(w, 400, "message is required and must fit within the ticket limit")
		return
	}
	if input.Channel != "telegram" && input.Channel != "max" && input.Channel != "email" {
		writeError(w, 400, "channel must be telegram, max or email")
		return
	}
	analysis := h.analyzer.AnalyzeSandbox(r.Context(), input.Message)
	config := settings.LoadOrDefault(r.Context(), h.builderConfig)
	answer := "Ваш вопрос передан специалисту поддержки."
	if analysis.RAGAnswer != nil {
		answer = channels.AppendSignature(*analysis.RAGAnswer, config.Signature)
	} else {
		answer = channels.BuilderEscalationReplyText(0, analysis.Department, config.HandoffTemplate)
	}
	autoReplied := analysis.RAGAnswer != nil
	var rendered string
	switch input.Channel {
	case "telegram":
		rendered = channels.FormatTelegramHTML(answer)
	case "max":
		rendered = channels.FormatMAX(answer)
	case "email":
		rendered = channels.FormatEmailPreview(input.Message, answer, autoReplied)
	}
	matched := ""
	if analysis.RAGAnswer != nil {
		if doc := base.FindRelevant(input.Message, analysis.Department); doc != nil {
			matched = doc.Title
		}
	}
	writeJSON(w, 200, map[string]any{"matched_kb_title": matched, "department": analysis.Department, "priority": analysis.Priority, "auto_replied": autoReplied, "rendered_preview": rendered, "pii_masked_count": security.MaskPII(input.Message).MaskedCount})
}
