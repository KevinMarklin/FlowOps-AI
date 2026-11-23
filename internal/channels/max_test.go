package channels

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestSendMaxMessageUsesAuthorizationHeader(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/messages" {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.String())
		}
		if got := r.Header.Get("Authorization"); got != "test-max-token" {
			t.Errorf("Authorization = %q", got)
		}
		if r.URL.Query().Get("user_id") != "1234" {
			t.Errorf("user_id = %q", r.URL.Query().Get("user_id"))
		}
		if _, ok := r.URL.Query()["access_token"]; ok {
			t.Error("token must not be sent in query parameters")
		}
		var body map[string]string
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode body: %v", err)
		}
		if body["text"] != "Ответ" {
			t.Errorf("message text = %q", body["text"])
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	maxClients.Store(false, server.Client())
	t.Cleanup(func() { maxClients.Delete(false) })
	cfg := Config{MaxToken: "test-max-token", MaxAPIURL: server.URL}
	if err := sendMaxMessage(context.Background(), cfg, "user_id", "1234", "Ответ"); err != nil {
		t.Fatal(err)
	}
}
