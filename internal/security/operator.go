package security

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flowops-ai/internal/settings"
	"github.com/jackc/pgx/v5"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

type tenantContextKey struct{}
type operatorAuthContextKey struct{}

func TenantID(ctx context.Context) string {
	if tenant, ok := ctx.Value(tenantContextKey{}).(string); ok && tenant != "" {
		return tenant
	}
	return "default"
}

// WithTenantID binds background work to the workspace that owns its source.
func WithTenantID(ctx context.Context, tenant string) context.Context {
	if strings.TrimSpace(tenant) == "" {
		tenant = "default"
	}
	return context.WithValue(ctx, tenantContextKey{}, tenant)
}
func IsOperatorAuthenticated(ctx context.Context) bool {
	value, _ := ctx.Value(operatorAuthContextKey{}).(bool)
	return value
}

// ProtectOperatorTenants authenticates each operator token against the stored
// SHA-256 workspace hash and attaches its tenant identity to the request.
func ProtectOperatorTenants(next http.Handler, db *pgxpool.Pool) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		if r.Method == http.MethodPost && (path == "/api/v1/auth/login" || path == "/api/v1/auth/activate" || path == "/api/v1/auth/logout") {
			next.ServeHTTP(w, r)
			return
		}
		if !strings.HasPrefix(path, "/api/") || path == "/api/v1/workspaces" && r.Method == http.MethodPost ||
			path == "/api/v1/widget/config" && r.Method == http.MethodOptions ||
			path == "/api/v1/ingest" && (r.Method == http.MethodPost || r.Method == http.MethodOptions) {
			next.ServeHTTP(w, r)
			return
		}
		value, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		if !ok || value == "" {
			if r.Header.Get("Authorization") == "" {
				p, csrfHash, err := cookiePrincipal(r, db)
				if err == nil {
					// A shared browser cookie may have been replaced by another tab.
					// The member ID is a consistency guard, never an authorization credential.
					if expected := r.Header.Get("X-FlowOps-Member"); expected != "" && expected != p.ID {
						roleJSON(w, http.StatusConflict, map[string]string{"code": "session_changed", "error": "В другой вкладке выполнен вход в другой аккаунт. Войдите заново."})
						return
					}
					if !validSessionWrite(r, csrfHash) {
						ingestError(w, 403, "Проверка безопасности сеанса не пройдена. Обновите страницу.")
						return
					}
					if !authorizePrincipal(w, r, db, p) {
						return
					}
					w.Header().Set("Cache-Control", "no-store")
					next.ServeHTTP(w, r.WithContext(WithPrincipal(r.Context(), p)))
					return
				}
				if !errors.Is(err, pgx.ErrNoRows) {
					ingestError(w, 503, "Сервис авторизации недоступен")
					return
				}
				clearSessionCookies(w, r)
			}
			operatorUnauthorized(w)
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		hash := sha256.Sum256([]byte(value))
		p, err := LookupPrincipal(r.Context(), db, value)
		if err == nil {
			if !authorizePrincipal(w, r, db, p) {
				return
			}
			next.ServeHTTP(w, r.WithContext(WithPrincipal(r.Context(), p)))
			return
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			ingestError(w, 503, "Сервис авторизации недоступен")
			return
		}
		var tenant string
		hashText := hex.EncodeToString(hash[:])
		if path != "/api/v1/widget/config" || r.Method != http.MethodGet || db.QueryRow(r.Context(), `SELECT tenant_id FROM api_keys WHERE key_hash=$1 AND scope='widget' AND allowed_origin=$2 AND is_active=TRUE`, hashText, r.Header.Get("Origin")).Scan(&tenant) != nil {
			operatorUnauthorized(w)
			return
		}
		ctx := WithTenantID(r.Context(), tenant)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func operatorUnauthorized(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("WWW-Authenticate", "Bearer")
	w.WriteHeader(http.StatusUnauthorized)
	_, _ = w.Write([]byte(`{"error":"Operator authorization required"}`))
}

type WorkspaceHandler struct {
	db      *pgxpool.Pool
	mu      sync.Mutex
	windows map[string]workspaceRateWindow
}
type workspaceRateWindow struct {
	started time.Time
	count   int
}

func NewWorkspaceHandler(db *pgxpool.Pool) *WorkspaceHandler {
	return &WorkspaceHandler{db: db, windows: make(map[string]workspaceRateWindow)}
}

func (h *WorkspaceHandler) Me(w http.ResponseWriter, r *http.Request) {
	tenant := TenantID(r.Context())
	var name, organizationType string
	var snapshot []byte
	if err := h.db.QueryRow(r.Context(), `SELECT name,organization_type,template_snapshot FROM workspaces WHERE tenant_id=$1`, tenant).Scan(&name, &organizationType, &snapshot); err != nil {
		http.Error(w, `{"error":"workspace not found"}`, http.StatusUnauthorized)
		return
	}
	organization, _ := settings.Organization(organizationType)
	if len(snapshot) > 2 {
		if err := json.Unmarshal(snapshot, &organization); err != nil {
			ingestError(w, 500, "Не удалось прочитать шаблон организации")
			return
		}
	}
	config, err := settings.LoadBuilderConfig(r.Context(), func(ctx context.Context) ([]byte, error) {
		var raw []byte
		err := h.db.QueryRow(ctx, `SELECT builder_config FROM app_settings WHERE tenant_id=$1 AND id=TRUE`, tenant).Scan(&raw)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return raw, err
	})
	if err != nil {
		ingestError(w, 500, "Не удалось загрузить настройки организации")
		return
	}
	if p, ok := CurrentPrincipal(r.Context()); ok && p.Role != "admin" {
		roleJSON(w, 200, map[string]any{"tenant_id": tenant, "name": name, "organization": organization, "departments": config.Departments, "user": p})
		return
	}
	var tickets, kb int
	_ = h.db.QueryRow(r.Context(), `SELECT COUNT(*) FROM tickets WHERE tenant_id=$1`, tenant).Scan(&tickets)
	entries, _ := os.ReadDir(filepath.Join("knowledge_base", tenant))
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasSuffix(strings.ToLower(entry.Name()), ".md") {
			kb++
		}
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(map[string]any{"tenant_id": tenant, "name": name, "organization": organization, "departments": config.Departments, "is_empty": tickets == 0 && kb == 0, "user": currentUser(r.Context())})
}

// Registration is public: expire old entries and cap distinct IPs so spoofed or
// distributed traffic cannot grow the limiter forever.
func (h *WorkspaceHandler) allowRegistration(ip string, now time.Time) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	for key, window := range h.windows {
		if now.Sub(window.started) >= time.Minute {
			delete(h.windows, key)
		}
	}
	window, exists := h.windows[ip]
	if !exists {
		if len(h.windows) >= 10000 {
			return false
		}
		window = workspaceRateWindow{started: now}
	}
	if window.count >= 10 {
		return false
	}
	window.count++
	h.windows[ip] = window
	return true
}

