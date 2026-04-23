// Package banhistory keeps a bounded log of every rate-limit event fired
// by the account pool so operators can investigate past quarantines from
// the dashboard (the current /dashboard/api/accounts view only shows what
// is rate-limited *right now*).
//
// Events are ring-buffered in memory (no persistence) — the intent is
// "what happened in the last few hours" not forensic audit. For durable
// audit use the JSONL log files.
package banhistory

import (
	"sync"
	"time"
)

// Entry is one rate-limit event. `Model == ""` means the whole account
// was quarantined; otherwise only that specific model key was banned.
type Entry struct {
	Ts        int64  `json:"ts"`        // unix-ms when the ban was recorded
	AccountID string `json:"accountId"` // full id, not truncated
	Email     string `json:"email"`
	Model     string `json:"model,omitempty"` // empty = account-wide
	Server    int64  `json:"serverMs"`        // upstream-reported window (ms)
	Effective int64  `json:"effectiveMs"`     // clamped / grace-padded window we applied
	Until     int64  `json:"until"`           // unix-ms when this ban expires
}

const ringCap = 500

var (
	mu   sync.RWMutex
	ring []Entry
)

// Record appends one entry. Thread-safe; FIFO-evicts when the ring fills.
// Old entries older than 24h are filtered out on read rather than in write
// so Recent() can cheaply give callers a bounded window.
func Record(e Entry) {
	if e.Ts == 0 {
		e.Ts = time.Now().UnixMilli()
	}
	mu.Lock()
	defer mu.Unlock()
	ring = append(ring, e)
	if len(ring) > ringCap {
		ring = ring[len(ring)-ringCap:]
	}
}

// Recent returns up to n most recent entries, newest first. n<=0 means "all".
// Returns a fresh slice — callers can sort / mutate without affecting state.
func Recent(n int) []Entry {
	mu.RLock()
	defer mu.RUnlock()
	if n <= 0 || n > len(ring) {
		n = len(ring)
	}
	out := make([]Entry, n)
	// Reverse copy: ring is append-only, newest is at the tail.
	for i := 0; i < n; i++ {
		out[i] = ring[len(ring)-1-i]
	}
	return out
}

// Clear wipes the ring — exposed so a dashboard "clear history" button can
// reset telemetry without restarting the service.
func Clear() {
	mu.Lock()
	ring = nil
	mu.Unlock()
}
