package security

import (
	"bytes"
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

type ingestKey struct{ scope, origin string }
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
		if r.Method == http.MethodOptions {
			w.Header().Set("Cache-Control", "no-store")
			w.Header().Set("Access-Control-Allow-Methods", "POST, OPTIONS")
			w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type")
			w.Header().Set("Vary", "Origin")
			if r.Header.Get("Origin") != "" {
				var allowed bool
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
		if !hasBearer || value == "" {
			ingestUnauthorized(w)
			return
		}
		credentialHash := sha256.Sum256([]byte(value))
		identity := ""
		widgetKey := false
		authorized := false
		if len(token) >= 32 && subtle.ConstantTimeCompare(expected[:], credentialHash[:]) == 1 {
			authorized, identity = true, "global"
		} else {
			var key ingestKey
			err := db.QueryRow(r.Context(), `SELECT scope,allowed_origin FROM api_keys WHERE key_hash=$1 AND is_active=TRUE`, hex.EncodeToString(credentialHash[:])).Scan(&key.scope, &key.origin)
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
				w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type")
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
			authorized, identity = true, hex.EncodeToString(credentialHash[:])
			if _, err := db.Exec(r.Context(), `UPDATE api_keys SET last_used_at=NOW() WHERE key_hash=$1 AND is_active=TRUE`, identity); err != nil {
				ingestError(w, http.StatusServiceUnavailable, "Integration credential store unavailable")
				return
			}
		}
		if !authorized {
			ingestUnauthorized(w)
			return
		}
		ip := clientIP(r)
		if !limiter.allow(identity + "|" + ip) {
			ingestError(w, http.StatusTooManyRequests, "Integration rate limit exceeded")
			return
		}
		if widgetKey {
			w.Header().Set("Access-Control-Allow-Origin", r.Header.Get("Origin"))
		}
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
