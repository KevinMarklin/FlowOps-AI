package security

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestPasswordPolicyAndSecureCookies(t *testing.T) {
	for _, c := range []struct {
		password string
		want     bool
	}{{"short", false}, {strings.Repeat(" ", 12), false}, {"A phrase for testing!", true}, {strings.Repeat("Я", 36), true}, {strings.Repeat("Я", 37), false}, {strings.Repeat("a", 73), false}} {
		if validPassword(c.password) != c.want {
			t.Fatalf("password policy mismatch for %d bytes", len(c.password))
		}
	}
	t.Setenv("FLOWOPS_COOKIE_SECURE", "true")
	r := httptest.NewRequest("GET", "http://app.example/", nil)
	w := httptest.NewRecorder()
	setSessionCookies(w, r, sessionSecrets{"synthetic-session", "synthetic-csrf"})
	cookies := w.Result().Cookies()
	if len(cookies) != 2 {
		t.Fatal("session cookies missing")
	}
	for _, cookie := range cookies {
		if !cookie.Secure || cookie.Path != "/" || cookie.SameSite != http.SameSiteStrictMode {
			t.Fatal("unsafe production cookie attributes")
		}
		if cookie.HttpOnly != (cookie.Name == sessionCookie) {
			t.Fatal("wrong HttpOnly flag")
		}
	}
}

func TestPasswordJSONAndSessionCSRF(t *testing.T) {
	for _, c := range []struct {
		body, contentType string
		status            int
	}{{`{"email":"test@example.org"}`, "application/json", 200}, {`{"email":"test@example.org"} {}`, "application/json", 400}, {`{}`, "application/json-wrong", 403}, {`{}`, "text/plain", 403}} {
		r := httptest.NewRequest("POST", "http://app.example/", strings.NewReader(c.body))
		r.Header.Set("Content-Type", c.contentType)
		w := httptest.NewRecorder()
		var input struct {
			Email string `json:"email"`
		}
		ok := publicAuthJSON(w, r, &input)
		if ok != (c.status == 200) || w.Code != c.status {
			t.Fatalf("JSON validation: %s status %d", c.contentType, w.Code)
		}
	}
	r := httptest.NewRequest("POST", "http://app.example/", nil)
	r.Header.Set("X-CSRF-Token", "synthetic-csrf")
	if !validSessionWrite(r, digest("synthetic-csrf")) {
		t.Fatal("matching CSRF rejected")
	}
	r.Header.Set("Origin", "https://attacker.example")
	if validSessionWrite(r, digest("synthetic-csrf")) {
		t.Fatal("cross-origin write accepted")
	}
	r.Header.Del("Origin")
	r.Header.Set("X-CSRF-Token", "wrong")
	if validSessionWrite(r, digest("synthetic-csrf")) {
		t.Fatal("invalid CSRF accepted")
	}
}
