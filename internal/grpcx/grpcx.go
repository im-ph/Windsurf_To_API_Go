// Package grpcx is a minimal gRPC-over-HTTP/2 client for the local Windsurf
// language-server binary. The LS speaks cleartext HTTP/2 (h2c), so we use
// golang.org/x/net/http2 with AllowHTTP=true. Only unary and server-streaming
// are needed — those are all the Windsurf RPCs we ever call.
package grpcx

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"time"

	"golang.org/x/net/http2"
)

// Frame: 1 byte compression (0) + 4 bytes BE length + payload.
func Frame(payload []byte) []byte {
	buf := make([]byte, 5+len(payload))
	buf[0] = 0
	binary.BigEndian.PutUint32(buf[1:5], uint32(len(payload)))
	copy(buf[5:], payload)
	return buf
}

// StripFrame strips a single gRPC frame header.
func StripFrame(b []byte) []byte {
	if len(b) >= 5 && b[0] == 0 {
		n := binary.BigEndian.Uint32(b[1:5])
		if uint32(len(b)) >= 5+n {
			return b[5 : 5+n]
		}
	}
	return b
}

// ─── Client ─────────────────────────────────────────────────

// Client is reusable across many requests. One per unique LS port is enough.
type Client struct {
	port     int
	csrf     string
	hc       *http.Client
	baseURL  string
}

// New constructs a Client aimed at http://127.0.0.1:<port>. The h2c transport
// dials plain TCP and upgrades — identical to what Node's http2.connect does.
//
// The host literal is IPv4 (not "localhost") on purpose: on Linux boxes with
// an IPv6 stack, "localhost" resolves to both 127.0.0.1 and ::1, Go picks
// ::1 first under Happy Eyeballs, and the LS only listens on IPv4 — producing
// "dial tcp [::1]:<port>: connect: connection refused" even though IPv4 is
// fine. Dialling 127.0.0.1 outright sidesteps the resolver entirely.
func New(port int, csrf string) *Client {
	tr := &http2.Transport{
		AllowHTTP: true,
		// h2c: bypass TLS even though http2.Transport normally expects it.
		// DialTLSContext is called for both http:// and https:// origins
		// when AllowHTTP is set, so returning a plain TCP conn is correct.
		DialTLSContext: func(ctx context.Context, network, addr string, _ *tls.Config) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, network, addr)
		},
	}
	hc := &http.Client{Transport: tr}
	return &Client{
		port:    port,
		csrf:    csrf,
		hc:      hc,
		baseURL: fmt.Sprintf("http://127.0.0.1:%d", port),
	}
}

// Close frees any idle connections.
func (c *Client) Close() {
	if tr, ok := c.hc.Transport.(*http2.Transport); ok {
		tr.CloseIdleConnections()
	}
}

// Unary performs a unary gRPC call. body is a framed payload built with Frame().
// Returns the unwrapped protobuf payload (frame stripped).
func (c *Client) Unary(ctx context.Context, path string, body []byte, timeout time.Duration) ([]byte, error) {
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	u := c.baseURL + path
	if _, err := url.Parse(u); err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, "POST", u, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/grpc")
	req.Header.Set("TE", "trailers")
	req.Header.Set("x-codeium-csrf-token", c.csrf)

	resp, err := c.hc.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	// gRPC frames can pre-empt trailers, so drain all frames first.
	// Cap at 64 MB to prevent an oversized LS response from exhausting memory.
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	if err != nil {
		return nil, err
	}

	if status := resp.Trailer.Get("grpc-status"); status != "" && status != "0" {
		msg := resp.Trailer.Get("grpc-message")
		if msg == "" {
			msg = "grpc status " + status
		} else if dec, derr := url.QueryUnescape(msg); derr == nil {
			msg = dec
		}
		return nil, fmt.Errorf("%s", msg)
	}
	return StripFrame(raw), nil
}

// StreamFrame is one decoded frame on the wire.
type StreamFrame struct {
	Payload []byte
}

// Stream performs a server-streaming gRPC call. onFrame is invoked for every
// received frame. Returns when the server closes the stream or ctx is done.
func (c *Client) Stream(ctx context.Context, path string, body []byte, timeout time.Duration, onFrame func([]byte)) error {
	if timeout <= 0 {
		timeout = 5 * time.Minute
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, "POST", c.baseURL+path, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/grpc")
	req.Header.Set("TE", "trailers")
	req.Header.Set("x-codeium-csrf-token", c.csrf)

	resp, err := c.hc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	r := bufio.NewReader(resp.Body)
	for {
		hdr := make([]byte, 5)
		if _, err := io.ReadFull(r, hdr); err != nil {
			if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
				break
			}
			return err
		}
		n := binary.BigEndian.Uint32(hdr[1:5])
		// Guard against a malformed or hostile LS response allocating huge slices.
		// 64 MB matches the cap already applied to Unary responses.
		const maxFrameBytes = 64 << 20
		if n > maxFrameBytes {
			return fmt.Errorf("grpc stream: frame size %d exceeds limit %d", n, maxFrameBytes)
		}
		payload := make([]byte, n)
		if _, err := io.ReadFull(r, payload); err != nil {
			return err
		}
		if hdr[0] == 0 {
			onFrame(payload)
		}
	}

	if status := resp.Trailer.Get("grpc-status"); status != "" && status != "0" {
		msg := resp.Trailer.Get("grpc-message")
		if dec, derr := url.QueryUnescape(msg); derr == nil {
			msg = dec
		}
		if msg == "" {
			msg = "grpc status " + status
		}
		return fmt.Errorf("%s", msg)
	}
	return nil
}
