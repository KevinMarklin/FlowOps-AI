package handlers

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

type IntegrationsHandler struct {
	db              *pgxpool.Pool
	restartChannels func(map[string]string)
}

func NewIntegrationsHandler(db *pgxpool.Pool) *IntegrationsHandler {
	return &IntegrationsHandler{db: db}
}

func (h *IntegrationsHandler) WithChannelRestart(restart func(map[string]string)) *IntegrationsHandler {
	h.restartChannels = restart
	return h
}

type apiKey struct {
	ID            int    `json:"id"`
	Name          string `json:"name"`
	KeyPrefix     string `json:"key_prefix"`
	Scope         string `json:"scope"`
	AllowedOrigin string `json:"allowed_origin"`
	IsActive      bool   `json:"is_active"`
	CreatedAt     any    `json:"created_at"`
	LastUsedAt    any    `json:"last_used_at"`
}

func (h *IntegrationsHandler) Keys(w http.ResponseWriter, r *http.Request) {
	if !h.operatorAuthorized(w, r) {
		return
	}
	switch r.Method {
	case http.MethodGet:
		rows, err := h.db.Query(r.Context(), `SELECT id,name,key_prefix,scope,allowed_origin,is_active,created_at,last_used_at FROM api_keys ORDER BY created_at DESC,id DESC`)
		if err != nil {
			writeError(w, 500, "failed to list API keys")
			return
		}
		defer rows.Close()
		keys := make([]apiKey, 0)
		for rows.Next() {
			var key apiKey
			if err := rows.Scan(&key.ID, &key.Name, &key.KeyPrefix, &key.Scope, &key.AllowedOrigin, &key.IsActive, &key.CreatedAt, &key.LastUsedAt); err != nil {
				writeError(w, 500, "failed to read API keys")
				return
			}
			keys = append(keys, key)
		}
		writeJSON(w, 200, keys)
	case http.MethodPost:
		var request struct {
			Name          string `json:"name"`
			Scope         string `json:"scope"`
			AllowedOrigin string `json:"allowed_origin"`
		}
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&request); err != nil {
			writeError(w, 400, "invalid JSON body")
			return
		}
		request.Name = strings.TrimSpace(request.Name)
		request.AllowedOrigin = strings.TrimSpace(request.AllowedOrigin)
		if request.Name == "" || len(request.Name) > 120 || (request.Scope != "widget" && request.Scope != "ingest") {
			writeError(w, 400, "name and valid scope (widget or ingest) are required")
			return
		}
		if request.Scope == "widget" {
			canonical, err := canonicalOrigin(request.AllowedOrigin)
			if err != nil {
				writeError(w, 400, "widget keys require an allowed_origin origin (http/https scheme and host only)")
				return
			}
			request.AllowedOrigin = canonical
		} else if request.AllowedOrigin != "" {
			writeError(w, 400, "allowed_origin is only supported for widget keys")
			return
		}
		secret := make([]byte, 32)
		if _, err := rand.Read(secret); err != nil {
			writeError(w, 500, "failed to generate API key")
			return
		}
		full := "flw_" + base64.RawURLEncoding.EncodeToString(secret)
		sum := sha256.Sum256([]byte(full))
		prefix := full[:12]
		var id int
		if err := h.db.QueryRow(r.Context(), `INSERT INTO api_keys(name,key_prefix,key_hash,scope,allowed_origin) VALUES($1,$2,$3,$4,$5) RETURNING id`, request.Name, prefix, hex.EncodeToString(sum[:]), request.Scope, request.AllowedOrigin).Scan(&id); err != nil {
			writeError(w, 500, "failed to issue API key")
			return
		}
		writeJSON(w, http.StatusCreated, map[string]any{"id": id, "name": request.Name, "key_prefix": prefix, "scope": request.Scope, "allowed_origin": request.AllowedOrigin, "key": full})
	default:
		w.Header().Set("Allow", "GET, POST")
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func canonicalOrigin(value string) (string, error) {
	parsed, err := url.ParseRequestURI(value)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" || parsed.User != nil || parsed.Path != "" && parsed.Path != "/" || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", errors.New("invalid origin")
	}
	host := strings.ToLower(parsed.Hostname())
	if host == "" {
		return "", errors.New("invalid origin")
	}
	port := parsed.Port()
	if port != "" && !((parsed.Scheme == "https" && port == "443") || (parsed.Scheme == "http" && port == "80")) {
		host = net.JoinHostPort(host, port)
	} else if strings.Contains(host, ":") {
		host = "[" + host + "]"
	}
	return strings.ToLower(parsed.Scheme) + "://" + host, nil
}

var hexColor = regexp.MustCompile(`^#[0-9a-fA-F]{6}$`)

