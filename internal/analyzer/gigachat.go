package analyzer

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
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
		insecure: false,
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
	req, err := newProviderRequest(ctx, http.MethodPost, config.oauthURL, strings.NewReader(form.Encode()))
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

	resp, err := a.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("request GigaChat OAuth token failed")
	}
	defer resp.Body.Close()
	data, err := readProviderBody(resp.Body)
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
	payload := map[string]any{
		"model":       config.model,
		"temperature": 0.1,
		"max_tokens":  512,
		"messages": []map[string]string{
			{"role": "system", "content": classificationPolicy},
			{"role": "user", "content": classificationInput(rawText)},
		},
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return Result{}, err
	}
	req, err := newProviderRequest(ctx, http.MethodPost, config.baseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return Result{}, fmt.Errorf("create GigaChat completion request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("X-Request-ID", requestID)
	resp, err := a.client.Do(req)
	if err != nil {
		return Result{}, fmt.Errorf("request GigaChat completion failed")
	}
	defer resp.Body.Close()
	data, err := readProviderBody(resp.Body)
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
	return decodeModelResult(envelope.Choices[0].Message.Content)
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
