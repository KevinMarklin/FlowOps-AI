package handlers

import (
	"flowops-ai/internal/security"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestIntakePreviewIsReadOnlyAndStrict(t *testing.T) {
	h := &TicketsHandler{} // Deliberately no database or analyzer.
	for _, tc := range []struct {
		body   string
		status int
	}{
		{`{"message":"Не работает проектор"}`, 200},
		{`{"message":"Не работает проектор","details":{"skip":true}}`, 200},
		{`{"message":"test","details":{"priority":"high"}}`, 400},
		{`{"message":"test"} {}`, 400},
		{`{"message":""}`, 400},
	} {
		r := httptest.NewRequest("POST", "/api/v1/intake/preview", strings.NewReader(tc.body))
		w := httptest.NewRecorder()
		h.IntakePreview(w, r)
		if w.Code != tc.status {
			t.Fatalf("%d: %s", w.Code, w.Body.String())
		}
	}
	for _, role := range []string{"admin", "operator", "employee"} {
		if !security.AllowRequest(role, "POST", "/api/v1/intake/preview") {
			t.Fatal("requester cannot use preview")
		}
	}
	if security.AllowRequest("analyst", "POST", "/api/v1/intake/preview") {
		t.Fatal("analyst may submit ticket text")
	}
}
