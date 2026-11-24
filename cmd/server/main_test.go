package main

import (
	"net/http/httptest"
	"strings"
	"testing"
)

func TestIsNoisyPollingPath(t *testing.T) {
	for _, path := range []string{"/api/tickets", "/api/stats", "/api/audit"} {
		if !isNoisyPollingPath(path) {
			t.Errorf("expected polling path %q to be suppressed", path)
		}
	}
	for _, path := range []string{"/api/tickets/1", "/api/knowledge", "/api/stats/"} {
		if isNoisyPollingPath(path) {
			t.Errorf("unexpectedly suppressing %q", path)
		}
	}
}

func TestWidgetJavaScriptServed(t *testing.T) {
	handler := noStoreFileServer("../../web")
	request := httptest.NewRequest("GET", "/widget.js", nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != 200 {
		t.Fatalf("widget.js status: %d", response.Code)
	}
	if !strings.Contains(response.Body.String(), "attachShadow") || !strings.Contains(response.Body.String(), "api/v1/ingest") {
		t.Fatal("served widget asset is missing required implementation")
	}
}
