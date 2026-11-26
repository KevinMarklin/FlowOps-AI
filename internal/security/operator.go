package security

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
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
		if !strings.HasPrefix(path, "/api/") || path == "/api/v1/workspaces" && r.Method == http.MethodPost ||
			path == "/api/v1/widget/config" && r.Method == http.MethodOptions {
			next.ServeHTTP(w, r)
			return
		}
		value, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		if !ok || value == "" {
			operatorUnauthorized(w)
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		hash := sha256.Sum256([]byte(value))
		var tenant string
		hashText := hex.EncodeToString(hash[:])
		isWorkspaceToken := true
		if err := db.QueryRow(r.Context(), `SELECT tenant_id FROM workspaces WHERE token_hash=$1`, hashText).Scan(&tenant); err != nil {
			isWorkspaceToken = false
			if path != "/api/v1/widget/config" || r.Method != http.MethodGet || db.QueryRow(r.Context(), `SELECT tenant_id FROM api_keys WHERE key_hash=$1 AND scope='widget' AND allowed_origin=$2 AND is_active=TRUE`, hashText, r.Header.Get("Origin")).Scan(&tenant) != nil {
				operatorUnauthorized(w)
				return
			}
		}
		ctx := context.WithValue(r.Context(), tenantContextKey{}, tenant)
		if isWorkspaceToken {
			ctx = context.WithValue(ctx, operatorAuthContextKey{}, true)
		}
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
	var name string
	if err := h.db.QueryRow(r.Context(), `SELECT name FROM workspaces WHERE tenant_id=$1`, tenant).Scan(&name); err != nil {
		http.Error(w, `{"error":"workspace not found"}`, http.StatusUnauthorized)
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
	_ = json.NewEncoder(w).Encode(map[string]any{"tenant_id": tenant, "name": name, "is_empty": tickets == 0 && kb == 0})
}

func (h *WorkspaceHandler) Create(w http.ResponseWriter, r *http.Request) {
	h.mu.Lock()
	ip := r.RemoteAddr
	if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		ip = host
	}
	window := h.windows[ip]
	if time.Since(window.started) >= time.Minute {
		window = workspaceRateWindow{started: time.Now()}
	}
	window.count++
	h.windows[ip] = window
	h.mu.Unlock()
	if window.count > 10 {
		http.Error(w, `{"error":"rate limit exceeded"}`, http.StatusTooManyRequests)
		return
	}
	var input struct {
		Name string `json:"name"`
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil || strings.TrimSpace(input.Name) == "" || len([]rune(input.Name)) > 120 {
		http.Error(w, `{"error":"valid workspace name is required"}`, http.StatusBadRequest)
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
	_, err := h.db.Exec(r.Context(), `INSERT INTO workspaces(tenant_id,name,token_prefix,token_hash) VALUES($1,$2,$3,$4)`, tenant, strings.TrimSpace(input.Name), token[:11], hex.EncodeToString(hash[:]))
	if err != nil {
		http.Error(w, `{"error":"workspace creation failed"}`, 500)
		return
	}
	if _, err = h.db.Exec(r.Context(), `INSERT INTO app_settings(tenant_id,id) VALUES($1,TRUE) ON CONFLICT (tenant_id,id) DO NOTHING`, tenant); err != nil {
		http.Error(w, `{"error":"workspace initialization failed"}`, 500)
		return
	}
	if err = os.MkdirAll(filepath.Join("knowledge_base", tenant), 0700); err != nil {
		http.Error(w, `{"error":"workspace initialization failed"}`, 500)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(map[string]string{"tenant_id": tenant, "name": strings.TrimSpace(input.Name), "token": token})
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
