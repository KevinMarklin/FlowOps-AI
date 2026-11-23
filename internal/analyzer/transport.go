package analyzer

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
)

func newProviderRequest(ctx context.Context, method, endpoint string, body io.Reader) (*http.Request, error) {
	u, err := url.Parse(endpoint)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.Fragment != "" {
		return nil, errors.New("AI endpoint must use HTTPS without URL credentials")
	}
	return http.NewRequestWithContext(ctx, method, endpoint, body)
}
