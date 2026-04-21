// Package cache is an exact-body LRU response cache keyed on the request's
// semantically meaningful fields. Mirrors src/cache.js.
package cache

import (
	"container/list"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sync"
	"sync/atomic"
	"time"
)

const (
	ttl      = 5 * time.Minute
	maxItems = 500
)

// Entry is what callers read/write. Text + Thinking are the only replay
// channels today.
type Entry struct {
	Text     string
	Thinking string
}

type record struct {
	key       string
	value     Entry
	expiresAt time.Time
}

// Stats is a snapshot for the dashboard.
type Stats struct {
	Size       int    `json:"size"`
	MaxSize    int    `json:"maxSize"`
	TTLMs      int64  `json:"ttlMs"`
	Hits       uint64 `json:"hits"`
	Misses     uint64 `json:"misses"`
	Stores     uint64 `json:"stores"`
	Evictions  uint64 `json:"evictions"`
	HitRatePct string `json:"hitRate"`
}

var (
	mu    sync.Mutex
	order = list.New()
	idx   = map[string]*list.Element{}

	hits, misses, stores, evictions uint64
)

// KeyFromRequest hashes the subset of the request body that actually changes
// semantic output — matches src/cache.js normalize().
type RequestBody struct {
	Model       string          `json:"model"`
	Messages    json.RawMessage `json:"messages"`
	Tools       json.RawMessage `json:"tools,omitempty"`
	ToolChoice  json.RawMessage `json:"tool_choice,omitempty"`
	Temperature *float64        `json:"temperature,omitempty"`
	TopP        *float64        `json:"top_p,omitempty"`
	MaxTokens   *int            `json:"max_tokens,omitempty"`
}

func Key(body RequestBody) string {
	b, _ := json.Marshal(body)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// Get returns a non-expired entry and refreshes its LRU position.
func Get(k string) (Entry, bool) {
	mu.Lock()
	defer mu.Unlock()
	el, ok := idx[k]
	if !ok {
		atomic.AddUint64(&misses, 1)
		return Entry{}, false
	}
	r := el.Value.(*record)
	if time.Now().After(r.expiresAt) {
		order.Remove(el)
		delete(idx, k)
		atomic.AddUint64(&misses, 1)
		return Entry{}, false
	}
	order.MoveToBack(el)
	atomic.AddUint64(&hits, 1)
	return r.value, true
}

// Set replaces or inserts an entry; evicts oldest when over capacity.
func Set(k string, v Entry) {
	if v.Text == "" && v.Thinking == "" {
		return
	}
	mu.Lock()
	defer mu.Unlock()
	if el, ok := idx[k]; ok {
		r := el.Value.(*record)
		r.value = v
		r.expiresAt = time.Now().Add(ttl)
		order.MoveToBack(el)
		return
	}
	r := &record{key: k, value: v, expiresAt: time.Now().Add(ttl)}
	el := order.PushBack(r)
	idx[k] = el
	atomic.AddUint64(&stores, 1)
	for order.Len() > maxItems {
		front := order.Front()
		if front == nil {
			break
		}
		order.Remove(front)
		delete(idx, front.Value.(*record).key)
		atomic.AddUint64(&evictions, 1)
	}
}

// Clear wipes everything.
func Clear() {
	mu.Lock()
	order.Init()
	idx = map[string]*list.Element{}
	mu.Unlock()
	atomic.StoreUint64(&hits, 0)
	atomic.StoreUint64(&misses, 0)
	atomic.StoreUint64(&stores, 0)
	atomic.StoreUint64(&evictions, 0)
}

// Snapshot returns a copy for the dashboard.
func Snapshot() Stats {
	mu.Lock()
	size := order.Len()
	mu.Unlock()
	h := atomic.LoadUint64(&hits)
	m := atomic.LoadUint64(&misses)
	rate := "0.0"
	if total := h + m; total > 0 {
		rate = fmtFloat(float64(h) / float64(total) * 100)
	}
	return Stats{
		Size: size, MaxSize: maxItems, TTLMs: ttl.Milliseconds(),
		Hits: h, Misses: m,
		Stores:    atomic.LoadUint64(&stores),
		Evictions: atomic.LoadUint64(&evictions),
		HitRatePct: rate,
	}
}

func fmtFloat(f float64) string {
	// Match JS toFixed(1) — one decimal, no trailing zero trim.
	buf := make([]byte, 0, 8)
	intPart := int(f)
	buf = appendInt(buf, intPart)
	buf = append(buf, '.')
	frac := int((f - float64(intPart)) * 10)
	if frac < 0 {
		frac = -frac
	}
	buf = append(buf, byte('0'+frac))
	return string(buf)
}

func appendInt(dst []byte, n int) []byte {
	if n == 0 {
		return append(dst, '0')
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
	return append(dst, tmp[i:]...)
}
