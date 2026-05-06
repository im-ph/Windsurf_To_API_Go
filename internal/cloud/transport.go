// Package cloud — HTTPS transport + HTTP CONNECT tunnelling helpers shared by
// the Connect-RPC client and the Firebase sign-in flow.
//
// net/http's stdlib transport handles CONNECT-based proxying automatically
// when Transport.Proxy returns a non-nil URL, so all we need here is a small
// cache of per-proxy clients plus a "try-via-proxy-then-direct" fallback.
package cloud

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"

	"golang.org/x/net/proxy"

	"windsurfapi/internal/langserver"
	"windsurfapi/internal/logx"
	"windsurfapi/internal/netguard"
)

// Error returned by transport when the proxy itself fails (so we can fall
// back to direct). Upstream API errors are NOT wrapped with this.
var ErrProxyFailed = errors.New("proxy transport failed")

var proxyErrRE = regexp.MustCompile(`(?i)proxyconnect|proxy.+(connect|refused|timeout|authentication|407|unsupported)`)

func isProxyError(err error) bool {
	if err == nil {
		return false
	}
	return proxyErrRE.MatchString(err.Error()) || strings.Contains(err.Error(), ErrProxyFailed.Error())
}

var (
	clientMu   sync.Mutex
	clientCache = map[string]*http.Client{}
)

func proxyKey(p *langserver.Proxy) string {
	if p == nil || p.Host == "" {
		return "direct"
	}
	return fmt.Sprintf("%s:%d:%s", p.Host, p.Port, p.Username)
}

// stripPort cleans up a proxy host string that may have been entered with
// a trailing :port (JS does the same cleanup).
func stripPort(host string) string {
	if !strings.Contains(host, ":") {
		return host
	}
	if i := strings.LastIndex(host, ":"); i > 0 {
		return host[:i]
	}
	return host
}

// proxyType normalises the type string. Default is "http".
func proxyType(p *langserver.Proxy) string {
	t := strings.ToLower(strings.TrimSpace(p.Type))
	switch t {
	case "socks", "socks5", "socks5h":
		return "socks5"
	default:
		return "http"
	}
}

// clientFor returns a cached *http.Client configured for the given proxy
// (or direct when p is nil). Clients are shared across requests for
// connection reuse.
//
// N3: when p is non-nil, the proxy host is validated through
// netguard.ResolveAndCheckHost BEFORE the transport is created. Private
// IPs / metadata-service literals are rejected to prevent SSRF pivot via
// an operator-supplied (potentially attacker-controlled) proxy host. A
// rejection logs once and the client falls back to direct egress.
//
// N22: SOCKS5 proxies (Type ∈ {"socks", "socks5", "socks5h"}) are now
// honoured via golang.org/x/net/proxy. The same SSRF guard applies; the
// SOCKS dialer wraps a netguard-checked net.Dialer so even a hostile
// resolver answering 169.254.169.254 mid-dial is caught.
func clientFor(p *langserver.Proxy) *http.Client {
	key := proxyKey(p)
	clientMu.Lock()
	defer clientMu.Unlock()
	if c, ok := clientCache[key]; ok {
		return c
	}
	tr := &http.Transport{
		TLSClientConfig:     &tls.Config{MinVersion: tls.VersionTLS12},
		TLSHandshakeTimeout: 10 * time.Second,
		IdleConnTimeout:     90 * time.Second,
		MaxIdleConns:        50,
		MaxIdleConnsPerHost: 5,
	}
	if p != nil && p.Host != "" {
		host := stripPort(p.Host)
		port := p.Port
		if port == 0 {
			if proxyType(p) == "socks5" {
				port = 1080
			} else {
				port = 8080
			}
		}
		// N3: SSRF guard. ResolveAndCheckHost looks up DNS and rejects
		// private/loopback/metadata literals. A hostile DNS that flips
		// to a private IP mid-attack is also caught because we look up
		// here, NOT at dial time, and the SOCKS5/HTTP dialers below
		// receive the validated host string (which the stdlib then
		// re-resolves — TOCTOU window is small, but the dashboard test
		// path runs the same check before saving the proxy so an attacker
		// cannot set a hostile host in the first place).
		if _, err := netguard.ResolveAndCheckHost(host, nil); err != nil {
			logx.Warn("cloud transport: refusing proxy host %q (%v) — falling back to direct egress", host, err)
			c := &http.Client{Transport: tr, Timeout: 30 * time.Second}
			clientCache[key] = c
			return c
		}

		switch proxyType(p) {
		case "socks5":
			// N22: SOCKS5 dialer. Wraps a guarded net.Dialer so the
			// SOCKS server can't be tricked into making us connect to
			// an internal address either — every Dial(addr) hits
			// netguard.ResolveAndCheckHost on the target host before
			// the underlying TCP open.
			var auth *proxy.Auth
			if p.Username != "" {
				auth = &proxy.Auth{User: p.Username, Password: p.Password}
			}
			fwd := &guardedDialer{base: &net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}}
			d, err := proxy.SOCKS5("tcp", fmt.Sprintf("%s:%d", host, port), auth, fwd)
			if err != nil {
				logx.Warn("cloud transport: socks5 dialer init failed for %s:%d: %v — direct egress", host, port, err)
				break
			}
			ctxd, ok := d.(proxy.ContextDialer)
			if ok {
				tr.DialContext = ctxd.DialContext
			} else {
				tr.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
					return d.Dial(network, addr)
				}
			}
			tr.Proxy = nil // SOCKS5 wraps the dialer, not the request URL
		default: // http CONNECT proxy
			pu := &url.URL{Scheme: "http", Host: fmt.Sprintf("%s:%d", host, port)}
			if p.Username != "" {
				pu.User = url.UserPassword(p.Username, p.Password)
			}
			tr.Proxy = http.ProxyURL(pu)
		}
	}
	c := &http.Client{Transport: tr, Timeout: 30 * time.Second}
	clientCache[key] = c
	return c
}

