package security

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Principal struct {
	ID       string `json:"id"`
	TenantID string `json:"tenant_id"`
	Name     string `json:"display_name"`
	Email    string `json:"email"`
	Role     string `json:"role"`
	Owner    bool   `json:"owner"`
}
type principalKey struct{}

func CurrentPrincipal(ctx context.Context) (Principal, bool) {
	p, ok := ctx.Value(principalKey{}).(Principal)
	return p, ok
}
func WithPrincipal(ctx context.Context, p Principal) context.Context {
	ctx = WithTenantID(ctx, p.TenantID)
	ctx = context.WithValue(ctx, principalKey{}, p)
	return context.WithValue(ctx, operatorAuthContextKey{}, p.Role == "admin" || p.Role == "operator")
}
func IsEmployee(ctx context.Context) bool {
	p, ok := CurrentPrincipal(ctx)
	return ok && p.Role == "employee"
}
func RequesterID(ctx context.Context) string { p, _ := CurrentPrincipal(ctx); return p.ID }
func ValidRole(role string) bool {
	return role == "admin" || role == "operator" || role == "analyst" || role == "employee"
}

// Every request resolves the stored role again: revocation and role changes
// take effect without waiting for a client token or cache to expire.
func LookupPrincipal(ctx context.Context, db *pgxpool.Pool, token string) (Principal, error) {
	sum := sha256.Sum256([]byte(token))
	hash := hex.EncodeToString(sum[:])
	p := Principal{}
	err := db.QueryRow(ctx, `SELECT tenant_id,name FROM workspaces WHERE token_hash=$1`, hash).Scan(&p.TenantID, &p.Name)
	if err == nil {
		p.ID = "owner"
		p.Role = "admin"
		p.Owner = true
		return p, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return p, err
	}
	err = db.QueryRow(ctx, `SELECT id,tenant_id,display_name,email,role FROM workspace_members WHERE token_hash=$1 AND is_active=TRUE`, hash).Scan(&p.ID, &p.TenantID, &p.Name, &p.Email, &p.Role)
	return p, err
}

// AllowRequest is deny-by-default. UI navigation never grants access.
func AllowRequest(role, method, path string) bool {
	if path == "/api/tickets/reset" {
		return false
	}
	if !ValidRole(role) {
		return false
	}
	if path == "/api/v1/auth/me" && method == http.MethodGet {
		return true
	}
	if path == "/api/v1/organization" {
		return method == http.MethodGet
	}
	if path == "/api/v1/settings/organization" {
		return method == http.MethodGet && (role == "analyst" || role == "admin") || method == http.MethodPut && role == "admin"
	}
	if path == "/api/v1/profile/bots" {
		return method == http.MethodGet
	}
	if path == "/api/v1/profile/bots/telegram" || path == "/api/v1/profile/bots/max" {
		return method == http.MethodPost || method == http.MethodDelete
	}
	if role == "admin" {
		return true
	}
	if path == "/api/analytics/dashboards" || strings.HasPrefix(path, "/api/analytics/dashboards/") {
		return role == "analyst" && (method == "GET" || method == "POST" || method == "PUT" || method == "DELETE")
	}
	if path == "/api/v1/kb/articles" {
		return role == "operator" && (method == "GET" || method == "POST")
	}
	if path == "/api/v1/kb/from-tickets" {
		return role == "operator" && method == "POST"
	}
	if strings.HasPrefix(path, "/api/v1/kb/articles/") {
		return role == "operator" && (method == "PUT" || method == "GET" && strings.HasSuffix(path, "/versions"))
	}

	if path == "/api/optimization" {
		return role == "analyst" && method == "GET"
	}
	if path == "/api/analytics/explore" {
		return role == "analyst" && method == http.MethodGet
	}
	if path == "/api/stats" {
		return role == "analyst" && method == http.MethodGet
	}
	if role == "analyst" {
		return false
	}
	if path == "/api/v1/intake/preview" {
		return method == http.MethodPost
	}
	if path == "/api/tickets" {
		return method == http.MethodGet || method == http.MethodPost
	}
	if path == "/api/tickets/queue/clear" {
		return role == "operator" && method == http.MethodPost
	}
	if strings.HasPrefix(path, "/api/tickets/") {
		parts := strings.Split(strings.TrimPrefix(path, "/api/tickets/"), "/")
		id, err := strconv.Atoi(parts[0])
		if err != nil || id <= 0 {
			return false
		}
		if len(parts) == 1 {
			return method == http.MethodGet || (role == "operator" && method == http.MethodPatch)
		}
		if len(parts) == 2 && method == http.MethodPost {
			if parts[1] == "read" {
				return role == "operator"
			}
			if parts[1] == "messages" || (parts[1] == "decision" && role == "employee") {
				return true
			}
			return parts[1] == "feedback" || (role == "operator" && (parts[1] == "reply" || parts[1] == "work"))
		}
		if len(parts) == 2 && (parts[1] == "messages" || parts[1] == "events") && method == http.MethodGet {
			return true
		}
		if len(parts) == 4 && parts[1] == "messages" && parts[3] == "rating" {
			return role == "employee" && method == http.MethodPost
		}
	}
	if role == "operator" && (path == "/api/knowledge" || strings.HasPrefix(path, "/api/knowledge/")) {
		return method == http.MethodGet || method == http.MethodPost || method == http.MethodPatch
	}
	return false
}

func authorizePrincipal(w http.ResponseWriter, r *http.Request, db *pgxpool.Pool, p Principal) bool {
	if !AllowRequest(p.Role, r.Method, r.URL.Path) {
		ingestError(w, http.StatusForbidden, "Недостаточно прав для этого действия")
		return false
	}
	if p.Role == "employee" && strings.HasPrefix(r.URL.Path, "/api/tickets/") {
		id := strings.Split(strings.TrimPrefix(r.URL.Path, "/api/tickets/"), "/")[0]
		var exists bool
		if err := db.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM tickets WHERE id=$1 AND tenant_id=$2 AND requester_id=$3)`, id, p.TenantID, p.ID).Scan(&exists); err != nil {
			ingestError(w, 503, "Не удалось проверить доступ к обращению")
			return false
		}
		if !exists {
			ingestError(w, 404, "Обращение не найдено")
			return false
		}
	}
	return true
}

func currentUser(ctx context.Context) Principal { p, _ := CurrentPrincipal(ctx); return p }
