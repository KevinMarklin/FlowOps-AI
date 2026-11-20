package analyzer

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"flowops-ai/internal/knowledge"
)

const gigachatTokenRefreshBuffer = 60 * time.Second

type gigachatConfig struct {
	authKey  string
	scope    string
	oauthURL string
	baseURL  string
	model    string
	insecure bool
}

type gigachatTokenResponse struct {
	AccessToken string `json:"access_token"`
	ExpiresAt   int64  `json:"expires_at"`
}

func loadGigaChatConfig() gigachatConfig {
	config := gigachatConfig{
		authKey:  strings.TrimSpace(os.Getenv("GIGACHAT_AUTH_KEY")),
		scope:    strings.TrimSpace(os.Getenv("GIGACHAT_SCOPE")),
		oauthURL: strings.TrimSpace(os.Getenv("GIGACHAT_OAUTH_URL")),
		baseURL:  strings.TrimRight(strings.TrimSpace(os.Getenv("GIGACHAT_BASE_URL")), "/"),
		model:    strings.TrimSpace(os.Getenv("GIGACHAT_MODEL")),
		insecure: true,
	}
	if config.scope == "" {
		config.scope = "GIGACHAT_API_PERS"
	}
	if config.oauthURL == "" {
		config.oauthURL = "https://ngw.devices.sberbank.ru:9443/api/v2/oauth"
	}
	if config.baseURL == "" {
		config.baseURL = "https://api.giga.chat/v1"
	}
	if config.model == "" {
		config.model = "GigaChat"
	}
	if value := strings.TrimSpace(os.Getenv("GIGACHAT_INSECURE_TLS")); value != "" {
		config.insecure = value == "1" || strings.EqualFold(value, "true") || strings.EqualFold(value, "yes")
	}
	return config
}

func newInsecureGigaChatClient(timeout time.Duration) *http.Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	// GigaChat's certificate chain may not be installed in local/dev images.
	transport.TLSClientConfig = &tls.Config{InsecureSkipVerify: true} //nolint:gosec
	return &http.Client{Timeout: timeout, Transport: transport}
}

func (a *Analyzer) gigachatHTTPClient(insecure bool) *http.Client {
	if insecure {
		return a.insecureClient
	}
	return a.client
}

