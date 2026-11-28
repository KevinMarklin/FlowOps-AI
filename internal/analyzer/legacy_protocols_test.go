package analyzer

import (
	"bytes"
	"context"
	"encoding/json"
	"flowops-ai/internal/security"
	"flowops-ai/internal/settings"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

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