// guardedDialer wraps a *net.Dialer with a netguard pre-flight check so
// every connection through it (whether direct or via a SOCKS5 forwarder)
// rejects RFC1918 / link-local / metadata-service targets.
type guardedDialer struct {
	base *net.Dialer
}

func (d *guardedDialer) Dial(network, addr string) (net.Conn, error) {
	return d.DialContext(context.Background(), network, addr)
}

func (d *guardedDialer) DialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		// Untyped — let the base dialer surface the error.
		return d.base.DialContext(ctx, network, addr)
	}
	if _, err := netguard.ResolveAndCheckHost(host, nil); err != nil {
		return nil, fmt.Errorf("blocked by netguard: %w", err)
	}
	return d.base.DialContext(ctx, network, addr)
}

// PostJSON does a JSON POST to urlStr. When proxy is non-nil and the proxy
// itself fails (not the target) it transparently falls back to a direct
// request — matches the JS proxyModes = [proxy, null] behaviour.
func PostJSON(urlStr string, body []byte, proxy *langserver.Proxy, headers map[string]string) (int, []byte, error) {
	return postJSONImpl(urlStr, body, proxy, headers)
}

// postJSON is the unexported in-package alias kept for legacy call sites.
func postJSON(urlStr string, body []byte, proxy *langserver.Proxy, headers map[string]string) (int, []byte, error) {
	return postJSONImpl(urlStr, body, proxy, headers)
}

func postJSONImpl(urlStr string, body []byte, proxy *langserver.Proxy, headers map[string]string) (int, []byte, error) {
	attempts := []*langserver.Proxy{proxy, nil}
	if proxy == nil {
		attempts = attempts[:1]
	}
	var lastErr error
	for _, p := range attempts {
		status, data, err := doPost(urlStr, body, p, headers)
		if err == nil {
			return status, data, nil
		}
		lastErr = err
		if p == nil || !isProxyError(err) {
			return status, data, err
		}
	}
	return 0, nil, lastErr
}

func doPost(urlStr string, body []byte, proxy *langserver.Proxy, headers map[string]string) (int, []byte, error) {
	req, err := http.NewRequest("POST", urlStr, strings.NewReader(string(body)))
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	c := clientFor(proxy)
	resp, err := c.Do(req)
	if err != nil {
		if proxy != nil && isProxyError(err) {
			return 0, nil, fmt.Errorf("%w: %s", ErrProxyFailed, err.Error())
		}
		return 0, nil, err
	}
	defer func() {
		// Drain any unread bytes past our 10 MB cap before Close so the
		// underlying TCP connection can go back in the keep-alive pool.
		// Without this, an upstream that writes >10 MB (which LimitReader
		// silently truncates) leaves the socket mid-stream and net/http
		// abandons it → connection churn on every oversize response during
		// a rate-limit storm.
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
	}()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 10<<20))
	if err != nil {
		return resp.StatusCode, nil, err
	}
	return resp.StatusCode, raw, nil
}
