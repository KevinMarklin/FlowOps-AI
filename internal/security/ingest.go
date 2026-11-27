package security

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Ingest credentials belong to a trusted adapter, not to end users or operators.
// An authenticated adapter is responsible for verifying its sender identities.
func ProtectIngest(next http.Handler, token string) http.Handler {
	expected := sha256.Sum256([]byte(token))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		if len(token) < 32 {
			w.WriteHeader(503)
			_, _ = w.Write([]byte(`{"error":"Integration access is disabled"}`))
			return
		}
		value, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		actual := sha256.Sum256([]byte(value))
		if !ok || subtle.ConstantTimeCompare(expected[:], actual[:]) != 1 {
			w.Header().Set("WWW-Authenticate", "Bearer")
			w.WriteHeader(401)
			_, _ = w.Write([]byte(`{"error":"Integration authorization required"}`))
			return
		}
		next.ServeHTTP(w, r)
	})
}

type ingestKey struct{ scope, origin, tenant string }
type rateWindow struct {
	started time.Time
	count   int
}
type ingestLimiter struct {
	mu      sync.Mutex
	windows map[string]rateWindow
}

// ProtectIngestWithKeys accepts the configured service token and stored scoped keys.
// A tiny fixed-window limiter is intentionally local to each server process.
func ProtectIngestWithKeys(next http.Handler, token string, db *pgxpool.Pool) http.Handler {
	limiter := &ingestLimiter{windows: make(map[string]rateWindow)}
	expected := sha256.Sum256([]byte(token))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		if p, ok := CurrentPrincipal(r.Context()); ok && strings.HasSuffix(r.URL.Path, "/feedback") && AllowRequest(p.Role, r.Method, r.URL.Path) {
			next.ServeHTTP(w, r)
			return
		}
		if r.Method == http.MethodOptions {
			w.Header().Set("Cache-Control", "no-store")
			w.Header().Set("Access-Control-Allow-Methods", "POST, OPTIONS")
			w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type, Idempotency-Key")
			w.Header().Set("Vary", "Origin")
			if r.Header.Get("Origin") != "" {
				var allowed bool
				// Browser preflights contain requested header names, never the
				// bearer itself. Actual POST still validates key AND exact origin.
				err := db.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM api_keys WHERE is_active=TRUE AND scope='widget' AND allowed_origin=$1)`, r.Header.Get("Origin")).Scan(&allowed)
				if err != nil || !allowed {
					w.WriteHeader(http.StatusForbidden)
					return
				}
				w.Header().Set("Access-Control-Allow-Origin", r.Header.Get("Origin"))
			}
			w.WriteHeader(http.StatusNoContent)
			return
		}
		value, hasBearer := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		cookieAuth := false
		var cookieUser Principal
		if !hasBearer || value == "" {
			if r.Header.Get("Authorization") != "" {
				ingestUnauthorized(w)
				return
			}
			var csrfHash string
			var err error
			cookieUser, csrfHash, err = cookiePrincipal(r, db)
			if err != nil {
				ingestUnauthorized(w)
				return
			}
			if cookieUser.Role != "admin" || !validSessionWrite(r, csrfHash) {
				ingestError(w, 403, "Недостаточно прав или не пройдена проверка сеанса")
				return
			}
			cookie, _ := r.Cookie(sessionCookie)
			value = "session:" + cookie.Value
			cookieAuth = true
			r = r.WithContext(WithPrincipal(r.Context(), cookieUser))
		}
		credentialHash := sha256.Sum256([]byte(value))
		identity := ""
		tenantID := "default"
		widgetKey := false
		authorized := false
		if cookieAuth {
			authorized, identity, tenantID = true, hex.EncodeToString(credentialHash[:]), cookieUser.TenantID
		} else if len(token) >= 32 && subtle.ConstantTimeCompare(expected[:], credentialHash[:]) == 1 {
			authorized, identity = true, "global"
		} else {
			if p, err := LookupPrincipal(r.Context(), db, value); err == nil {
				if p.Role != "admin" {
					ingestError(w, 403, "Только администратор может использовать симулятор интеграций")
					return
				}
				authorized, identity, tenantID = true, "admin:"+hex.EncodeToString(credentialHash[:]), p.TenantID
			} else if err != pgx.ErrNoRows {
				ingestError(w, 503, "Integration credential store unavailable")
				return
			}
			if authorized {
				// The authenticated dashboard uses the same ingest pipeline as
				// external integrations while retaining its workspace boundary.
			} else {
				var key ingestKey
				err := db.QueryRow(r.Context(), `SELECT scope,allowed_origin,tenant_id FROM api_keys WHERE key_hash=$1 AND is_active=TRUE`, hex.EncodeToString(credentialHash[:])).Scan(&key.scope, &key.origin, &key.tenant)
				if err != nil {
					if err != pgx.ErrNoRows {
						ingestError(w, http.StatusServiceUnavailable, "Integration credential store unavailable")
						return
					}
					ingestUnauthorized(w)
					return
				}
				origin := r.Header.Get("Origin")
				if key.scope == "widget" && (origin == "" || origin != key.origin) {
					ingestUnauthorized(w)
					return
				}
				if key.scope != "widget" && key.scope != "ingest" {
					ingestUnauthorized(w)
					return
				}
				if key.scope == "widget" {
					widgetKey = true
					w.Header().Set("Access-Control-Allow-Origin", origin)
					w.Header().Set("Access-Control-Allow-Methods", "POST, OPTIONS")
					w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type, Idempotency-Key")
					w.Header().Set("Vary", "Origin")
					body, readErr := io.ReadAll(http.MaxBytesReader(w, r.Body, 2<<20))
					if readErr != nil {
						ingestError(w, http.StatusBadRequest, "invalid ingest body")
						return
					}
					var payload struct {
						Channel string `json:"channel"`
					}
					if json.Unmarshal(body, &payload) != nil || payload.Channel != "widget" {
						ingestUnauthorized(w)
						return
					}
					r.Body = io.NopCloser(bytes.NewReader(body))
					widgetKey = true
				}
				tenantID = key.tenant
				authorized, identity = true, hex.EncodeToString(credentialHash[:])
				if _, err := db.Exec(r.Context(), `UPDATE api_keys SET last_used_at=NOW() WHERE key_hash=$1 AND tenant_id=$2 AND is_active=TRUE`, identity, tenantID); err != nil {
					ingestError(w, http.StatusServiceUnavailable, "Integration credential store unavailable")
					return
				}
			}
		}
		if !authorized {
			ingestUnauthorized(w)
			return
		}
		ip := clientIP(r)
		if !limiter.allow("key:"+identity) || !limiter.allow("ip:"+ip) {
			ingestError(w, http.StatusTooManyRequests, "Integration rate limit exceeded")
			return
		}
		if widgetKey {
			w.Header().Set("Access-Control-Allow-Origin", r.Header.Get("Origin"))
		}
		ctx := context.WithValue(r.Context(), tenantContextKey{}, tenantID)
		r = r.WithContext(ctx)
		next.ServeHTTP(w, r)
	})
}

func (l *ingestLimiter) allow(key string) bool {
	now := time.Now()
	l.mu.Lock()
	defer l.mu.Unlock()
	for candidate, window := range l.windows {
		if now.Sub(window.started) > 2*time.Minute {
			delete(l.windows, candidate)
		}
	}
	window := l.windows[key]
	if now.Sub(window.started) >= time.Minute {
		window = rateWindow{started: now}
	}
	window.count++
	l.windows[key] = window
	return window.count <= 60
}

func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err == nil {
		return host
	}
	return r.RemoteAddr
}
func ingestUnauthorized(w http.ResponseWriter) {
	w.Header().Set("WWW-Authenticate", "Bearer")
	ingestError(w, http.StatusUnauthorized, "Integration authorization required")
}
func ingestError(w http.ResponseWriter, status int, message string) {
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": message})
}
