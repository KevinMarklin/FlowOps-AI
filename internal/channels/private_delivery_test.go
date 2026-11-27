package channels

import (
	"context"
	"net/http"
	"testing"
)

func TestCorporateReplyNeverLeavesForGroupChat(t *testing.T) {
	original := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = original })
	http.DefaultTransport = telegramRoundTripFunc(func(*http.Request) (*http.Response, error) { t.Fatal("group reply reached network"); return nil, nil })
	d := NewDispatcher(Config{MaxToken: "synthetic"})
	if err := d.Dispatch(context.Background(), "bot", "max_chat:123", 1, "", "", "private corporate answer"); err == nil {
		t.Fatal("group reply accepted")
	}
}