func (a *Analyzer) gigachatAccessToken(ctx context.Context, config gigachatConfig) (string, error) {
	a.tokenMu.Lock()
	defer a.tokenMu.Unlock()
	if a.gigaAuthKey != config.authKey || a.gigaOAuthURL != config.oauthURL {
		a.gigaToken = ""
		a.gigaExpiresAt = time.Time{}
		a.gigaAuthKey = config.authKey
		a.gigaOAuthURL = config.oauthURL
	}
	if a.gigaToken != "" && time.Now().Add(gigachatTokenRefreshBuffer).Before(a.gigaExpiresAt) {
		return a.gigaToken, nil
	}

	requestID, err := newUUIDv4()
	if err != nil {
		return "", fmt.Errorf("generate GigaChat OAuth request ID: %w", err)
	}
	form := url.Values{"scope": {config.scope}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, config.oauthURL, strings.NewReader(form.Encode()))
	if err != nil {
		return "", fmt.Errorf("create GigaChat OAuth request: %w", err)
	}
	authKey := strings.TrimSpace(config.authKey)
	if !strings.HasPrefix(strings.ToLower(authKey), "basic ") {
		authKey = "Basic " + authKey
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("RqUID", requestID)
	req.Header.Set("Authorization", authKey)

	resp, err := a.gigachatHTTPClient(config.insecure).Do(req)
	if err != nil {
		return "", fmt.Errorf("request GigaChat OAuth token failed")
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("read GigaChat OAuth response failed")
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("GigaChat OAuth status %s", resp.Status)
	}
	var token gigachatTokenResponse
	if err := json.Unmarshal(data, &token); err != nil {
		return "", fmt.Errorf("parse GigaChat OAuth response: %w", err)
	}
	if token.AccessToken == "" || token.ExpiresAt <= 0 {
		return "", fmt.Errorf("GigaChat OAuth response has no valid access_token or expires_at")
	}
	a.gigaToken = token.AccessToken
	a.gigaExpiresAt = time.UnixMilli(token.ExpiresAt)
	if time.Now().Add(gigachatTokenRefreshBuffer).After(a.gigaExpiresAt) {
		a.gigaToken = ""
		return "", fmt.Errorf("GigaChat OAuth token is already expired or expires too soon")
	}
	return a.gigaToken, nil
}

func (a *Analyzer) analyzeWithGigaChat(ctx context.Context, rawText string) (Result, error) {
	config := loadGigaChatConfig()
	if config.authKey == "" {
		return Result{}, fmt.Errorf("GigaChat auth key is not configured")
	}
	token, err := a.gigachatAccessToken(ctx, config)
	if err != nil {
		return Result{}, err
	}
	requestID, err := newUUIDv4()
	if err != nil {
		return Result{}, fmt.Errorf("generate GigaChat request ID: %w", err)
	}
	docs := a.maskedKnowledgeDocuments()
	systemPrompt := "Ты маршрутизатор FlowOps AI. Классифицируй обращение и верни строго валидный JSON без Markdown-обёртки с полями department (IT, Billing, HR, Accounting, Facilities, General, Unassigned), priority (low, medium, high), extracted_summary, proposed_solution и matched_article. proposed_solution и matched_article должны быть null, если документ базы знаний не подходит. matched_article — точный путь файла из контекста. Заполняй решение только по прямо соответствующему документу; не выдумывай инструкции и не подставляй нерелевантные регламенты."
	userPrompt := "Регламенты базы знаний:\n" + strings.Join(docs, "\n\n") + "\n\nОбращение пользователя:\n" + rawText
	payload := map[string]any{
		"model":       config.model,
		"temperature": 0.1,
		"messages": []map[string]string{
			{"role": "system", "content": systemPrompt},
			{"role": "user", "content": userPrompt},
		},
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return Result{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, config.baseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return Result{}, fmt.Errorf("create GigaChat completion request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("X-Request-ID", requestID)
	resp, err := a.gigachatHTTPClient(config.insecure).Do(req)
	if err != nil {
		return Result{}, fmt.Errorf("request GigaChat completion failed")
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return Result{}, fmt.Errorf("read GigaChat completion response failed")
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return Result{}, fmt.Errorf("GigaChat completion status %s", resp.Status)
	}
	var envelope struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(data, &envelope); err != nil {
		return Result{}, fmt.Errorf("parse GigaChat completion response: %w", err)
	}
	if len(envelope.Choices) == 0 {
		return Result{}, fmt.Errorf("GigaChat completion response has no choices")
	}
	var modelResult struct {
		Department       string  `json:"department"`
		Priority         string  `json:"priority"`
		ExtractedSummary string  `json:"extracted_summary"`
		ProposedSolution *string `json:"proposed_solution"`
		MatchedArticle   *string `json:"matched_article"`
		LegacyRAGAnswer  *string `json:"rag_answer"`
	}
	content := cleanJSONContent(envelope.Choices[0].Message.Content)
	if err := json.Unmarshal([]byte(content), &modelResult); err != nil {
		return Result{}, fmt.Errorf("parse GigaChat JSON content: %w", err)
	}
	result := Result{
		Department:       knowledge.NormalizeDepartment(modelResult.Department),
		Priority:         modelResult.Priority,
		ExtractedSummary: modelResult.ExtractedSummary,
	}
	if modelResult.ProposedSolution != nil && strings.TrimSpace(*modelResult.ProposedSolution) != "" && modelResult.MatchedArticle != nil && strings.TrimSpace(*modelResult.MatchedArticle) != "" {
		answer := strings.TrimSpace(*modelResult.ProposedSolution) + "\n\n[Источник: " + strings.TrimSpace(*modelResult.MatchedArticle) + "]"
		result.RAGAnswer = &answer
	} else if modelResult.LegacyRAGAnswer != nil && strings.TrimSpace(*modelResult.LegacyRAGAnswer) != "" {
		result.RAGAnswer = modelResult.LegacyRAGAnswer
	}
	if result.RAGAnswer != nil && strings.TrimSpace(*result.RAGAnswer) == "" {
		result.RAGAnswer = nil
	}
	return result, nil
}

func newUUIDv4() (string, error) {
	var uuid [16]byte
	if _, err := rand.Read(uuid[:]); err != nil {
		return "", err
	}
	uuid[6] = (uuid[6] & 0x0f) | 0x40
	uuid[8] = (uuid[8] & 0x3f) | 0x80
	encoded := hex.EncodeToString(uuid[:])
	return encoded[:8] + "-" + encoded[8:12] + "-" + encoded[12:16] + "-" + encoded[16:20] + "-" + encoded[20:], nil
}
