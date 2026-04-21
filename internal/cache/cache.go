// Package cache is a disk-backed LRU response cache. Keys are SHA-256
// hashes of the normalised request body; each Entry's Text+Thinking is
// written to its own file, not held in Go heap memory.
//
// Storage strategy — why disk, not RAM:
//
// The service runs on a small VM (≤1 GB RAM typical). With maxItems=6000
// and typical text around 2-6 KB per response, a purely in-RAM cache can
// grow to 10-30 MB and fight the account pool / LS process for memory.
// Pushing the cache to disk moves that pressure off the Go heap entirely:
//
//   - Path defaults to /tmp/windsurfapi-cache/ — on systemd Debian, /tmp is
//     tmpfs (backed by swap). Entries live in RAM while memory is cheap,
//     but the kernel transparently pages cold files out to swap under
//     pressure. Net effect: "cache lives in SWAP" as requested.
//   - Setting CACHE_PATH to a regular-disk directory (e.g. /opt/…/cache)
//     instead puts the cache on persistent disk — it survives restarts
//     and the OS page cache + swap handle memory pressure for free.
//
// RAM footprint of the in-process bookkeeping:
//
//   per entry:  64-char hex key + filepath + expiresAt + list node ≈ 200 B
//   6000 entries × 200 B ≈ 1.2 MB — negligible.
package cache

import (
	"container/list"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"windsurfapi/internal/atomicfile"
)

// Defaults — overridden by Init() when called with non-empty args.
const (
	defaultTTL      = 2 * time.Hour
	defaultMaxItems = 6000
	defaultDir      = "/tmp/windsurfapi-cache"
)

// Entry is what callers read/write. Text + Thinking are the only replay
// channels today.
type Entry struct {
	Text     string `json:"text"`
	Thinking string `json:"thinking,omitempty"`
}

// record is the in-RAM bookkeeping. The *value* is NOT held here — Get
// reads from the file when the caller asks.
type record struct {
	key       string
	expiresAt time.Time
	sizeBytes int
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
	// Backing path + resident byte count — handy when debugging "where is
	// my cache actually living" questions from the dashboard.
	Backing    string `json:"backing"`
	BytesOnDisk int64 `json:"bytesOnDisk"`
}

var (
	mu    sync.Mutex
	order = list.New()
	idx   = map[string]*list.Element{}

	ttl        = defaultTTL
	maxItems   = defaultMaxItems
	dir        = defaultDir
	bytesOnDisk int64

	hits, misses, stores, evictions uint64
)

// Init configures the cache. Safe to call from main() before first use.
// Pass zero / empty to keep defaults. Missing backing dir is created with
// 0o700 so only the service user can read pooled cache bodies.
func Init(cacheDir string, maxEntries int, entryTTL time.Duration) {
	mu.Lock()
	defer mu.Unlock()
	if cacheDir != "" {
		dir = cacheDir
	}
	if maxEntries > 0 {
		maxItems = maxEntries
	}
	if entryTTL > 0 {
		ttl = entryTTL
	}
	_ = os.MkdirAll(dir, 0o700)
	// Reload whatever is still on disk — cache survives restart on
	// persistent-disk backings, and on tmpfs it at least survives within
	// the same uptime when we happen to bounce the service.
	reloadFromDisk()
}

// KeyFromRequest hashes the subset of the request body that actually changes
// semantic output — matches src/cache.js normalize().
//
// IdentityPrompt is a server-side toggle (runtimecfg flag), NOT part of the
// client's request. Without including it in the key, flipping the flag
// between request A (stamped) and request B (not stamped) lets B re-serve
// A's response — the cached text still carries whatever identity was
// injected at generation time. Participating the flag's identity string
// in the key sidesteps that confusion; toggling it off naturally misses
// previously-stamped cache entries.
type RequestBody struct {
	Model          string          `json:"model"`
	Messages       json.RawMessage `json:"messages"`
	Tools          json.RawMessage `json:"tools,omitempty"`
	ToolChoice     json.RawMessage `json:"tool_choice,omitempty"`
	Temperature    *float64        `json:"temperature,omitempty"`
	TopP           *float64        `json:"top_p,omitempty"`
	MaxTokens      *int            `json:"max_tokens,omitempty"`
	IdentityPrompt string          `json:"identityPrompt,omitempty"`
}