func (h *IntegrationsHandler) WidgetConfig(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "GET, OPTIONS, PUT")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
	w.Header().Set("Vary", "Origin")
	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if r.Method == http.MethodPut {
		var input struct {
			Title    string `json:"title"`
			Color    string `json:"color"`
			Greeting string `json:"greeting"`
		}
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&input); err != nil {
			writeError(w, 400, "invalid JSON body")
			return
		}
		input.Title = strings.TrimSpace(input.Title)
		input.Greeting = strings.TrimSpace(input.Greeting)
		if input.Title == "" || len(input.Title) > 100 || input.Greeting == "" || len(input.Greeting) > 500 || !hexColor.MatchString(input.Color) {
			writeError(w, 400, "title, greeting and a six-digit HEX color are required")
			return
		}
		if _, err := h.db.Exec(r.Context(), `UPDATE app_settings SET widget_title=$1,widget_color=$2,widget_greeting=$3 WHERE id=TRUE`, input.Title, input.Color, input.Greeting); err != nil {
			writeError(w, 500, "failed to save widget settings")
			return
		}
		writeJSON(w, 200, map[string]string{"title": input.Title, "color": input.Color, "greeting": input.Greeting})
		return
	}
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", "GET, OPTIONS, PUT")
		writeError(w, 405, "method not allowed")
		return
	}
	var title, color, greeting string
	if err := h.db.QueryRow(r.Context(), `SELECT widget_title,widget_color,widget_greeting FROM app_settings WHERE id=TRUE`).Scan(&title, &color, &greeting); err != nil {
		writeError(w, 500, "failed to load widget settings")
		return
	}
	writeJSON(w, 200, map[string]string{"title": title, "color": color, "greeting": greeting})
}

func (h *IntegrationsHandler) RevokeKey(w http.ResponseWriter, r *http.Request) {
	if !h.operatorAuthorized(w, r) {
		return
	}
	id, err := strconv.Atoi(r.PathValue("id"))
	if err != nil || id < 1 {
		writeError(w, 400, "invalid API key id")
		return
	}
	tag, err := h.db.Exec(r.Context(), `UPDATE api_keys SET is_active=FALSE WHERE id=$1 AND is_active=TRUE`, id)
	if err != nil {
		writeError(w, 500, "failed to revoke API key")
		return
	}
	if tag.RowsAffected() == 0 {
		writeError(w, 404, "API key not found")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *IntegrationsHandler) BuilderSettings(w http.ResponseWriter, r *http.Request) {
	if !h.operatorAuthorized(w, r) {
		return
	}
	if r.Method == http.MethodGet {
		var raw []byte
		if err := h.db.QueryRow(r.Context(), `SELECT builder_config FROM app_settings WHERE id=TRUE`).Scan(&raw); err != nil {
			writeError(w, 500, "failed to load constructor settings")
			return
		}
		if len(raw) == 0 || string(raw) == "null" {
			raw = []byte(`{"departments":[{"name":"IT","description":"ИТ-поддержка"},{"name":"HR","description":"Кадровые вопросы"},{"name":"Accounting","description":"Бухгалтерия"},{"name":"Facilities","description":"Офис и АХО"}],"tone":"friendly","start_template":"Здравствуйте! Опишите ваш вопрос — я постараюсь помочь.","handoff_template":"Заявка #{ticket_id} направлена в отдел {department}.","signature":"Служба поддержки FlowOps"}`)
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		_, _ = w.Write(raw)
		return
	}
	if r.Method != http.MethodPut {
		w.Header().Set("Allow", "GET, PUT")
		writeError(w, 405, "method not allowed")
		return
	}
	var input struct {
		Departments []struct {
			Name        string `json:"name"`
			Description string `json:"description"`
		} `json:"departments"`
		Tone            string `json:"tone"`
		StartTemplate   string `json:"start_template"`
		HandoffTemplate string `json:"handoff_template"`
		Signature       string `json:"signature"`
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<18))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil {
		writeError(w, 400, "invalid JSON body")
		return
	}
	if len(input.Departments) > 20 || (input.Tone != "friendly" && input.Tone != "formal" && input.Tone != "concise" && input.Tone != "warm") || len(input.StartTemplate) > 2000 || len(input.HandoffTemplate) > 2000 || len(input.Signature) > 500 {
		writeError(w, 400, "constructor settings are invalid or exceed limits")
		return
	}
	seen := map[string]bool{}
	for _, department := range input.Departments {
		name := strings.TrimSpace(department.Name)
		if name == "" || len(name) > 80 || len(department.Description) > 500 || seen[strings.ToLower(name)] {
			writeError(w, 400, "department names must be unique and valid")
			return
		}
		seen[strings.ToLower(name)] = true
	}
	data, err := json.Marshal(input)
	if err != nil {
		writeError(w, 400, "invalid settings")
		return
	}
	if _, err := h.db.Exec(r.Context(), `UPDATE app_settings SET builder_config=$1::jsonb WHERE id=TRUE`, string(data)); err != nil {
		writeError(w, 500, "failed to save constructor settings")
		return
	}
	writeJSON(w, 200, map[string]bool{"saved": true})
}

// Channel settings are operator-only; secret values are never returned by GET.
func (h *IntegrationsHandler) ChannelSettings(w http.ResponseWriter, r *http.Request) {
	if !h.operatorAuthorized(w, r) {
		return
	}
	if r.Method == http.MethodGet {
		var raw []byte
		if err := h.db.QueryRow(r.Context(), `SELECT channel_config FROM app_settings WHERE id=TRUE`).Scan(&raw); err != nil {
			writeError(w, 500, "failed to load channel settings")
			return
		}
		values := map[string]string{}
		if len(raw) > 0 {
			_ = json.Unmarshal(raw, &values)
		}
		configured := map[string]bool{}
		for key, value := range values {
			configured[key] = strings.TrimSpace(value) != ""
		}
		writeJSON(w, 200, map[string]any{"configured": configured})
		return
	}
	if r.Method != http.MethodPut {
		w.Header().Set("Allow", "GET, PUT")
		writeError(w, 405, "method not allowed")
		return
	}
	var values map[string]string
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&values); err != nil {
		writeError(w, 400, "invalid JSON body")
		return
	}
	for key, value := range values {
		if key != "telegram_token" && key != "max_token" && key != "mail_imap_host" && key != "mail_smtp_host" && key != "mail_user" && key != "mail_password" || len(value) > 4096 {
			writeError(w, 400, "invalid channel configuration")
			return
		}
	}
	if _, err := h.db.Exec(r.Context(), `UPDATE app_settings SET channel_config=channel_config || $1::jsonb WHERE id=TRUE`, mustJSON(values)); err != nil {
		writeError(w, 500, "failed to save channel settings")
		return
	}
	if h.restartChannels != nil {
		var raw []byte
		if err := h.db.QueryRow(r.Context(), `SELECT channel_config FROM app_settings WHERE id=TRUE`).Scan(&raw); err == nil {
			all := map[string]string{}
			if json.Unmarshal(raw, &all) == nil {
				h.restartChannels(all)
			}
		}
	}
	writeJSON(w, 200, map[string]bool{"saved": true, "restart_required": true})
}