func (h *WorkspaceHandler) Create(w http.ResponseWriter, r *http.Request) {
	ip := r.RemoteAddr
	if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		ip = host
	}
	if !h.allowRegistration(ip, time.Now()) {
		w.Header().Set("Retry-After", "60")
		http.Error(w, `{"error":"rate limit exceeded"}`, http.StatusTooManyRequests)
		return
	}
	var input struct {
		Name             string `json:"name"`
		OrganizationType string `json:"organization_type"`
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil || strings.TrimSpace(input.Name) == "" || len([]rune(input.Name)) > 120 {
		http.Error(w, `{"error":"valid workspace name is required"}`, http.StatusBadRequest)
		return
	}
	organization, valid := settings.Organization(input.OrganizationType)
	if !valid {
		ingestError(w, 400, "Выберите допустимый тип организации")
		return
	}
	secret := make([]byte, 32)
	if _, err := randRead(secret); err != nil {
		http.Error(w, `{"error":"token generation failed"}`, 500)
		return
	}
	token := "op_" + base64URL(secret)
	hash := sha256.Sum256([]byte(token))
	tenantBytes := make([]byte, 16)
	if _, err := randRead(tenantBytes); err != nil {
		http.Error(w, `{"error":"workspace generation failed"}`, 500)
		return
	}
	tenant := "ws_" + hex.EncodeToString(tenantBytes)
	tx, err := h.db.Begin(r.Context())
	if err != nil {
		ingestError(w, 500, "Не удалось создать компанию")
		return
	}
	defer tx.Rollback(r.Context())
	snapshot, _ := json.Marshal(organization)
	_, err = tx.Exec(r.Context(), `INSERT INTO workspaces(tenant_id,name,token_prefix,token_hash,organization_type,template_snapshot) VALUES($1,$2,$3,$4,$5,$6)`, tenant, strings.TrimSpace(input.Name), token[:11], hex.EncodeToString(hash[:]), organization.Type, snapshot)
	if err != nil {
		http.Error(w, `{"error":"workspace creation failed"}`, 500)
		return
	}
	config, err := json.Marshal(organization.Builder)
	if err != nil {
		ingestError(w, 500, "Не удалось подготовить шаблон")
		return
	}
	if _, err = tx.Exec(r.Context(), `INSERT INTO app_settings(tenant_id,id,builder_config,widget_greeting) VALUES($1,TRUE,$2,$3)`, tenant, config, organization.Builder.StartTemplate); err != nil {
		http.Error(w, `{"error":"workspace initialization failed"}`, 500)
		return
	}
	if err = os.MkdirAll("knowledge_base", 0700); err != nil {
		ingestError(w, 500, "Не удалось подготовить базу знаний")
		return
	}
	directory := filepath.Join("knowledge_base", tenant)
	if err = organization.Seed(directory); err != nil {
		http.Error(w, `{"error":"workspace initialization failed"}`, 500)
		return
	}
	// Seed refuses existing paths and cleans up its own partial writes.
	committed := false
	defer func() {
		if !committed {
			_ = os.RemoveAll(directory)
		}
	}()
	if err = tx.Commit(r.Context()); err != nil {
		ingestError(w, 500, "Не удалось сохранить компанию")
		return
	}
	committed = true
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(map[string]any{"tenant_id": tenant, "name": strings.TrimSpace(input.Name), "token": token, "organization": organization, "starter_documents": len(organization.Documents)})
}

