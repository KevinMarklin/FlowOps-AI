package analyzer

import (
	"context"
	"crypto/x509"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"flowops-ai/internal/knowledge"
)

func allowTestProviders(t *testing.T) {
	t.Helper()
	t.Setenv("FLOWOPS_AI_PROVIDERS", "gigachat,gemini,compatible")
	t.Setenv("FLOWOPS_AI_ALLOW_FALLBACK", "true")
	t.Setenv("FLOWOPS_AI_ALLOW_SENSITIVE", "true")
}

func TestProviderPolicyPreventsNetworkAndSenderExport(t *testing.T) {
	calls := 0
	server := providerTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		body, _ := io.ReadAll(r.Body)
		if strings.Contains(string(body), "tg:987654321") || strings.Contains(string(body), "Отправитель") {
			t.Error("sender metadata exported")
		}
		_, _ = w.Write([]byte(`{"candidates":[{"content":{"parts":[{"text":"{\"department\":\"IT\",\"priority\":\"medium\",\"extracted_summary\":\"VPN\"}"}]}}]}`))
	}))
	defer server.Close()
	t.Setenv("GIGACHAT_AUTH_KEY", "")
	t.Setenv("GEMINI_API_KEY", "synthetic")
	t.Setenv("AI_API_KEY", "")
	t.Setenv("AI_GOOGLE_BASE_URL", server.URL)
	t.Setenv("FLOWOPS_AI_PROVIDERS", "")
	t.Setenv("FLOWOPS_AI_ALLOW_SENSITIVE", "false")
	base, _ := knowledge.Load(t.TempDir())
	a := newLegacyAnalyzer(base)
	a.Analyze(context.Background(), "Не работает VPN")
	if calls != 0 {
		t.Fatal("API key enabled network without approval")
	}
	t.Setenv("FLOWOPS_AI_PROVIDERS", "gemini,typo")
	a.Analyze(context.Background(), "Не работает VPN")
	if calls != 0 {
		t.Fatal("invalid policy partially enabled providers")
	}
	t.Setenv("FLOWOPS_AI_PROVIDERS", "gemini")
	a.Analyze(context.Background(), "Сотрудник Иван Петров, зарплата 250000 рублей")
	if calls != 0 {
		t.Fatal("sensitive data sent without approval")
	}
	a.AnalyzeWithSender(context.Background(), "Не работает VPN", "tg:987654321")
	if calls != 1 {
		t.Fatalf("approved provider not used: calls=%d", calls)
	}
}

func TestFallbackRequiresSeparateApproval(t *testing.T) {
	allowTestProviders(t)
	t.Setenv("FLOWOPS_AI_ALLOW_FALLBACK", "false")
	calls := 0
	primary := providerTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(503) }))
	defer primary.Close()
	secondary := providerTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls++; w.WriteHeader(503) }))
	defer secondary.Close()
	t.Setenv("GIGACHAT_AUTH_KEY", "synthetic")
	t.Setenv("GIGACHAT_OAUTH_URL", primary.URL)
	t.Setenv("GIGACHAT_INSECURE_TLS", "false")
	t.Setenv("GEMINI_API_KEY", "synthetic")
	t.Setenv("AI_GOOGLE_BASE_URL", secondary.URL)
	base, _ := knowledge.Load(t.TempDir())
	newLegacyAnalyzer(base).Analyze(context.Background(), "VPN")
	if calls != 0 {
		t.Fatal("unapproved fallback exported data")
	}
}

func TestLocalAnalysisUnaffectedByModelBudget(t *testing.T) {
	t.Setenv("FLOWOPS_AI_PROVIDERS", "")
	base, _ := knowledge.Load(t.TempDir())
	a := newLegacyAnalyzer(base)
	for i := 0; i < 100; i++ {
		if result := a.Analyze(context.Background(), "Не работает VPN"); result.Department != "IT" {
			t.Fatalf("local analysis lost at %d", i)
		}
	}
	if a.budgetCount != 0 {
		t.Fatal("local analysis consumed external budget")
	}
}

// Local fixtures use real TLS verification with only their generated CA trusted.
func providerTestServer(t *testing.T, handler http.Handler) *httptest.Server {
	t.Helper()
	server := httptest.NewTLSServer(handler)
	old := http.DefaultTransport
	transport := old.(*http.Transport).Clone()
	if transport.TLSClientConfig == nil {
		transport.TLSClientConfig = server.Client().Transport.(*http.Transport).TLSClientConfig.Clone()
	}
	roots := x509.NewCertPool()
	if transport.TLSClientConfig.RootCAs != nil {
		roots = transport.TLSClientConfig.RootCAs.Clone()
	}
	roots.AddCert(server.Certificate())
	transport.TLSClientConfig.RootCAs = roots
	transport.TLSClientConfig.InsecureSkipVerify = false
	http.DefaultTransport = transport
	t.Cleanup(func() { transport.CloseIdleConnections(); http.DefaultTransport = old })
	return server
}
