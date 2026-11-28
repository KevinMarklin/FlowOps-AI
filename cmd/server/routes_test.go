package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

func TestAllNavigationURLsServeShell(t *testing.T) {
	t.Chdir("../..")
	mux := http.NewServeMux()
	registerSectionRoutes(mux)
	for _, path := range sectionPaths {
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest("GET", path, nil))
		if w.Code != 200 || !strings.Contains(w.Body.String(), `id="auth-gate"`) || w.Header().Get("Cache-Control") != "no-store" {
			t.Fatal(path, w.Code)
		}
	}
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest("GET", "/settings/not-a-page", nil))
	if w.Code != 404 {
		t.Fatal("unknown route should 404")
	}
	// The client route map and server allowlist must remain in lockstep.
	manifest, err := os.ReadFile("web/routes.js")
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range sectionPaths {
		if strings.HasPrefix(path, "/settings/") {
			continue
		}
		encoded, _ := json.Marshal(path)
		if !strings.Contains(string(manifest), "'"+path+"'") {
			t.Fatal("missing client route", string(encoded))
		}
	}
}
