package security

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestIntegrationBoundary(t *testing.T) {
	key := strings.Repeat("a", 40)
	for _, tc := range []struct {
		configured, header string
		status             int
	}{
		{"", "", 503}, {"short", "Bearer short", 503}, {key, "", 401}, {key, "Bearer wrong", 401}, {key, "Basic " + key, 401}, {key, "Bearer " + key, 204},
	} {
		called := false
		h := ProtectIngest(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { called = true; w.WriteHeader(204) }), tc.configured)
		r := httptest.NewRequest("POST", "/api/v1/ingest", strings.NewReader(`{"channel":"email","sender":"forged@example.com"}`))
		r.Header.Set("Authorization", tc.header)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != tc.status || called != (tc.status == 204) {
			t.Fatalf("status=%d called=%v", w.Code, called)
		}
	}
}
