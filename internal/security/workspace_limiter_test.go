package security

import (
	"fmt"
	"testing"
	"time"
)

func TestWorkspaceRegistrationLimiterExpiresAndCapsIPs(t *testing.T) {
	h := NewWorkspaceHandler(nil)
	now := time.Now()
	for i := 0; i < 10; i++ {
		if !h.allowRegistration("same-ip", now) {
			t.Fatal("premature rate limit")
		}
	}
	if h.allowRegistration("same-ip", now) {
		t.Fatal("registration limit bypassed")
	}
	if !h.allowRegistration("same-ip", now.Add(time.Minute)) {
		t.Fatal("expired registration window retained")
	}
	h.windows = make(map[string]workspaceRateWindow)
	for i := 0; i < 10000; i++ {
		h.windows[fmt.Sprint(i)] = workspaceRateWindow{started: now}
	}
	if h.allowRegistration("new-ip", now) || len(h.windows) != 10000 {
		t.Fatal("unbounded IP cache")
	}
	if !h.allowRegistration("after-expiry", now.Add(2*time.Minute)) || len(h.windows) != 1 {
		t.Fatal("expired IPs not reclaimed")
	}
}
