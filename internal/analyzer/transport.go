package analyzer

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
)

func newProviderRequest(ctx context.Context, method, endpoint string, body io.Reader) (*http.Request, error) {
	u, err := url.Parse(endpoint)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.Fragment != "" {
		return nil, errors.New("AI endpoint must use HTTPS without URL credentials")
	}
	return http.NewRequestWithContext(ctx, method, endpoint, body)
}

// Optional enterprise TLS inspection CA is scoped to Yandex requests only.
// Hostname verification and standard system roots remain enabled.
func (a *Analyzer) doProviderRequest(req *http.Request) (*http.Response, error) {
	caFile := strings.TrimSpace(os.Getenv("YANDEX_CA_FILE"))
	if caFile == "" || req.URL.Scheme != "https" || req.URL.Hostname() != "ai.api.cloud.yandex.net" {
		return a.client.Do(req)
	}
	pem, err := os.ReadFile(caFile)
	if err != nil {
		return nil, fmt.Errorf("Yandex CA file unavailable")
	}
	roots, err := x509.SystemCertPool()
	if err != nil {
		return nil, fmt.Errorf("system certificate pool unavailable")
	}
	if !roots.AppendCertsFromPEM(pem) {
		return nil, fmt.Errorf("Yandex CA file contains no PEM certificates")
	}
	original := a.client.Transport
	if original == nil {
		original = http.DefaultTransport
	}
	base, ok := original.(*http.Transport)
	if !ok {
		return nil, fmt.Errorf("Yandex CA requires standard HTTP transport")
	}
	transport := base.Clone()
	if transport.TLSClientConfig == nil {
		transport.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12}
	} else {
		transport.TLSClientConfig = transport.TLSClientConfig.Clone()
	}
	transport.TLSClientConfig.RootCAs = roots
	transport.TLSClientConfig.InsecureSkipVerify = false
	defer transport.CloseIdleConnections()
	client := *a.client
	client.Transport = transport
	return client.Do(req)
}