func Key(body RequestBody) string {
	b, _ := json.Marshal(body)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// Get returns a non-expired entry and refreshes its LRU position. The value
// is read from disk on the fly — cheap enough for tmpfs / SSD and avoids
// pinning response bodies in the Go heap.
func Get(k string) (Entry, bool) {
	mu.Lock()
	el, ok := idx[k]
	if !ok {
		mu.Unlock()
		atomic.AddUint64(&misses, 1)
		return Entry{}, false
	}
	r := el.Value.(*record)
	if time.Now().After(r.expiresAt) {
		removeLocked(el, r)
		mu.Unlock()
		atomic.AddUint64(&misses, 1)
		return Entry{}, false
	}
	order.MoveToBack(el)
	path := pathFor(r.key)
	mu.Unlock()

	raw, err := os.ReadFile(path)
	if err != nil {
		// File disappeared under us (evicted by another goroutine,
		// manually cleared, or tmpfs truncated). Treat as miss.
		mu.Lock()
		if el2, ok := idx[k]; ok && el2 == el {
			removeLocked(el, r)
		}
		mu.Unlock()
		atomic.AddUint64(&misses, 1)
		return Entry{}, false
	}
	var e Entry
	if json.Unmarshal(raw, &e) != nil {
		// Corrupt blob — previously we just returned miss but left the
		// index entry in place, which made every subsequent Get re-read
		// the same bad file and re-report miss. Clean it up so the next
		// Set gets a fresh slot.
		mu.Lock()
		if el2, ok := idx[k]; ok && el2 == el {
			removeLocked(el, r)
		}
		mu.Unlock()
		atomic.AddUint64(&misses, 1)
		return Entry{}, false
	}
	atomic.AddUint64(&hits, 1)
	return e, true
}

// Set replaces or inserts an entry; evicts oldest when over capacity.
func Set(k string, v Entry) {
	if v.Text == "" && v.Thinking == "" {
		return
	}
	blob, err := json.Marshal(v)
	if err != nil {
		return
	}
	path := pathFor(k)
	// atomicfile.Write generates a unique per-call tmp name + 0o600 so two
	// concurrent Set(sameKey) calls can't clobber each other's .tmp draft —
	// the shared `<path>.tmp` collision was the same class of bug round 1
	// fixed in the auth/proxycfg/modelaccess/runtimecfg/stats persistence
	// paths; cache Set got missed in that sweep.
	if err := atomicfile.Write(path, blob); err != nil {
		return
	}

	mu.Lock()
	defer mu.Unlock()
	if el, ok := idx[k]; ok {
		r := el.Value.(*record)
		atomic.AddInt64(&bytesOnDisk, int64(len(blob)-r.sizeBytes))
		r.expiresAt = time.Now().Add(ttl)
		r.sizeBytes = len(blob)
		order.MoveToBack(el)
		return
	}
	r := &record{key: k, expiresAt: time.Now().Add(ttl), sizeBytes: len(blob)}
	el := order.PushBack(r)
	idx[k] = el
	atomic.AddInt64(&bytesOnDisk, int64(len(blob)))
	atomic.AddUint64(&stores, 1)
	for order.Len() > maxItems {
		front := order.Front()
		if front == nil {
			break
		}
		fr := front.Value.(*record)
		removeLocked(front, fr)
		atomic.AddUint64(&evictions, 1)
	}
}

// removeLocked deletes the LRU node, the index entry, and the backing file.
// Caller holds mu.
func removeLocked(el *list.Element, r *record) {
	order.Remove(el)
	delete(idx, r.key)
	atomic.AddInt64(&bytesOnDisk, -int64(r.sizeBytes))
	_ = os.Remove(pathFor(r.key))
}

// Clear wipes everything — RAM index + disk files + counters.
func Clear() {
	mu.Lock()
	order.Init()
	idx = map[string]*list.Element{}
	atomic.StoreInt64(&bytesOnDisk, 0)
	// Nuke the whole directory and recreate — simpler than iterating the
	// LRU list and matching filenames, and drops any orphaned files too.
	_ = os.RemoveAll(dir)
	_ = os.MkdirAll(dir, 0o700)
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
	backing := dir
	maxE := maxItems
	ttlMs := ttl.Milliseconds()
	mu.Unlock()
	h := atomic.LoadUint64(&hits)
	m := atomic.LoadUint64(&misses)
	rate := "0.0"
	if total := h + m; total > 0 {
		rate = fmtFloat(float64(h) / float64(total) * 100)
	}
	return Stats{
		Size: size, MaxSize: maxE, TTLMs: ttlMs,
		Hits: h, Misses: m,
		Stores:      atomic.LoadUint64(&stores),
		Evictions:   atomic.LoadUint64(&evictions),
		HitRatePct:  rate,
		Backing:     backing,
		BytesOnDisk: atomic.LoadInt64(&bytesOnDisk),
	}
}

// pathFor returns the file path for a cache key. The key is a 64-char hex
// SHA-256, safe to drop directly into the filename with no escaping.
func pathFor(k string) string {
	return filepath.Join(dir, k+".json")
}

// reloadFromDisk rehydrates the in-RAM index from whatever files are in
// the backing dir. Entries whose files are unreadable or already expired
// (by mtime + ttl) are purged. Caller holds mu.
func reloadFromDisk() {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	now := time.Now()
	for _, e := range ents {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !hasSuffix(name, ".json") {
			continue
		}
		// Skip .tmp / crash residue.
		if hasSuffix(name, ".tmp.json") || hasSuffix(name, ".tmp") {
			continue
		}
		key := name[:len(name)-len(".json")]
		if len(key) != 64 {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		expires := info.ModTime().Add(ttl)
		if now.After(expires) {
			_ = os.Remove(filepath.Join(dir, name))
			continue
		}
		if _, ok := idx[key]; ok {
			continue
		}
		r := &record{key: key, expiresAt: expires, sizeBytes: int(info.Size())}
		el := order.PushBack(r)
		idx[key] = el
		atomic.AddInt64(&bytesOnDisk, info.Size())
		if order.Len() >= maxItems {
			// Don't overshoot on reload — anything beyond cap gets dropped.
			break
		}
	}
	// If the dir somehow holds a phantom ".tmp" from a mid-write crash,
	// clean that up too so it doesn't linger.
	for _, e := range ents {
		if e.IsDir() {
			continue
		}
		if hasSuffix(e.Name(), ".tmp") {
			_ = os.Remove(filepath.Join(dir, e.Name()))
		}
	}
	_ = errors.New // keep errors import stable if future probes need it
}

func hasSuffix(s, suf string) bool {
	return len(s) >= len(suf) && s[len(s)-len(suf):] == suf
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
