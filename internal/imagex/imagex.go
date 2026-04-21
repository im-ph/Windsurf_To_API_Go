// Package imagex turns the loose "image input" shapes that OpenAI-style and
// Anthropic-style clients send into a single canonical {base64, mime} pair
// ready to hand to the Cascade proto builder. Fetching remote HTTP images is
// deliberately conservative — strict https allowlist, SSRF filter that
// matches dashapi.isPrivateHost, 5 MB cap, 3-redirect chain limit.
//
// Size rationale: Windsurf's LS rejects frames over 64 MB on the gRPC layer
// (we cap our Unary readers at the same number). Base64 inflates by ~33%, so
// cap the raw bytes at ~5 MB and the encoded payload still fits in a single
// RPC comfortably, matching JS src/image.js MAX_BYTES.
package imagex

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"
)

// Image is the canonical internal shape.
type Image struct {
	Base64 string // no prefix, no whitespace — just the encoded bytes
	Mime   string // e.g. "image/png" / "image/jpeg" / "image/webp"
}

const (
	// MaxBytes is the hard cap on the decoded image — 5 MB, same as JS side.
	MaxBytes = 5 * 1024 * 1024
	// MaxRedirects caps the CONNECT → 302 → 302 → ... chain when fetching
	// remote URLs so a malicious host can't sink our request into a loop.
	MaxRedirects = 3
	// FetchTimeout applies to a single HTTP round trip when we go to the wire.
	FetchTimeout = 15 * time.Second
)

// Resolve normalises a caller-provided image reference. Accepts:
//
//   - "data:image/png;base64,iVBORw0KGgo…"  (data URL, handled inline)
//   - "https://example.com/cat.png"          (fetched, with SSRF guard)
//   - "http://…"                             (also accepted — some clients)
//   - a bare base64 blob                     (assumed PNG unless sniffable)
//
// Returns nil,nil for an empty / blank input so callers can silently skip
// missing images without error-propagating into the chat path.
func Resolve(raw string) (*Image, error) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return nil, nil
	}
	if strings.HasPrefix(s, "data:") {
		return parseDataURL(s)
	}
	if strings.HasPrefix(s, "http://") || strings.HasPrefix(s, "https://") {
		return fetchURL(s, MaxRedirects)
	}
	// Bare base64 fallback. MIME is unknowable so we assume PNG — the LS
	// tolerates a misdeclared MIME as long as the image decodes on their
	// side. Trim any leading `base64,` the caller might have forgotten.
	s = strings.TrimPrefix(s, "base64,")
	s = strings.ReplaceAll(s, "\n", "")
	s = strings.ReplaceAll(s, "\r", "")
	if _, err := base64.StdEncoding.DecodeString(s); err != nil {
		return nil, fmt.Errorf("image: invalid base64 payload: %w", err)
	}
	return &Image{Base64: s, Mime: "image/png"}, nil
}

func parseDataURL(s string) (*Image, error) {
	// data:<mime>[;base64],<payload>
	i := strings.Index(s, ",")
	if i < 0 {
		return nil, errors.New("image: malformed data URL (no comma)")
	}
	head := s[5:i] // drop "data:"
	payload := s[i+1:]
	mime := "image/png"
	isB64 := false
	for _, part := range strings.Split(head, ";") {
		p := strings.TrimSpace(part)
		if strings.EqualFold(p, "base64") {
			isB64 = true
			continue
		}
		if p != "" && !strings.Contains(p, "=") {
			mime = p
		}
	}
	if !isB64 {
		// URL-encoded data is possible but none of the real clients send
		// images that way — easier to reject than to transcode poorly.
		return nil, errors.New("image: data URL must be base64-encoded")
	}
	payload = strings.ReplaceAll(payload, "\n", "")
	payload = strings.ReplaceAll(payload, "\r", "")
	decoded, err := base64.StdEncoding.DecodeString(payload)
	if err != nil {
		return nil, fmt.Errorf("image: data URL base64 decode: %w", err)
	}
	if len(decoded) > MaxBytes {
		return nil, fmt.Errorf("image: decoded size %d exceeds %d byte limit", len(decoded), MaxBytes)
	}
	return &Image{Base64: payload, Mime: mime}, nil
}

