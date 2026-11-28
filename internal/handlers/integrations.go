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
	"fmt"
	"io"
	"net"
	"net/http"
	"net/mail"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"flowops-ai/internal/channels"
	"flowops-ai/internal/security"
	"flowops-ai/internal/settings"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

type IntegrationsHandler struct {
	db              *pgxpool.Pool
	restartChannels func(string, map[string]string)
	validateChannel func(context.Context, string, map[string]string) (string, string, map[string]string, error)
}

func NewIntegrationsHandler(db *pgxpool.Pool) *IntegrationsHandler {
	return &IntegrationsHandler{db: db}
}

func (h *IntegrationsHandler) WithChannelRestart(restart func(string, map[string]string)) *IntegrationsHandler {
	h.restartChannels = restart
	return h
}

func (h *IntegrationsHandler) WithChannelValidator(validate func(context.Context, string, map[string]string) (string, string, map[string]string, error)) *IntegrationsHandler {
	h.validateChannel = validate
	return h
}

func (h *IntegrationsHandler) LoadBuilderConfig(ctx context.Context) (settings.BuilderConfig, error) {
	return settings.LoadBuilderConfig(ctx, func(ctx context.Context) ([]byte, error) {
		var raw []byte
		err := h.db.QueryRow(ctx, `SELECT builder_config FROM app_settings WHERE tenant_id=$1 AND id=TRUE`, security.TenantID(ctx)).Scan(&raw)
		return raw, err
	})
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
		rows, err := h.db.Query(r.Context(), `SELECT id,name,key_prefix,scope,allowed_origin,is_active,created_at,last_used_at FROM api_keys WHERE tenant_id=$1 ORDER BY created_at DESC,id DESC`, security.TenantID(r.Context()))
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
		if err := h.db.QueryRow(r.Context(), `INSERT INTO api_keys(tenant_id,name,key_prefix,key_hash,scope,allowed_origin) VALUES($1,$2,$3,$4,$5,$6) RETURNING id`, security.TenantID(r.Context()), request.Name, prefix, hex.EncodeToString(sum[:]), request.Scope, request.AllowedOrigin).Scan(&id); err != nil {
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
		if _, err := h.db.Exec(r.Context(), `INSERT INTO app_settings(tenant_id,id,widget_title,widget_color,widget_greeting) VALUES($4,TRUE,$1,$2,$3) ON CONFLICT (tenant_id,id) DO UPDATE SET widget_title=EXCLUDED.widget_title,widget_color=EXCLUDED.widget_color,widget_greeting=EXCLUDED.widget_greeting`, input.Title, input.Color, input.Greeting, security.TenantID(r.Context())); err != nil {
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
	if err := h.db.QueryRow(r.Context(), `SELECT widget_title,widget_color,widget_greeting FROM app_settings WHERE tenant_id=$1 AND id=TRUE`, security.TenantID(r.Context())).Scan(&title, &color, &greeting); err != nil {
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
	tag, err := h.db.Exec(r.Context(), `UPDATE api_keys SET is_active=FALSE WHERE id=$1 AND tenant_id=$2 AND is_active=TRUE`, id, security.TenantID(r.Context()))
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
		config, err := h.LoadBuilderConfig(r.Context())
		if err != nil {
			writeError(w, 500, "failed to load constructor settings")
			return
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		_ = json.NewEncoder(w).Encode(config)
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
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		writeError(w, 400, "invalid JSON body")
		return
	}
	if len(input.Departments) > 1000 || (input.Tone != "friendly" && input.Tone != "formal" && input.Tone != "concise" && input.Tone != "warm") || len(input.StartTemplate) > 2000 || len(input.HandoffTemplate) > 2000 || len(input.Signature) > 500 {
		writeError(w, 400, "constructor settings are invalid or exceed limits")
		return
	}
	seen := map[string]bool{}
	for index, department := range input.Departments {
		name := strings.TrimSpace(department.Name)
		if !validDepartment(name) || len(name) > 80 || len(department.Description) > 500 || seen[strings.ToLower(name)] {
			writeError(w, 400, "department names must be unique and valid")
			return
		}
		seen[strings.ToLower(name)] = true
		input.Departments[index].Name = name
	}
	data, err := json.Marshal(input)
	if err != nil {
		writeError(w, 400, "invalid settings")
		return
	}
	if _, err := h.db.Exec(r.Context(), `INSERT INTO app_settings(tenant_id,id,builder_config) VALUES($2,TRUE,$1::jsonb) ON CONFLICT (tenant_id,id) DO UPDATE SET builder_config=EXCLUDED.builder_config`, string(data), security.TenantID(r.Context())); err != nil {
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
		err := h.db.QueryRow(r.Context(), `SELECT channel_config FROM app_settings WHERE tenant_id=$1 AND id=TRUE`, security.TenantID(r.Context())).Scan(&raw)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			writeError(w, 500, "failed to load channel settings")
			return
		}
		values := map[string]string{}
		if len(raw) > 0 {
			_ = json.Unmarshal(raw, &values)
		}
		configured := map[string]bool{}
		for _, key := range []string{"telegram_token", "max_token", "mail_user", "mail_imap_host", "mail_smtp_host"} {
			configured[key] = strings.TrimSpace(values[key]) != ""
		}
		metadata := map[string]map[string]string{}
		if len(raw) > 0 {
			var metadataRaw []byte
			_ = h.db.QueryRow(r.Context(), `SELECT channel_metadata FROM app_settings WHERE tenant_id=$1 AND id=TRUE`, security.TenantID(r.Context())).Scan(&metadataRaw)
			_ = json.Unmarshal(metadataRaw, &metadata)
		}
		for _, channel := range metadata {
			channel["last_error"] = security.SafeChannelError(channel["last_error"])
		}
		writeJSON(w, 200, map[string]any{"configured": configured, "channels": metadata})
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
	tenant := security.TenantID(r.Context())
	channel := channelFromValues(values)
	if channel == "email" {
		values["mail_user"] = strings.ToLower(strings.TrimSpace(values["mail_user"]))
	}
	if channel == "" {
		writeError(w, 400, "exactly one channel configuration is required")
		return
	}
	identity, label, metadata, err := h.validateChannelIdentity(r.Context(), channel, values)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	tx, err := h.db.Begin(r.Context())
	if err != nil {
		writeError(w, 500, "failed to save channel settings")
		return
	}
	defer tx.Rollback(r.Context())
	_, _ = tx.Exec(r.Context(), `DELETE FROM channel_bindings WHERE tenant_id=$1 AND channel=$2`, tenant, channel)
	identities := []string{identity}
	if channel == "telegram" || channel == "max" {
		tokenKey := "telegram_token"
		if channel == "max" {
			tokenKey = "max_token"
		}
		identities = append(identities, channels.ChannelTokenIdentity(channel, values[tokenKey]))
	}
	for _, bindingIdentity := range identities {
		if _, err = tx.Exec(r.Context(), `INSERT INTO channel_bindings(identity,tenant_id,channel) VALUES($1,$2,$3)`, bindingIdentity, tenant, channel); err != nil {
			var pgErr *pgconn.PgError
			if errors.As(err, &pgErr) && pgErr.Code == "23505" {
				_ = tx.Rollback(r.Context())
				writeError(w, http.StatusConflict, fmt.Sprintf("Этот канал (%s) уже используется в другом рабочем пространстве", label))
				return
			}
			writeError(w, 500, "failed to reserve channel identity")
			return
		}
	}
	if _, err = tx.Exec(r.Context(), `INSERT INTO app_settings(tenant_id,id,channel_config,channel_metadata) VALUES($1,TRUE,$2::jsonb,$3::jsonb) ON CONFLICT (tenant_id,id) DO UPDATE SET channel_config=app_settings.channel_config || EXCLUDED.channel_config,channel_metadata=jsonb_set(app_settings.channel_metadata,ARRAY[$4],COALESCE(app_settings.channel_metadata->$4,'{}'::jsonb)||$5::jsonb,TRUE)`, tenant, mustJSON(values), `{}`, channel, mustJSON(metadata)); err != nil {
		writeError(w, 500, "failed to save channel settings")
		return
	}
	if err = tx.Commit(r.Context()); err != nil {
		writeError(w, 500, "failed to save channel settings")
		return
	}
	if h.restartChannels != nil {
		h.restartChannels(tenant, values)
	}
	writeJSON(w, 200, map[string]bool{"saved": true, "restart_required": true})
}

func (h *IntegrationsHandler) DeleteChannel(w http.ResponseWriter, r *http.Request) {
	if !h.operatorAuthorized(w, r) {
		return
	}
	channel := r.PathValue("channel")
	keys := map[string][]string{"telegram": {"telegram_token"}, "max": {"max_token"}, "email": {"mail_imap_host", "mail_smtp_host", "mail_user", "mail_password"}}
	selected, ok := keys[channel]
	if !ok {
		writeError(w, http.StatusBadRequest, "unknown channel")
		return
	}
	tenant := security.TenantID(r.Context())
	tx, err := h.db.Begin(r.Context())
	if err != nil {
		writeError(w, 500, "failed to disable channel")
		return
	}
	defer tx.Rollback(r.Context())
	var raw, metadataRaw []byte
	if err = tx.QueryRow(r.Context(), `SELECT channel_config,channel_metadata FROM app_settings WHERE tenant_id=$1 AND id=TRUE FOR UPDATE`, tenant).Scan(&raw, &metadataRaw); err != nil {
		writeError(w, 500, "failed to load channel settings")
		return
	}
	values, metadata := map[string]string{}, map[string]map[string]string{}
	_ = json.Unmarshal(raw, &values)
	_ = json.Unmarshal(metadataRaw, &metadata)
	for _, key := range selected {
		// Empty values explicitly suppress the default workspace .env fallback.
		values[key] = ""
	}
	delete(metadata, channel)
	if _, err = tx.Exec(r.Context(), `UPDATE app_settings SET channel_config=$1::jsonb,channel_metadata=$2::jsonb WHERE tenant_id=$3 AND id=TRUE`, mustJSON(values), mustJSON(metadata), tenant); err != nil {
		writeError(w, 500, "failed to disable channel")
		return
	}
	if _, err = tx.Exec(r.Context(), `DELETE FROM channel_bindings WHERE tenant_id=$1 AND channel=$2`, tenant, channel); err != nil {
		writeError(w, 500, "failed to release channel identity")
		return
	}
	if err = tx.Commit(r.Context()); err != nil {
		writeError(w, 500, "failed to disable channel")
		return
	}
	if h.restartChannels != nil {
		changed := make(map[string]string, len(selected))
		for _, key := range selected {
			changed[key] = ""
		}
		h.restartChannels(tenant, changed)
	}
	writeJSON(w, http.StatusOK, map[string]bool{"disabled": true})
}

func mustJSON(value any) string { data, _ := json.Marshal(value); return string(data) }

func channelFromValues(values map[string]string) string {
	count, channel := 0, ""
	for _, key := range []string{"telegram_token", "max_token", "mail_user"} {
		if strings.TrimSpace(values[key]) != "" {
			count++
			switch key {
			case "telegram_token":
				channel = "telegram"
			case "max_token":
				channel = "max"
			default:
				channel = "email"
			}
		}
	}
	if count == 1 {
		return channel
	}
	return ""
}

func (h *IntegrationsHandler) loadChannelConfig(ctx context.Context, tenant string) map[string]string {
	var raw []byte
	values := map[string]string{}
	if h.db.QueryRow(ctx, `SELECT channel_config FROM app_settings WHERE tenant_id=$1 AND id=TRUE`, tenant).Scan(&raw) == nil {
		_ = json.Unmarshal(raw, &values)
	}
	return values
}

func (h *IntegrationsHandler) validateAndIdentifyChannel(ctx context.Context, channel string, values map[string]string) (string, string, map[string]string, error) {
	switch channel {
	case "telegram":
		token := strings.TrimSpace(values["telegram_token"])
		var payload struct {
			OK     bool `json:"ok"`
			Result struct {
				ID        int64  `json:"id"`
				Username  string `json:"username"`
				FirstName string `json:"first_name"`
			} `json:"result"`
			Description string `json:"description"`
		}
		if err := getJSON(ctx, "https://api.telegram.org/bot"+token+"/getMe", "", &payload); err != nil || !payload.OK || payload.Result.ID == 0 {
			return "", "", nil, errors.New("Telegram token is invalid or getMe failed")
		}
		var webhook struct {
			OK     bool `json:"ok"`
			Result struct {
				URL string `json:"url"`
			} `json:"result"`
		}
		_ = getJSON(ctx, "https://api.telegram.org/bot"+token+"/getWebhookInfo", "", &webhook)
		identity := fmt.Sprintf("tg:%d", payload.Result.ID)
		label := "@" + payload.Result.Username
		if payload.Result.Username == "" {
			label = payload.Result.FirstName
		}
		meta := map[string]string{"bot_id": fmt.Sprint(payload.Result.ID), "bot_username": payload.Result.Username, "bot_name": payload.Result.FirstName, "status": "connected", "last_error": ""}
		if webhook.Result.URL != "" {
			meta["last_error"] = "Установлен webhook: " + webhook.Result.URL
			meta["status"] = "warning"
		}
		return identity, label, meta, nil
	case "max":
		token := strings.TrimSpace(values["max_token"])
		var payload struct {
			UserID    int64  `json:"user_id"`
			ID        int64  `json:"id"`
			Username  string `json:"username"`
			Name      string `json:"name"`
			FirstName string `json:"first_name"`
		}
		if err := getJSON(ctx, "https://platform-api2.max.ru/me", token, &payload); err != nil {
			return "", "", nil, errors.New("MAX token is invalid or getMe failed")
		}
		id := payload.UserID
		if id == 0 {
			id = payload.ID
		}
		if id == 0 {
			return "", "", nil, errors.New("MAX getMe returned no bot identifier")
		}
		name := payload.Name
		if name == "" {
			name = payload.FirstName
		}
		username := payload.Username
		if username != "" {
			username = "@" + username
		}
		if username == "" {
			username = name
		}
		return fmt.Sprintf("max:%d", id), username, map[string]string{"bot_id": fmt.Sprint(id), "bot_username": payload.Username, "bot_name": name, "status": "connected", "last_error": ""}, nil
	case "email":
		user := strings.TrimSpace(values["mail_user"])
		parsedUser, err := mail.ParseAddress(user)
		if err != nil {
			return "", "", nil, errors.New("invalid email login")
		}
		if strings.TrimSpace(values["mail_password"]) == "" || strings.TrimSpace(values["mail_imap_host"]) == "" || strings.TrimSpace(values["mail_smtp_host"]) == "" {
			return "", "", nil, errors.New("IMAP, SMTP, email and password are required")
		}
		if err := channels.CheckEmailCredentials(ctx, values["mail_imap_host"], values["mail_smtp_host"], user, values["mail_password"]); err != nil {
			return "", "", nil, errors.New("IMAP/SMTP login validation failed")
		}
		host := strings.ToLower(strings.TrimSpace(values["mail_imap_host"]))
		identity := "mail:" + strings.ToLower(parsedUser.Address) + "@" + host
		return identity, strings.ToLower(parsedUser.Address), map[string]string{"email_user": parsedUser.Address, "status": "connected", "last_error": ""}, nil
	}
	return "", "", nil, errors.New("unknown channel")
}

func (h *IntegrationsHandler) validateChannelIdentity(ctx context.Context, channel string, values map[string]string) (string, string, map[string]string, error) {
	if h.validateChannel != nil {
		return h.validateChannel(ctx, channel, values)
	}
	return h.validateAndIdentifyChannel(ctx, channel, values)
}

func getJSON(ctx context.Context, endpoint, authorization string, target any) error {
	checkCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(checkCtx, http.MethodGet, endpoint, nil)
	if err != nil {
		return err
	}
	if authorization != "" {
		request.Header.Set("Authorization", authorization)
	}
	response, err := (&http.Client{Timeout: 5 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}).Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("remote check returned %s", response.Status)
	}
	return json.NewDecoder(io.LimitReader(response.Body, 8192)).Decode(target)
}

func (h *IntegrationsHandler) CheckChannel(w http.ResponseWriter, r *http.Request) {
	if !h.operatorAuthorized(w, r) {
		return
	}
	channel := r.PathValue("channel")
	if channel != "telegram" && channel != "max" && channel != "email" {
		writeError(w, 400, "unknown channel")
		return
	}
	values := h.loadChannelConfig(r.Context(), security.TenantID(r.Context()))
	_, _, metadata, err := h.validateChannelIdentity(r.Context(), channel, values)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "channel": channel, "message": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "channel": channel, "metadata": metadata, "message": metadata["last_error"]})
	return
}

func (h *IntegrationsHandler) operatorAuthorized(w http.ResponseWriter, r *http.Request) bool {
	if p, ok := security.CurrentPrincipal(r.Context()); ok {
		if p.Role == "admin" {
			return true
		}
		writeError(w, http.StatusForbidden, "administrator access required")
		return false
	}
	if security.IsOperatorAuthenticated(r.Context()) {
		return true
	}
	// Keep handler-level callers safe when mounted in isolated unit tests; the
	// production router authenticates every request and supplies its tenant.
	token := os.Getenv("FLOWOPS_OPERATOR_TOKEN")
	supplied, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if len(token) < 32 || !ok || subtle.ConstantTimeCompare([]byte(token), []byte(supplied)) != 1 {
		w.Header().Set("WWW-Authenticate", "Bearer")
		writeError(w, http.StatusUnauthorized, "operator authorization required")
		return false
	}
	return true
}
