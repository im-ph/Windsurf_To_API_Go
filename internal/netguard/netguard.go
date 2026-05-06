// Package netguard centralises the SSRF / private-IP defence shared by every
// outbound network path in the proxy. It was extracted from dashapi.go +
// imagex.go (which had two near-identical copies) so that:
//
//  1. cloud/transport.go can apply the same guard to its proxy egress —
//     previously the proxy host (operator-supplied via the dashboard) was
//     dialed without any private-IP rejection, letting a leaked dashboard
//     password pivot to internal services (RFC1918 gateways, AWS / GCP /
//     Azure metadata, link-local).
//
//  2. The SOCKS5 dialer (cloud/transport.go via golang.org/x/net/proxy)
//     can apply the same guard before opening the TCP connection.
//
// Functions here are deliberately pure (no logging, no globals); callers
// log the decision themselves so they can attach context (proxy id,
// account email, request id) without netguard knowing about any of them.
package netguard

import (
	"errors"
	"net"
	"net/url"
	"strings"
)

// Errors returned by the guards. Callers should compare with errors.Is to
// distinguish "the operator typo'd a host" from "an attacker is trying to
// pivot via SSRF" — both rejected, but logged at different levels.
var (
	ErrPrivateIP   = errors.New("private/loopback IP not allowed for outbound proxy")
	ErrPrivateHost = errors.New("private/loopback hostname not allowed for outbound proxy")
	ErrInvalidHost = errors.New("invalid host")
)

// IsPrivateIP returns true for loopback, link-local, and RFC1918 ranges,
// PLUS the cloud-metadata link-local address (169.254.169.254). Both IPv4
// and IPv6 are checked. Mirrors the patterns already in dashapi.go and
// imagex.go.
func IsPrivateIP(ip net.IP) bool {
	if ip == nil {
		return true // be conservative on parse failures
	}
	if ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() ||
		ip.IsInterfaceLocalMulticast() || ip.IsMulticast() || ip.IsUnspecified() {
		return true
	}
	for _, cidr := range privateCIDRs {
		if cidr.Contains(ip) {
			return true
		}
	}
	return false
}

var privateCIDRs []*net.IPNet

func init() {
	for _, s := range []string{
		"10.0.0.0/8",      // RFC-1918
		"172.16.0.0/12",   // RFC-1918
		"192.168.0.0/16",  // RFC-1918
		"169.254.0.0/16",  // link-local (AWS / GCP / Azure metadata service IP lives here)
		"127.0.0.0/8",     // loopback (covered by IsLoopback but explicit for IPv6-mapped form)
		"100.64.0.0/10",   // CGNAT / RFC-6598 — not internal but uniformly private
		"fc00::/7",        // IPv6 unique-local
		"fe80::/10",       // IPv6 link-local
		"::1/128",         // IPv6 loopback
	} {
		_, n, err := net.ParseCIDR(s)
		if err == nil {
			privateCIDRs = append(privateCIDRs, n)
		}
	}
}

// IsPrivateHost returns true when host is a private literal IP, a known
// metadata-service hostname, OR localhost. Does NOT do DNS — pass through
// ResolveAndCheckHost when the host is a name and you want to catch DNS
// rebinding (an attacker-controlled DNS that resolves a public name to
// a private IP at the moment of dial).
func IsPrivateHost(host string) bool {
	host = strings.TrimSpace(host)
	host = strings.TrimPrefix(strings.TrimSuffix(host, "]"), "[")
	host = strings.ToLower(host)
	if host == "" {
		return true
	}
	if ip := net.ParseIP(host); ip != nil {
		return IsPrivateIP(ip)
	}
	switch host {
	case "localhost",
		"metadata.google.internal",
		"metadata.aws.internal",
		"169.254.169.254",
		"100.100.100.200":
		return true
	}
	// Anything ending in .local / .internal / .lan / .home is treated as
	// private — covers mDNS / corporate split-horizon DNS / consumer router
	// defaults. Operators behind those zones legitimately need internal
	// routing; that's what loopback bind-host mode is for.
	if strings.HasSuffix(host, ".local") ||
		strings.HasSuffix(host, ".internal") ||
		strings.HasSuffix(host, ".lan") ||
		strings.HasSuffix(host, ".home") {
		return true
	}
	return false
}

// ResolveAndCheckHost looks up host (using the provided resolver, or the
// system default when nil) and returns the resolved IPs. Returns
// ErrPrivateHost if host is a known-private literal/sentinel and
// ErrPrivateIP if any resolved address is in a private range.
//
// Use this BEFORE dialing any operator-supplied proxy host. The DNS
// lookup result is intentionally returned so callers can pass the IP
// back to their dialer to dodge a TOCTOU race against a hostile resolver
// that might answer with a public IP twice and a private IP on the third
// dial — bind to the first answer, don't re-resolve at dial time.
func ResolveAndCheckHost(host string, resolver *net.Resolver) ([]net.IP, error) {
	if IsPrivateHost(host) {
		return nil, ErrPrivateHost
	}
	if ip := net.ParseIP(host); ip != nil {
		// Already an IP literal — IsPrivateHost above would have caught
		// the private case, so this is a public IP. Return it unchanged.
		return []net.IP{ip}, nil
	}
	r := resolver
	if r == nil {
		r = net.DefaultResolver
	}
	ips, err := r.LookupIP(nil, "ip", host)
	if err != nil {
		return nil, err
	}
	if len(ips) == 0 {
		return nil, ErrInvalidHost
	}
	for _, ip := range ips {
		if IsPrivateIP(ip) {
			return nil, ErrPrivateIP
		}
	}
	return ips, nil
}

// CheckProxyURL validates a proxy URL string and returns the parsed URL.
// The proxy host MUST resolve to a public address; the function applies
// ResolveAndCheckHost internally. Used by cloud/transport.go before
// installing http.Transport.Proxy.
func CheckProxyURL(rawURL string) (*url.URL, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil, err
	}
	host := u.Hostname()
	if host == "" {
		return nil, ErrInvalidHost
	}
	if _, err := ResolveAndCheckHost(host, nil); err != nil {
		return nil, err
	}
	return u, nil
}