func mustJSON(value any) string { data, _ := json.Marshal(value); return string(data) }

func (h *IntegrationsHandler) CheckChannel(w http.ResponseWriter, r *http.Request) {
	if !h.operatorAuthorized(w, r) {
		return
	}
	channel := r.PathValue("channel")
	if channel != "telegram" && channel != "max" && channel != "email" {
		writeError(w, 400, "unknown channel")
		return
	}
	var raw []byte
	if err := h.db.QueryRow(r.Context(), `SELECT channel_config FROM app_settings WHERE id=TRUE`).Scan(&raw); err != nil {
		writeError(w, 500, "failed to load channel settings")
		return
	}
	values := map[string]string{}
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &values)
	}
	var endpoint, token string
	switch channel {
	case "telegram":
		token = values["telegram_token"]
		endpoint = "https://api.telegram.org/bot" + token + "/getMe"
	case "max":
		token = values["max_token"]
		endpoint = "https://platform-api2.max.ru/me"
	case "email":
		token = values["mail_password"]
		if values["mail_user"] == "" || values["mail_imap_host"] == "" || values["mail_smtp_host"] == "" {
			writeError(w, 400, "IMAP, SMTP and email settings are required")
			return
		}
		writeJSON(w, 200, map[string]any{"ok": token != "", "channel": channel, "message": "Email settings are present; network login is checked on the next poll."})
		return
	}
	if token == "" {
		writeError(w, 400, "save channel credentials before checking connection")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		writeError(w, 500, "failed to create channel check")
		return
	}
	if channel == "max" {
		request.Header.Set("Authorization", token)
	}
	client := &http.Client{Timeout: 5 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	response, err := client.Do(request)
	if err != nil {
		writeJSON(w, 200, map[string]any{"ok": false, "channel": channel, "message": "Connection check failed"})
		return
	}
	defer response.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 2048))
	writeJSON(w, 200, map[string]any{"ok": response.StatusCode >= 200 && response.StatusCode < 300, "channel": channel, "message": http.StatusText(response.StatusCode)})
}

func (h *IntegrationsHandler) operatorAuthorized(w http.ResponseWriter, r *http.Request) bool {
	token := os.Getenv("FLOWOPS_OPERATOR_TOKEN")
	if len(token) < 32 {
		writeError(w, http.StatusServiceUnavailable, "operator access is disabled")
		return false
	}
	supplied, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !ok || subtle.ConstantTimeCompare([]byte(token), []byte(supplied)) != 1 {
		w.Header().Set("WWW-Authenticate", "Bearer")
		writeError(w, http.StatusUnauthorized, "operator authorization required")
		return false
	}
	return true
}