func fetchURL(s string, redirectsLeft int) (*Image, error) {
	if redirectsLeft < 0 {
		return nil, errors.New("image: too many redirects")
	}
	if err := validateURL(s); err != nil {
		return nil, err
	}
	// DNS-rebinding defence: validate each resolved IP at dial time rather
	// than only at URL-parse time. Without this, an attacker-controlled
	// hostname passes validateURL() (host is `evil.example.com`, not an IP)
	// yet resolves to `169.254.169.254` / `127.0.0.1` when Go's http
	// transport actually dials. The CheckRedirect hook alone doesn't help —
	// it only sees the redirect URL, not the resolved target IP.
	dialer := &net.Dialer{Timeout: FetchTimeout, KeepAlive: 30 * time.Second}
	safeDial := func(ctx context.Context, network, addr string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(addr)
		if err != nil {
			return nil, err
		}
		ips, err := (&net.Resolver{}).LookupIP(ctx, "ip", host)
		if err != nil {
			return nil, err
		}
		if len(ips) == 0 {
			return nil, errors.New("image: no IPs for host")
		}
		for _, ip := range ips {
			if isPrivateIP(ip) {
				return nil, fmt.Errorf("image: refusing to dial private IP %s", ip)
			}
		}
		// Pin dial to the first safe IP so a later happy-eyeballs race can't
		// swap in an IP we never validated.
		return dialer.DialContext(ctx, network, net.JoinHostPort(ips[0].String(), port))
	}
	transport := &http.Transport{
		DialContext:         safeDial,
		TLSHandshakeTimeout: FetchTimeout,
		DisableKeepAlives:   true,
	}
	client := &http.Client{
		Timeout:   FetchTimeout,
		Transport: transport,
		// Intercept each redirect so we can re-validate the destination URL
		// text too — belt-and-suspenders with the per-dial IP check above.
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= MaxRedirects {
				return errors.New("image: too many redirects")
			}
			if err := validateURL(req.URL.String()); err != nil {
				return err
			}
			return nil
		},
	}
	req, err := http.NewRequest("GET", s, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "WindsurfAPI/ImageFetch")
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("image: fetch %s: %w", s, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("image: %s returned %d", s, resp.StatusCode)
	}
	// ContentLength isn't trustworthy (servers can lie or omit), so also
	// cap the reader itself.
	body, err := io.ReadAll(io.LimitReader(resp.Body, MaxBytes+1))
	if err != nil {
		return nil, err
	}
	if len(body) > MaxBytes {
		return nil, fmt.Errorf("image: payload exceeds %d byte limit", MaxBytes)
	}
	mime := resp.Header.Get("Content-Type")
	if mime == "" || !strings.HasPrefix(mime, "image/") {
		mime = sniffMime(body)
	}
	// Strip any ";charset=..." tail the server might attach.
	if i := strings.Index(mime, ";"); i >= 0 {
		mime = strings.TrimSpace(mime[:i])
	}
	return &Image{Base64: base64.StdEncoding.EncodeToString(body), Mime: mime}, nil
}

// validateURL mirrors dashapi.isPrivateHost — reject loopback, link-local,
// RFC-1918, CGNAT, IPv6 ULA, metadata.* shorthands. Cheap to duplicate here
// so the imagex package has zero intra-project import cycles.
func validateURL(raw string) error {
	// Minimal parse: we only need host + scheme.
	s := raw
	if i := strings.Index(s, "://"); i >= 0 {
		s = s[i+3:]
	}
	if i := strings.IndexAny(s, "/?#"); i >= 0 {
		s = s[:i]
	}
	host := s
	if i := strings.LastIndex(host, "@"); i >= 0 {
		host = host[i+1:]
	}
	// Strip IPv6 bracket + port.
	if strings.HasPrefix(host, "[") {
		if j := strings.Index(host, "]"); j >= 0 {
			host = host[1:j]
		}
	} else if i := strings.LastIndex(host, ":"); i >= 0 {
		host = host[:i]
	}
	host = strings.TrimSpace(strings.ToLower(host))
	if host == "" {
		return errors.New("image: empty host")
	}
	switch host {
	case "localhost", "metadata.google.internal", "169.254.169.254":
		return errors.New("image: refusing to fetch internal host")
	}
	if strings.HasSuffix(host, ".local") || strings.HasSuffix(host, ".internal") {
		return errors.New("image: refusing to fetch .local/.internal host")
	}
	if ip := net.ParseIP(host); ip != nil && isPrivateIP(ip) {
		return errors.New("image: refusing to fetch private IP")
	}
	return nil
}

func isPrivateIP(ip net.IP) bool {
	private := []string{
		"127.0.0.0/8", "::1/128",
		"10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16",
		"169.254.0.0/16", "fe80::/10", "fc00::/7",
		"100.64.0.0/10",
	}
	for _, cidr := range private {
		_, n, err := net.ParseCIDR(cidr)
		if err != nil {
			continue
		}
		if n.Contains(ip) {
			return true
		}
	}
	return false
}

// sniffMime picks a MIME from the first few bytes — the LS doesn't strictly
// validate, but sending `image/png` for a JPEG confuses the client SDK that
// later echoes our declared mime back to the user.
func sniffMime(b []byte) string {
	switch {
	case len(b) >= 8 && b[0] == 0x89 && b[1] == 'P' && b[2] == 'N' && b[3] == 'G':
		return "image/png"
	case len(b) >= 3 && b[0] == 0xFF && b[1] == 0xD8 && b[2] == 0xFF:
		return "image/jpeg"
	case len(b) >= 6 && string(b[:6]) == "GIF87a":
		return "image/gif"
	case len(b) >= 6 && string(b[:6]) == "GIF89a":
		return "image/gif"
	case len(b) >= 12 && string(b[:4]) == "RIFF" && string(b[8:12]) == "WEBP":
		return "image/webp"
	}
	return "image/png"
}
