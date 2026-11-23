package security

import (
	"crypto/sha256"
	"crypto/subtle"
	"net/http"
	"strings"
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
