// Package convpool is the Cascade cascade_id reuse pool. Reusing a cascade
// across turns lets Windsurf keep its own context cached server-side, so
// latency on long conversations drops sharply. Entries are pinned to a
// specific (apiKey, lsPort) pair.
package convpool

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sync"
	"sync/atomic"
	"time"
)

const (
	ttl  = 10 * time.Minute
	max_ = 500
)

// Entry is what callers check in and out.
type Entry struct {
	CascadeID  string
	SessionID  string
	LSPort     int
	APIKey     string
	CreatedAt  time.Time
	lastAccess time.Time
}

// Message is the minimum shape the fingerprint function needs.
type Message struct {
	Role    string `json:"role"`
	Content any    `json:"content"`
}

type Stats struct {
	Size       int    `json:"size"`
	MaxSize    int    `json:"maxSize"`
	TTLMs      int64  `json:"ttlMs"`
	Hits       uint64 `json:"hits"`
	Misses     uint64 `json:"misses"`
	Stores     uint64 `json:"stores"`
	Evictions  uint64 `json:"evictions"`
	Expired    uint64 `json:"expired"`
	HitRatePct string `json:"hitRate"`
}

var (
	mu    sync.Mutex
	pool_ = map[string]*Entry{}
	hits, misses, stores, evictions, expired uint64
)

func canonicaliseContent(c any) string {
	switch v := c.(type) {
	case string:
		return v
	case []any:
		var b []byte
		for _, p := range v {
			if m, ok := p.(map[string]any); ok {
				if t, ok := m["text"].(string); ok {
					b = append(b, t...)
					continue
				}
				raw, _ := json.Marshal(m)
				b = append(b, raw...)
			}
		}
		return string(b)
	default:
		b, _ := json.Marshal(v)
		return string(b)
	}
}

type canonMsg struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

func canonicalise(msgs []Message) []canonMsg {
	out := make([]canonMsg, len(msgs))
	for i, m := range msgs {
		out[i] = canonMsg{Role: m.Role, Content: canonicaliseContent(m.Content)}
	}
	return out
}

func sha256Hex(in string) string {
	s := sha256.Sum256([]byte(in))
	return hex.EncodeToString(s[:])
}

// FingerprintBefore returns the hash for "resume this conversation", based on
// every message except the latest user turn. Nil when nothing to resume.
//
// clientSalt partitions the fingerprint by caller so two independent
// clients that happen to send an identical message history (common when
// Claude Code's fixed system prompt + CLAUDE.md template collides across
// new sessions) don't end up sharing the same cascade_id and bleeding
// context into each other. Pass the client IP (or any per-caller token)
// — an empty salt disables partitioning, which is ONLY safe for
// single-user deployments.
func FingerprintBefore(msgs []Message, clientSalt string) string {
	if len(msgs) < 2 {
		return ""
	}
	hist := msgs[:len(msgs)-1]
	seenAsst := false
	for _, m := range hist {
		if m.Role == "assistant" {
			seenAsst = true
			break
		}
	}
	if !seenAsst {
		return ""
	}
	b, _ := json.Marshal(canonicalise(hist))
	return sha256Hex(clientSalt + "\x00" + string(b))
}

// FingerprintAfter is what the NEXT request's FingerprintBefore will look up.
// clientSalt must match what FingerprintBefore will receive on the next turn.
func FingerprintAfter(msgs []Message, assistantText, clientSalt string) string {
	full := append([]Message{}, msgs...)
	full = append(full, Message{Role: "assistant", Content: assistantText})
	b, _ := json.Marshal(canonicalise(full))
	return sha256Hex(clientSalt + "\x00" + string(b))
}

// Checkout removes and returns the matching entry, or nil on miss / expired.
func Checkout(fp string) *Entry {
	if fp == "" {
		incr(&misses)
		return nil
	}
	mu.Lock()
	defer mu.Unlock()
	e, ok := pool_[fp]
	if !ok {
		incr(&misses)
		return nil
	}
	delete(pool_, fp)
	if time.Since(e.lastAccess) > ttl {
		incr(&expired)
		return nil
	}
	incr(&hits)
	return e
}

// Checkin stores an entry under the post-turn fingerprint.
func Checkin(fp string, e Entry) {
	if fp == "" {
		return
	}
	mu.Lock()
	defer mu.Unlock()
	e.lastAccess = time.Now()
	if e.CreatedAt.IsZero() {
		e.CreatedAt = e.lastAccess
	}
	pool_[fp] = &e
	incr(&stores)
	if len(pool_) > max_ {
		// Evict oldest lastAccess.
		var oldestKey string
		var oldestTime time.Time
		first := true
		for k, v := range pool_ {
			if first || v.lastAccess.Before(oldestTime) {
				oldestKey = k
				oldestTime = v.lastAccess
				first = false
			}
		}
		if oldestKey != "" {
			delete(pool_, oldestKey)
			incr(&evictions)
		}
	}
}

// InvalidateFor drops entries owned by (apiKey, lsPort). Used when an account
// is removed or an LS port exits.
func InvalidateFor(apiKey string, lsPort int) int {
	mu.Lock()
	defer mu.Unlock()
	n := 0
	for k, e := range pool_ {
		if (apiKey != "" && e.APIKey == apiKey) || (lsPort != 0 && e.LSPort == lsPort) {
			delete(pool_, k)
			n++
		}
	}
	return n
}

// Clear wipes the pool and returns how many entries were dropped.
func Clear() int {
	mu.Lock()
	defer mu.Unlock()
	n := len(pool_)
	pool_ = map[string]*Entry{}
	return n
}

// Snapshot returns pool stats for the dashboard.
func Snapshot() Stats {
	mu.Lock()
	size := len(pool_)
	mu.Unlock()
	h := load(&hits)
	m := load(&misses)
	rate := "0.0"
	if total := h + m; total > 0 {
		rate = fmtFloat(float64(h) / float64(total) * 100)
	}
	return Stats{
		Size: size, MaxSize: max_, TTLMs: ttl.Milliseconds(),
		Hits: h, Misses: m,
		Stores: load(&stores), Evictions: load(&evictions), Expired: load(&expired),
		HitRatePct: rate,
	}
}

// Atomic counter helpers — previously bare `*p++` / `*p`. Even though `Snapshot`
// and `incr` are mostly reached while holding `mu`, `Snapshot` releases the
// lock before reading counters, so `load(&hits)` and a concurrent `incr(&hits)`
// racing under `mu` produce a Go-vet race flag. Go's `atomic` primitives cost
// the same nanosecond on amd64 and silence both the vet warning and any
// torn-read risk on ARM64 deployments.
func incr(p *uint64) { atomic.AddUint64(p, 1) }
func load(p *uint64) uint64 { return atomic.LoadUint64(p) }

func fmtFloat(f float64) string {
	intPart := int(f)
	frac := int((f - float64(intPart)) * 10)
	if frac < 0 {
		frac = -frac
	}
	return itoa(intPart) + "." + string('0'+byte(frac))
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var tmp [12]byte
	i := len(tmp)
	for n > 0 {
		i--
		tmp[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		tmp[i] = '-'
	}
	return string(tmp[i:])
}
