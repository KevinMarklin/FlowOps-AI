package security

import "testing"

func TestRolePermissionMatrix(t *testing.T) {
	cases := []struct {
		method, path string
		allowed      []string
	}{
		{"GET", "/api/v1/auth/me", []string{"employee", "operator", "analyst", "admin"}},
		{"GET", "/api/tickets", []string{"employee", "operator", "admin"}},
		{"POST", "/api/tickets", []string{"employee", "operator", "admin"}},
		{"GET", "/api/tickets/42", []string{"employee", "operator", "admin"}},
		{"POST", "/api/tickets/42/feedback", []string{"employee", "operator", "admin"}},
		{"PATCH", "/api/tickets/42", []string{"operator", "admin"}},
		{"POST", "/api/tickets/42/reply", []string{"operator", "admin"}},
		{"DELETE", "/api/tickets/reset", []string{"admin"}},
		{"GET", "/api/stats", []string{"analyst", "admin"}},
		{"GET", "/api/audit", []string{"admin"}},
		{"GET", "/api/knowledge", []string{"operator", "admin"}},
		{"POST", "/api/knowledge", []string{"operator", "admin"}},
		{"DELETE", "/api/knowledge/secret.md", []string{"admin"}},
		{"GET", "/api/v1/members", []string{"admin"}},
		{"POST", "/api/v1/members", []string{"admin"}},
		{"PATCH", "/api/v1/members/person", []string{"admin"}},
		{"GET", "/api/v1/settings/channels", []string{"admin"}},
		{"POST", "/api/v1/kb/smart-import", []string{"admin"}},
		{"POST", "/api/v1/keys", []string{"admin"}},
		{"GET", "/api/new-future-endpoint", []string{"admin"}},
		{"GET", "/api/tickets/reset", []string{"admin"}},
		{"GET", "/api/tickets/42/extra", []string{"admin"}},
	}
	for _, c := range cases {
		for _, role := range []string{"employee", "operator", "analyst", "admin", "unknown", ""} {
			want := false
			for _, allowed := range c.allowed {
				if role == allowed {
					want = true
				}
			}
			if got := AllowRequest(role, c.method, c.path); got != want {
				t.Errorf("%s %s %s: got %v want %v", role, c.method, c.path, got, want)
			}
		}
	}
}
