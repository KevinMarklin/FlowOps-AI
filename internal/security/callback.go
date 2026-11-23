package security

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"time"
)

var ErrCallbackDenied = errors.New("callback destination is not permitted")

// Callbacks are disabled unless an exact HTTPS endpoint is registered at startup.
// DNS is checked at dial time and the validated IP is pinned for the connection.
type Callbacks struct {
	allowed map[string]bool
	client  *http.Client
	lookup  func(context.Context, string) ([]net.IPAddr, error)
}

func NewCallbacks(endpoints string) (*Callbacks, error) {
	c := &Callbacks{allowed: make(map[string]bool), lookup: net.DefaultResolver.LookupIPAddr}
	for _, endpoint := range strings.Split(endpoints, ",") {
		endpoint = strings.TrimSpace(endpoint)
		if endpoint == "" {
			continue
		}
		u, err := callbackURL(endpoint)
		if err != nil {
			return nil, ErrCallbackDenied
		}
		if ip, err := netip.ParseAddr(u.Hostname()); err == nil && !publicCallbackIP(ip) {
			return nil, ErrCallbackDenied
		}
		c.allowed[endpoint] = true
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil // Environment proxies must not bypass the checked dialer.
	transport.DialContext = c.dialContext
	transport.MaxConnsPerHost = 4
	transport.ResponseHeaderTimeout = 3 * time.Second
	c.client = &http.Client{Transport: transport, Timeout: 3 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return ErrCallbackDenied }}
	return c, nil
}

func callbackURL(raw string) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.Fragment != "" || u.Opaque != "" || (u.Port() != "" && u.Port() != "443") || len(raw) > 2048 {
		return nil, ErrCallbackDenied
	}
	return u, nil
}

func (c *Callbacks) Validate(endpoint string) error {
	if endpoint == "" {
		return nil
	}
	if c == nil || !c.allowed[endpoint] {
		return ErrCallbackDenied
	}
	_, err := callbackURL(endpoint)
	return err
}

func (c *Callbacks) Post(ctx context.Context, endpoint string, body io.Reader) error {
	if endpoint == "" || c.Validate(endpoint) != nil {
		return ErrCallbackDenied
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, body)
	if err != nil {
		return ErrCallbackDenied
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.client.Do(req)
	if err != nil {
		return errors.New("callback delivery failed")
	} // Do not leak credential-bearing URLs.
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return errors.New("callback destination rejected delivery")
	}
	return nil
}

func (c *Callbacks) dialContext(ctx context.Context, network, address string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil || port != "443" {
		return nil, ErrCallbackDenied
	}
	ips, err := c.lookup(ctx, host)
	if err != nil || len(ips) == 0 {
		return nil, ErrCallbackDenied
	}
	// Reject mixed DNS responses too; never fall back to an unchecked address.
	for _, ip := range ips {
		addr, ok := netip.AddrFromSlice(ip.IP)
		if !ok || ip.Zone != "" || !publicCallbackIP(addr) {
			return nil, ErrCallbackDenied
		}
	}
	var dialer net.Dialer
	dialer.Timeout = 3 * time.Second
	for _, ip := range ips {
		conn, err := dialer.DialContext(ctx, network, net.JoinHostPort(ip.IP.String(), port))
		if err == nil {
			return conn, nil
		}
	}
	return nil, errors.New("callback connection failed")
}

func publicCallbackIP(ip netip.Addr) bool {
	ip = ip.Unmap()
	if !ip.IsGlobalUnicast() || ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() {
		return false
	}
	for _, cidr := range []string{
		"0.0.0.0/8", "100.64.0.0/10", "192.0.0.0/24", "192.0.2.0/24", "198.18.0.0/15", "198.51.100.0/24", "203.0.113.0/24", "240.0.0.0/4",
		"::/96", "64:ff9b::/96", "64:ff9b:1::/48", "100::/64", "2001::/23", "2001:db8::/32", "2002::/16",
	} {
		if netip.MustParsePrefix(cidr).Contains(ip) {
			return false
		}
	}
	return true
}
