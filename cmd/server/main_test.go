package main

import "testing"

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
