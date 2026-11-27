package analyzer

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"flowops-ai/internal/knowledge"
	"flowops-ai/internal/settings"
)

type yandexTransport func(*http.Request) (*http.Response, error)

func (f yandexTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestAliceUsesOwnCredentialsAndValidatesJSON(t *testing.T) {
	t.Setenv("FLOWOPS_AI_PROVIDERS", "yandex")
	t.Setenv("YANDEX_API_KEY", "alice-test-key")
	t.Setenv("YANDEX_FOLDER_ID", "test-folder")
	t.Setenv("YANDEX_MODEL", "aliceai-llm")
	t.Setenv("AI_API_KEY", "old-router-key")
	t.Setenv("GEMINI_API_KEY", "old-google-key")
	base, _ := knowledge.Load(t.TempDir())
	a := New(base)
	providers := a.approvedProviders()
	if len(providers) != 1 || !strings.Contains(providers[0].name, "Alice") {
		t.Fatal("Alice is not the only provider")
	}
	invalid := false
	a.client.Transport = yandexTransport(func(r *http.Request) (*http.Response, error) {
		if r.URL.String() != yandexBaseURL+"/chat/completions" || r.Header.Get("Authorization") != "Bearer alice-test-key" || r.Header.Get("OpenAI-Project") != "test-folder" || r.Header.Get("x-data-logging-enabled") != "false" {
			t.Fatal("wrong Alice endpoint or credentials")
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body["model"] != "gpt://test-folder/aliceai-llm" || body["response_format"].(map[string]any)["type"] != "json_object" {
			t.Fatal("wrong model or JSON mode")
		}
		content := `{"department":"IT","priority":"low","extracted_summary":"VPN"}`
		if invalid {
			content = "not JSON"
		}
		payload, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"message": map[string]string{"content": content}}}})
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(string(payload))), Header: make(http.Header)}, nil
	})
	result, err := a.analyzeWithYandex(context.Background(), "VPN", settings.BuilderConfig{})
	if err != nil || result.Department != "IT" {
		t.Fatalf("Alice classification failed: %v", err)
	}
	invalid = true
	if _, err := a.analyzeWithYandex(context.Background(), "VPN", settings.BuilderConfig{}); err == nil {
		t.Fatal("invalid JSON accepted")
	}
}

func TestAliceRejectsWrongFolder(t *testing.T) {
	t.Setenv("YANDEX_FOLDER_ID", "folder-a")
	t.Setenv("YANDEX_MODEL", "gpt://folder-b/aliceai-llm")
	if _, _, err := yandexConfig(); err == nil {
		t.Fatal("cross-folder model accepted")
	}
	t.Setenv("YANDEX_MODEL", "")
	if _, model, err := yandexConfig(); err != nil || model != "gpt://folder-a/aliceai-llm" {
		t.Fatal("wrong default Alice model")
	}
}
