// Package cloud — HTTPS transport + HTTP CONNECT tunnelling helpers shared by
// the Connect-RPC client and the Firebase sign-in flow.
//
// net/http's stdlib transport handles CONNECT-based proxying automatically
// when Transport.Proxy returns a non-nil URL, so all we need here is a small
// cache of per-proxy clients plus a "try-via-proxy-then-direct" fallback.
package cloud

import (
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"

	"windsurfapi/internal/langserver"
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

// clientFor returns a cached *http.Client configured for the given proxy
// (or direct when p is nil). Clients are shared across requests for
// connection reuse.
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
		port := p.Port
		if port == 0 {
			port = 8080
		}
		host := p.Host
		if strings.Contains(host, ":") {
			// Strip any trailing :port — JS does the same cleanup.
			if i := strings.LastIndex(host, ":"); i > 0 {
				host = host[:i]
			}
		}
		pu := &url.URL{Scheme: "http", Host: fmt.Sprintf("%s:%d", host, port)}
		if p.Username != "" {
			pu.User = url.UserPassword(p.Username, p.Password)
		}
		tr.Proxy = http.ProxyURL(pu)
	}
	c := &http.Client{Transport: tr, Timeout: 30 * time.Second}
	clientCache[key] = c
	return c
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
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 10<<20))
	if err != nil {
		return resp.StatusCode, nil, err
	}
	return resp.StatusCode, raw, nil
}
