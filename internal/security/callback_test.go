package security

import (
	"context"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"testing"
)

func TestCallbackRegistrationAndExactMatch(t *testing.T) {
	for _, raw := range []string{"http://hooks.example.com/reply", "https://u:p@hooks.example.com/reply", "https://hooks.example.com:8443/reply", "https://hooks.example.com/reply#x", "https://127.0.0.1/reply", "https://[::1]/reply"} {
		if _, err := NewCallbacks(raw); err == nil {
			t.Errorf("accepted %s", raw)
		}
	}
	c, err := NewCallbacks("https://hooks.example.com/reply")
	if err != nil {
		t.Fatal(err)
	}
	for _, raw := range []string{"https://hooks.example.com/reply/", "https://evil.example/reply", "https://hooks.example.com/reply?secret=x"} {
		if c.Validate(raw) == nil {
			t.Errorf("accepted unregistered endpoint %s", raw)
		}
	}
	if c.Validate("https://hooks.example.com/reply") != nil {
		t.Fatal("registered endpoint rejected")
	}
	var disabled *Callbacks
	if disabled.Validate("https://hooks.example.com/reply") == nil {
		t.Fatal("nil policy allowed callback")
	}
}

func TestCallbackBlocksPrivateDNSAtConnection(t *testing.T) {
	for _, ip := range []string{"127.0.0.1", "10.0.0.1", "169.254.169.254", "100.100.100.200", "::1", "fd00::1", "::ffff:127.0.0.1", "64:ff9b::7f00:1"} {
		c, _ := NewCallbacks("https://hooks.example.com/reply")
		c.lookup = func(context.Context, string) ([]net.IPAddr, error) { return []net.IPAddr{{IP: net.ParseIP(ip)}}, nil }
		if conn, err := c.dialContext(context.Background(), "tcp", "hooks.example.com:443"); err == nil {
			conn.Close()
			t.Errorf("allowed %s", ip)
		}
	}
	if !publicCallbackIP(netip.MustParseAddr("8.8.8.8")) || !publicCallbackIP(netip.MustParseAddr("2606:4700:4700::1111")) {
		t.Fatal("public addresses rejected")
	}
}

type callbackTransport func(*http.Request) (*http.Response, error)

func (f callbackTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestCallbackDoesNotFollowRedirect(t *testing.T) {
	c, _ := NewCallbacks("https://hooks.example.com/reply")
	calls := 0
	c.client.Transport = callbackTransport(func(r *http.Request) (*http.Response, error) {
		calls++
		return &http.Response{StatusCode: 307, Header: http.Header{"Location": []string{"https://127.0.0.1/private"}}, Body: http.NoBody, Request: r}, nil
	})
	if c.Post(context.Background(), "https://hooks.example.com/reply", strings.NewReader("{}")) == nil || calls != 1 {
		t.Fatalf("redirect escaped policy: calls=%d", calls)
	}
}
