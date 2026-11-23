package channels

import (
	"bufio"
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestMAXRejectsHTTPAndRequiresExplicitInsecureTLSOptIn(t *testing.T) {
	maxClients.Delete(false)
	maxClients.Delete(true)
	calls := 0
	s := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls++; w.WriteHeader(200) }))
	defer s.Close()
	for _, cfg := range []Config{{MaxAPIURL: "http://example.test", MaxToken: "synthetic"}, {MaxAPIURL: s.URL, MaxToken: "synthetic"}} {
		if resp, err := maxRequest(context.Background(), cfg, "POST", "/messages", nil, nil); err == nil {
			resp.Body.Close()
			t.Fatal("insecure transport accepted")
		}
	}
	if calls != 0 {
		t.Fatal("token was sent without the explicit TLS opt-in")
	}
	resp, err := maxRequest(context.Background(), Config{MaxAPIURL: s.URL, MaxToken: "synthetic", MaxInsecureTLS: true}, "POST", "/messages", nil, nil)
	if err != nil {
		t.Fatalf("explicit MAX_INSECURE_TLS opt-in failed: %v", err)
	}
	resp.Body.Close()
	if calls != 1 {
		t.Fatal("explicit MAX_INSECURE_TLS opt-in did not reach endpoint")
	}
}

func TestMailParserLimits(t *testing.T) {
	if _, err := readIMAPLine(bufio.NewReader(strings.NewReader(strings.Repeat("x", (64<<10)+1)))); err == nil {
		t.Fatal("unbounded protocol line accepted")
	}
	if _, err := decodeMIMEBodyDepth("text/plain", "", strings.NewReader("text"), 17); err == nil {
		t.Fatal("excessive MIME depth accepted")
	}
	line, err := readIMAPLine(bufio.NewReader(strings.NewReader("* OK ready\r\n")))
	if err != nil || line != "* OK ready\r\n" {
		t.Fatalf("valid greeting rejected: %q %v", line, err)
	}
}

func TestMAXDoesNotRedirectCredentials(t *testing.T) {
	calls := 0
	target := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls++; w.WriteHeader(200) }))
	defer target.Close()
	source := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, target.URL, 307) }))
	defer source.Close()
	client := source.Client()
	client.CheckRedirect = rejectChannelRedirect
	maxClients.Store(false, client)
	t.Cleanup(func() { maxClients.Delete(false) })
	if resp, err := maxRequest(context.Background(), Config{MaxAPIURL: source.URL, MaxToken: "synthetic"}, "POST", "/messages", nil, nil); err == nil {
		resp.Body.Close()
		t.Fatal("redirect allowed")
	}
	if calls != 0 {
		t.Fatal("redirect received credentials")
	}
}

func TestMailDialCancellationInterruptsStalledHandshake(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	done := make(chan struct{})
	defer close(done)
	go func() {
		conn, err := listener.Accept()
		if err == nil {
			defer conn.Close()
			<-done
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	started := time.Now()
	conn, cleanup, err := dialMail(ctx, listener.Addr().String())
	if err == nil {
		cleanup()
		conn.Close()
		t.Fatal("stalled TLS handshake succeeded")
	}
	if time.Since(started) > time.Second {
		t.Fatal("context did not interrupt mail connection")
	}
}

func TestDisabledDispatcherNeverClaimsSuccess(t *testing.T) {
	d := NewDispatcher(Config{})
	for _, tc := range []struct{ channel, sender string }{{"bot", "tg:1"}, {"bot", "max:1"}, {"email", "user@example.test"}, {"bot", "unknown"}} {
		if d.Dispatch(context.Background(), tc.channel, tc.sender, 1, "", "request", "reply") == nil {
			t.Fatalf("disabled destination reported delivered: %+v", tc)
		}
	}
}
