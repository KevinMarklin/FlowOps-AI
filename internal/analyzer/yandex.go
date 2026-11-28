package analyzer

import (
	"bytes"
	"context"
	"encoding/json"
	"flowops-ai/internal/security"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"flowops-ai/internal/settings"
)

const yandexBaseURL = "https://ai.api.cloud.yandex.net/v1"

func yandexConfig() (folder, model string, err error) {
	folder = strings.TrimSpace(os.Getenv("YANDEX_FOLDER_ID"))
	model = strings.TrimSpace(os.Getenv("YANDEX_MODEL"))
	if folder == "" || strings.ContainsAny(folder, "/\r\n\t ") {
		return "", "", fmt.Errorf("YANDEX_FOLDER_ID must contain a folder ID")
	}
	if model == "" {
		model = "aliceai-llm"
	}
	if !strings.HasPrefix(model, "gpt://") {
		model = "gpt://" + folder + "/" + model
	}
	if !strings.HasPrefix(model, "gpt://"+folder+"/") || strings.ContainsAny(model, "\r\n\t ") || strings.TrimPrefix(model, "gpt://"+folder+"/") == "" {
		return "", "", fmt.Errorf("YANDEX_MODEL must belong to YANDEX_FOLDER_ID")
	}
	return folder, model, nil
}

func (a *Analyzer) analyzeWithYandex(ctx context.Context, text string, config settings.BuilderConfig) (Result, error) {
	folder, model, err := yandexConfig()
	if err != nil {
		return Result{}, err
	}
	return a.analyzeWithCompatible(ctx, text, config, yandexBaseURL, model, strings.TrimSpace(os.Getenv("YANDEX_API_KEY")), folder)
}

// GenerateStructured is the shared Yandex-only generation path for Copilot,
// knowledge drafts and optimization. Callers validate their domain schema.
func (a *Analyzer) GenerateStructured(ctx context.Context, systemPrompt, userText string) (string, bool, error) {
	masked := security.MaskPII(userText)
	if !sensitiveModelInputAllowed(userText, masked) || !providerAllowed("yandex") || strings.TrimSpace(os.Getenv("YANDEX_API_KEY")) == "" {
		return "", false, nil
	}
	folder, model, err := yandexConfig()
	if err != nil {
		return "", false, err
	}
	if !a.acquireModelSlot() {
		return "", false, nil
	}
	defer func() { <-a.llmSlots }()
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	payload := map[string]any{"model": model, "temperature": 0.1, "max_tokens": 4096, "response_format": map[string]string{"type": "json_object"}, "messages": []map[string]string{{"role": "system", "content": systemPrompt + "\nReturn exactly one JSON object."}, {"role": "user", "content": masked.MaskedText}}}
	body, err := json.Marshal(payload)
	if err != nil {
		return "", false, err
	}
	req, err := newProviderRequest(ctx, http.MethodPost, yandexBaseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return "", false, err
	}
	req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(os.Getenv("YANDEX_API_KEY")))
	req.Header.Set("OpenAI-Project", folder)
	req.Header.Set("x-data-logging-enabled", "false")
	req.Header.Set("Content-Type", "application/json")
	response, err := a.client.Do(req)
	if err != nil {
		return "", true, fmt.Errorf("Yandex generation request failed: %w", err)
	}
	defer response.Body.Close()
	raw, err := readProviderBody(response.Body)
	if err != nil {
		return "", true, err
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return "", true, fmt.Errorf("Yandex generation status %d", response.StatusCode)
	}
	var envelope struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
	}
	if err = json.Unmarshal(raw, &envelope); err != nil || len(envelope.Choices) != 1 {
		return "", true, fmt.Errorf("invalid Yandex response envelope")
	}
	choice := envelope.Choices[0]
	content := strings.TrimSpace(choice.Message.Content)
	if choice.FinishReason == "length" || !json.Valid([]byte(content)) || !strings.HasPrefix(content, "{") {
		return "", true, fmt.Errorf("invalid or truncated Yandex JSON response")
	}
	return content, true, nil
}