var randRead = func(b []byte) (int, error) { return rand.Read(b) }
var base64URL = func(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

// ProtectOperatorWrites closes the unauthenticated KB-poisoning path. An unset
// or short token disables privileged mutations instead of silently bypassing auth.
// This is a scoped service-token boundary, not a replacement for employee RBAC.
func ProtectOperatorWrites(next http.Handler, token string) http.Handler {
	expected := sha256.Sum256([]byte(token))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		privileged := path == "/api/v1/keys" || strings.HasPrefix(path, "/api/v1/keys/") ||
			path == "/api/v1/kb/smart-import" || path == "/api/v1/settings/preview" ||
			strings.HasPrefix(path, "/api/v1/settings/builder") || strings.HasPrefix(path, "/api/v1/settings/channels") ||
			(path == "/api/v1/widget/config" && r.Method == http.MethodPut) ||
			strings.HasPrefix(path, "/api/knowledge") ||
			(strings.HasPrefix(path, "/api/tickets/") && (r.Method == http.MethodPatch || r.Method == http.MethodDelete || strings.HasSuffix(path, "/reply")))
		if !privileged {
			next.ServeHTTP(w, r)
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		if len(token) < 32 {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(`{"error":"Operator access is disabled: configure FLOWOPS_OPERATOR_TOKEN (at least 32 characters)"}`))
			return
		}
		supplied := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		actual := sha256.Sum256([]byte(supplied))
		if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") || subtle.ConstantTimeCompare(expected[:], actual[:]) != 1 {
			w.Header().Set("WWW-Authenticate", "Bearer")
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":"Operator authorization required"}`))
			return
		}
		next.ServeHTTP(w, r)
	})
}
