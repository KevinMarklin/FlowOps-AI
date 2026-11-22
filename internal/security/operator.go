package security

import (
	"crypto/sha256"
	"crypto/subtle"
	"net/http"
	"strings"
)

// ProtectOperatorWrites closes the unauthenticated KB-poisoning path. An unset
// or short token disables privileged mutations instead of silently bypassing auth.
// This is a scoped service-token boundary, not a replacement for employee RBAC.
func ProtectOperatorWrites(next http.Handler, token string) http.Handler {
	expected := sha256.Sum256([]byte(token))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		privileged := strings.HasPrefix(path, "/api/knowledge") ||
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
