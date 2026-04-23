// Package auth manages the account pool — the core of the service. Behaviour
// is a direct port of src/auth.js: tier-weighted RPM selection, per-model
// rate-limit quarantine, credit tracking, account blocklists, auto-disable on
// repeated auth failures.
package auth

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"windsurfapi/internal/atomicfile"
	"windsurfapi/internal/banhistory"
	"windsurfapi/internal/logx"
	"windsurfapi/internal/models"
)

const (
	StatusActive   = "active"
	StatusError    = "error"
	StatusDisabled = "disabled"
)

// Per-tier RPM caps — matches TIER_RPM in auth.js.
var tierRPM = map[string]int{
	"pro":     60,
	"free":    10,
	"unknown": 20,
	"expired": 0,
}

const rpmWindow = time.Minute

// Capability captures the result of the last canary probe.
type Capability struct {
	OK        bool   `json:"ok"`
	LastCheck int64  `json:"lastCheck"`
	Reason    string `json:"reason"`
}

// Credits is the subset of GetUserStatus we persist onto the account.
type Credits struct {
	PlanName      string  `json:"planName,omitempty"`
	DailyPercent  float64 `json:"dailyPercent,omitempty"`
	WeeklyPercent float64 `json:"weeklyPercent,omitempty"`
	DailyResetAt  int64   `json:"dailyResetAt,omitempty"`
	WeeklyResetAt int64   `json:"weeklyResetAt,omitempty"`
	FetchedAt     int64   `json:"fetchedAt,omitempty"`
	LastError     string  `json:"lastError,omitempty"`
}

// Account is one row in the pool.
type Account struct {
	ID           string                `json:"id"`
	Email        string                `json:"email"`
	APIKey       string                `json:"apiKey"`
	APIServerURL string                `json:"apiServerUrl,omitempty"`
	Method       string                `json:"method"`
	Status       string                `json:"status"`
	AddedAt      int64                 `json:"addedAt"`
	Tier         string                `json:"tier"`
	TierManual   bool                  `json:"tierManual,omitempty"`
	Capabilities map[string]Capability `json:"capabilities,omitempty"`
	LastProbed   int64                 `json:"lastProbed,omitempty"`
	Credits      *Credits              `json:"credits,omitempty"`
	BlockedModels []string             `json:"blockedModels,omitempty"`
	RefreshToken string                `json:"refreshToken,omitempty"`
	IDToken      string                `json:"-"`

	// Persisted rate-limit state. Kept on the exported surface so a restart
	// doesn't accidentally reopen a 5-min penalty window that was already
	// running — operators were getting surprised when stopping the service
	// right before a rate-limit expired led to the account getting slapped
	// again seconds after reboot. The refresh time (startedAt) is derivable
	// as `until - (serverWindow + grace)`; we record it explicitly so the
	// UI can show "started at X, ends at Y".
	RateLimitedUntil   int64            `json:"rateLimitedUntil,omitempty"`
	RateLimitedStarted int64            `json:"rateLimitedStarted,omitempty"`
	ModelRateLimits    map[string]int64 `json:"modelRateLimits,omitempty"`
	ModelRateStarted   map[string]int64 `json:"modelRateStarted,omitempty"`

	// Runtime state — never persisted.
	lastUsed             int64
	errorCount           int
	internalErrorStreak  int
	rpmHistory           []int64
}

// Pool is the process-wide account registry.
type Pool struct {
	mu       sync.RWMutex
	accounts []*Account
	file     string
	// lastSaveErr holds the most recent persist error (or wrapped nil).
	// Updated atomically by saveLocked so the /health + dashboard overview
	// can surface "save is broken right now" without needing to refactor
	// every mutating API to return error — see R3-#13 rationale in the
	// saveLocked doc comment.
	lastSaveErr atomic.Value
}

// New returns an empty pool backed by file (typically "accounts.json").
func New(file string) *Pool { return &Pool{file: file} }

// ─── Persistence ──────────────────────────────────────────

// Load reads the backing file.
func (p *Pool) Load() error {
	data, err := os.ReadFile(p.file)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var raw []Account
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	now := time.Now().UnixMilli()
	for i := range raw {
		a := raw[i]
		if a.ID == "" {
			a.ID = newID()
		}
		if a.Status == "" {
			a.Status = StatusActive
		}
		if a.Tier == "" {
			a.Tier = "unknown"
		}
		if a.Method == "" {
			a.Method = "api_key"
		}
		// Prune expired rate-limit entries at load so the pool doesn't
		// carry zombie windows after a long downtime. Active entries
		// keep their original start + deadline so "started at X, ends
		// at Y" remains accurate across restarts.
		if a.RateLimitedUntil > 0 && a.RateLimitedUntil <= now {
			a.RateLimitedUntil = 0
			a.RateLimitedStarted = 0
		}
		if len(a.ModelRateLimits) > 0 {
			for k, until := range a.ModelRateLimits {
				if until <= now {
					delete(a.ModelRateLimits, k)
					if a.ModelRateStarted != nil {
						delete(a.ModelRateStarted, k)
					}
				}
			}
			if len(a.ModelRateLimits) == 0 {
				a.ModelRateLimits = nil
			}
		}
		// Dedup by apiKey.
		dup := false
		for _, e := range p.accounts {
			if e.APIKey == a.APIKey {
				dup = true
				break
			}
		}
		if !dup {
			p.accounts = append(p.accounts, &a)
		}
	}
	if n := len(p.accounts); n > 0 {
		logx.Info("Loaded %d account(s) from disk", n)
	}
	return nil
}

// save serialises under lock-held caller. Errors are logged, not returned.
//
// The previous implementation used a shared `accounts.json.tmp` path. Two
// goroutines calling saveLocked concurrently (e.g. the 50-min Firebase
// loop, the 15-min credits loop, and a dashboard POST all firing within
// the same millisecond) could interleave bytes into the shared .tmp and
// the rename would land a corrupt JSON — at which point the next restart
// would Load() an empty pool and the operator would lose every account.
// atomicfile.Write generates a unique per-call tmp name to eliminate that
// race, and pins 0o600 so passwords / tokens never leak to group-readable
// permissions on shared hosts.
func (p *Pool) saveLocked() error {
	data, err := json.MarshalIndent(p.accounts, "", "  ")
	if err != nil {
		logx.Error("auth save: %s", err.Error())
		p.lastSaveErr.Store(errWrap{err})
		return err
	}
	if err := atomicfile.Write(p.file, data); err != nil {
		logx.Error("auth save: %s", err.Error())
		p.lastSaveErr.Store(errWrap{err})
		return err
	}
	// Clear the sticky error on success so operators see "recovered" after
	// a transient disk-full.
	p.lastSaveErr.Store(errWrap{nil})
	return nil
}

// errWrap lets atomic.Value hold a typed nil-or-error without violating
// atomic.Value's "consistent concrete type" rule.
type errWrap struct{ err error }

// LastSaveError returns the most recent persist failure (or nil). Exposed
// for the /health + dashboard overview so an operator can see "yes your
// AddByKey looked fine but disk is full and state won't survive restart"
// without trawling the log panel. Zero value is "never saved" = nil.
func (p *Pool) LastSaveError() error {
	v := p.lastSaveErr.Load()
	if v == nil {
		return nil
	}
	return v.(errWrap).err
}

// ─── Add / remove ─────────────────────────────────────────

// AddByKey inserts or returns an existing account matching apiKey.
func (p *Pool) AddByKey(apiKey, label string) *Account {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, a := range p.accounts {
		if a.APIKey == apiKey {
			return a
		}
	}
	a := &Account{
		ID:      newID(),
		Email:   labelOrKey(label, apiKey),
		APIKey:  apiKey,
		Method:  "api_key",
		Status:  StatusActive,
		AddedAt: time.Now().UnixMilli(),
		Tier:    "unknown",
	}
	p.accounts = append(p.accounts, a)
	p.saveLocked()
	logx.Info("Account added: %s (%s) [api_key]", a.ID, a.Email)
	return a
}

// Remove drops an account by id.
func (p *Pool) Remove(id string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	for i, a := range p.accounts {
		if a.ID == id {
			p.accounts = append(p.accounts[:i], p.accounts[i+1:]...)
			p.saveLocked()
			logx.Info("Account removed: %s (%s)", a.ID, a.Email)
			return true
		}
	}
	return false
}

// ─── Lookup / mutate ──────────────────────────────────────

// Get returns a deep snapshot of an account by id, or nil. See cloneAccount
// for why the copy must be deep.
func (p *Pool) Get(id string) *Account {
	p.mu.RLock()
	defer p.mu.RUnlock()
	for _, a := range p.accounts {
		if a.ID == id {
			return cloneAccount(a)
		}
	}
	return nil
}

// All returns a read-only snapshot of every account.
//
// IMPORTANT: this deep-copies the map and slice fields (`Capabilities`,
// `BlockedModels`, `ModelRateLimits`, `ModelRateStarted`). A shallow `cp := *a`
// would leave those references aliased with the live pool, so any caller that
// later iterated e.g. `cp.Capabilities` without holding `p.mu` would race
// with a writer mutating the same map and hit Go's `fatal error: concurrent
// map read and map write`, which crashes the whole process — not a
// recoverable panic. The dashboard's `/dashboard/api/accounts` enumeration
// triggers this path on every poll, so the race window is real.
func (p *Pool) All() []*Account {
	p.mu.RLock()
	defer p.mu.RUnlock()
	out := make([]*Account, 0, len(p.accounts))
	for _, a := range p.accounts {
		out = append(out, cloneAccount(a))
	}
	return out
}

// cloneAccount returns an owned deep copy safe to share across goroutines.
// Nil inputs return nil to keep callers' `if a := Get(id); a != nil` style
// working unchanged.
func cloneAccount(a *Account) *Account {
	if a == nil {
		return nil
	}
	cp := *a
	if a.Capabilities != nil {
		m := make(map[string]Capability, len(a.Capabilities))
		for k, v := range a.Capabilities {
			m[k] = v
		}
		cp.Capabilities = m
	}
	if a.BlockedModels != nil {
		cp.BlockedModels = append([]string(nil), a.BlockedModels...)
	}
	if a.ModelRateLimits != nil {
		m := make(map[string]int64, len(a.ModelRateLimits))
		for k, v := range a.ModelRateLimits {
			m[k] = v
		}
		cp.ModelRateLimits = m
	}
	if a.ModelRateStarted != nil {
		m := make(map[string]int64, len(a.ModelRateStarted))
		for k, v := range a.ModelRateStarted {
			m[k] = v
		}
		cp.ModelRateStarted = m
	}
	if a.Credits != nil {
		c := *a.Credits
		cp.Credits = &c
	}
	if a.rpmHistory != nil {
		cp.rpmHistory = append([]int64(nil), a.rpmHistory...)
	}
	return &cp
}

// HasEligible reports whether any active, non-blocked, tier-allowed account
// exists for the given model. Designed for the chat hot path: it avoids the
// whole-pool deep-copy that `All()` does on every request (30+ accounts ×
// every request = ~1 MB churn / sec at 100 rps), and returns early on the
// first match. `tierAllowed` is a closure injected by the caller so this
// package doesn't need to import `models` (which would pull in scoring /
// pricing / catalog weight that auth never uses).
func (p *Pool) HasEligible(modelKey string, tierAllowed func(tier, key string) bool) bool {
	p.mu.RLock()
	defer p.mu.RUnlock()
	for _, a := range p.accounts {
		if a.Status != StatusActive {
			continue
		}
		if !tierAllowed(a.Tier, modelKey) {
			continue
		}
		blocked := false
		for _, b := range a.BlockedModels {
			if b == modelKey {
				blocked = true
				break
			}
		}
		if !blocked {
			return true
		}
	}
	return false
}

// Counts returns {total, active, error}.
type Counts struct {
	Total  int `json:"total"`
	Active int `json:"active"`
	Error  int `json:"error"`
}

func (p *Pool) Counts() Counts {
	p.mu.RLock()
	defer p.mu.RUnlock()
	c := Counts{Total: len(p.accounts)}
	for _, a := range p.accounts {
		if a.Status == StatusActive {
			c.Active++
		} else if a.Status == StatusError {
			c.Error++
		}
	}
	return c
}

// IsAuthenticated reports whether any active account exists.
func (p *Pool) IsAuthenticated() bool {
	p.mu.RLock()
	defer p.mu.RUnlock()
	for _, a := range p.accounts {
		if a.Status == StatusActive {
			return true
		}
	}
	return false
}

// validStatus restricts what callers (especially the dashboard PATCH route)
// can write. An unchecked status would let an operator — or an attacker with
// the dashboard password — freeze an account into an unrecognised state
// like "hacked", after which `a.Status == StatusActive` checks would
// silently skip it forever.
func validStatus(s string) bool {
	switch s {
	case StatusActive, StatusError, StatusDisabled, "expired", "invalid":
		return true
	}
	return false
}

// SetStatus updates the status on an account. Unknown status strings are
// rejected (returns false) so an accidental typo or crafted PATCH can't
// wedge an account into a permanent non-active limbo.
func (p *Pool) SetStatus(id, status string) bool {
	if !validStatus(status) {
		return false
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, a := range p.accounts {
		if a.ID == id {
			a.Status = status
			if status == StatusActive {
				a.errorCount = 0
			}
			p.saveLocked()
			logx.Info("Account %s status set to %s", id, status)
			return true
		}
	}
	return false
}

// SetTier manually overrides an account's tier — matches src/auth.js
// setAccountTier. Used to fix misclassified Pro trials.
func (p *Pool) SetTier(id, tier string) bool {
	valid := false
	for _, t := range []string{"pro", "free", "unknown", "expired"} {
		if t == tier {
			valid = true
			break
		}
	}
	if !valid {
		return false
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, a := range p.accounts {
		if a.ID == id {
			a.Tier = tier
			a.TierManual = true
			p.saveLocked()
			logx.Info("Account %s tier manually set to %s", id, tier)
			return true
		}
	}
	return false
}

// SetBlockedModels replaces an account's per-model blocklist.
func (p *Pool) SetBlockedModels(id string, list []string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, a := range p.accounts {
		if a.ID == id {
			cp := append([]string(nil), list...)
			a.BlockedModels = cp
			p.saveLocked()
			logx.Info("Account %s blockedModels updated: %d blocked", id, len(cp))
			return true
		}
	}
	return false
}

// ─── Selection ────────────────────────────────────────────

// Selected is the result of an Acquire call — a shallow view ready for use
// by the chat path. Proxy lookup happens separately in the dashboard layer.
type Selected struct {
	ID           string
	Email        string
	APIKey       string
	APIServerURL string
}

// Acquire picks the next account eligible for modelKey, skipping any keys in
// `tried`. Returns nil when every active account is momentarily unavailable.
func (p *Pool) Acquire(tried []string, modelKey string) *Selected {
	now := time.Now().UnixMilli()
	triedSet := map[string]struct{}{}
	for _, k := range tried {
		triedSet[k] = struct{}{}
	}

	p.mu.Lock()
	defer p.mu.Unlock()

	type cand struct {
		a     *Account
		used  int
		limit int
	}
	var list []cand
	for _, a := range p.accounts {
		if a.Status != StatusActive {
			continue
		}
		if _, skip := triedSet[a.APIKey]; skip {
			continue
		}
		// Write-lock is held here, so the RW variant can prune expired
		// per-model entries inline without racing readers.
		if isRateLimitedRW(a, modelKey, now) {
			continue
		}
		limit := tierRPM[a.Tier]
		if limit == 0 && a.Tier != "expired" {
			limit = 20
		}
		if limit <= 0 {
			continue
		}
		if modelKey != "" {
			if !isModelAllowed(a, modelKey) {
				continue
			}
		}
		used := pruneRPM(a, now)
		if used >= limit {
			continue
		}
		list = append(list, cand{a, used, limit})
	}
	if len(list) == 0 {
		return nil
	}
	sort.Slice(list, func(i, j int) bool {
		ri := float64(list[i].limit-list[i].used) / float64(list[i].limit)
		rj := float64(list[j].limit-list[j].used) / float64(list[j].limit)
		if ri != rj {
			return ri > rj
		}
		return list[i].a.lastUsed < list[j].a.lastUsed
	})
	a := list[0].a
	a.rpmHistory = append(a.rpmHistory, now)
	a.lastUsed = now
	return &Selected{ID: a.ID, Email: a.Email, APIKey: a.APIKey, APIServerURL: a.APIServerURL}
}

// AcquireByKey tries to re-check-out a specific account, for the cascade-reuse
// path that must pin to the account owning the cached cascade_id.
func (p *Pool) AcquireByKey(apiKey, modelKey string) *Selected {
	now := time.Now().UnixMilli()
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, a := range p.accounts {
		if a.APIKey != apiKey {
			continue
		}
		if a.Status != StatusActive {
			return nil
		}
		if isRateLimitedRW(a, modelKey, now) {
			return nil
		}
		limit := tierRPM[a.Tier]
		if limit == 0 && a.Tier != "expired" {
			limit = 20
		}
		if limit <= 0 {
			return nil
		}
		if modelKey != "" && !isModelAllowed(a, modelKey) {
			return nil
		}
		if pruneRPM(a, now) >= limit {
			return nil
		}
		a.rpmHistory = append(a.rpmHistory, now)
		a.lastUsed = now
		return &Selected{ID: a.ID, Email: a.Email, APIKey: a.APIKey, APIServerURL: a.APIServerURL}
	}
	return nil
}

// IsAllRateLimited — if *every* eligible account is currently rate-limited
// for modelKey, returns (true, retryAfterMs).
func (p *Pool) IsAllRateLimited(modelKey string) (bool, int64) {
	now := time.Now().UnixMilli()
	soonest := int64(-1)
	anyEligible := false
	p.mu.RLock()
	defer p.mu.RUnlock()
	for _, a := range p.accounts {
		if a.Status != StatusActive {
			continue
		}
		if modelKey != "" && !isModelAllowed(a, modelKey) {
			continue
		}
		anyEligible = true
		if !isRateLimited(a, modelKey, now) {
			return false, 0
		}
		if a.RateLimitedUntil > now {
			if soonest == -1 || a.RateLimitedUntil < soonest {
				soonest = a.RateLimitedUntil
			}
		}
		if modelKey != "" {
			if t, ok := a.ModelRateLimits[modelKey]; ok && t > now {
				if soonest == -1 || t < soonest {
					soonest = t
				}
			}
		}
	}
	if !anyEligible {
		return false, 0
	}
	if soonest == -1 {
		return true, 60_000
	}
	retry := soonest - now
	if retry < 1000 {
		retry = 1000
	}
	return true, retry
}

// ─── Per-account signalling ────────────────────────────────

// rateLimitGrace is the extra buffer we tack on after rounding up the
// server-reported window to a whole minute. Waking up exactly at the
// boundary the server promised tends to trip a fresh 5-min penalty because
// server and client clocks aren't perfectly aligned and the last few
// requests are already queued upstream.
const rateLimitGrace = time.Minute

// EffectiveRateLimitWindow converts the server-reported retry-after into
// what the pool actually waits. Rule: round up to the next whole minute,
// then add a 1-minute grace. Matches the operator mental model of "if they
// said 5m, we wait 6m; if they said 27m31s we wait at least until 28m is
// clear then one more minute on top". Exported for tests + logging.
func EffectiveRateLimitWindow(d time.Duration) time.Duration {
	if d <= 0 {
		return rateLimitGrace
	}
	// ceil(d / 1min) * 1min
	ms := d.Milliseconds()
	minMs := time.Minute.Milliseconds()
	ceilMin := (ms + minMs - 1) / minMs
	return time.Duration(ceilMin)*time.Minute + rateLimitGrace
}

// MarkRateLimited quarantines the given apiKey. When modelKey is "" the
// whole account is blocked; otherwise only that model. The start/deadline
// pair is flushed to disk so a service restart doesn't lose the penalty
// window (operators were seeing accounts bounce right back into selection
// after a deploy, only to immediately get slapped again because the server
// still remembered).
func (p *Pool) MarkRateLimited(apiKey string, d time.Duration, modelKey string) {
	start := time.Now().UnixMilli()
	effective := EffectiveRateLimitWindow(d)
	until := start + effective.Milliseconds()
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, a := range p.accounts {
		if a.APIKey != apiKey {
			continue
		}
		if modelKey != "" {
			if a.ModelRateLimits == nil {
				a.ModelRateLimits = map[string]int64{}
			}
			if a.ModelRateStarted == nil {
				a.ModelRateStarted = map[string]int64{}
			}
			a.ModelRateLimits[modelKey] = until
			a.ModelRateStarted[modelKey] = start
			logx.Warn("Account %s (%s) rate-limited on %s: server=%s → effective=%s",
				a.ID, a.Email, modelKey, d, effective)
		} else {
			a.RateLimitedUntil = until
			a.RateLimitedStarted = start
			logx.Warn("Account %s (%s) rate-limited (all models): server=%s → effective=%s",
				a.ID, a.Email, d, effective)
		}
		// Append to the bounded history ring so the dashboard's "历史记录"
		// panel can show what was banned when, long after the window has
		// expired and the row has dropped out of the live Bans view.
		// Record inside the pool lock so the snapshot's Email/AccountID
		// is consistent with what we just mutated — Record itself takes
		// its own RLock, different mutex, no deadlock.
		banhistory.Record(banhistory.Entry{
			Ts:        start,
			AccountID: a.ID,
			Email:     a.Email,
			Model:     modelKey,
			Server:    d.Milliseconds(),
			Effective: effective.Milliseconds(),
			Until:     until,
		})
		p.saveLocked()
		return
	}
}

// ReportError increments the auth-failure counter; ≥3 flips to error status.
func (p *Pool) ReportError(apiKey string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, a := range p.accounts {
		if a.APIKey != apiKey {
			continue
		}
		a.errorCount++
		if a.errorCount >= 3 {
			a.Status = StatusError
			logx.Warn("Account %s (%s) disabled after %d errors", a.ID, a.Email, a.errorCount)
		}
		return
	}
}

// ReportSuccess resets the auth-failure counter.
func (p *Pool) ReportSuccess(apiKey string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, a := range p.accounts {
		if a.APIKey != apiKey {
			continue
		}
		if a.errorCount > 0 {
			a.errorCount = 0
			a.Status = StatusActive
		}
		a.internalErrorStreak = 0
		return
	}
}

// ReportInternalError: 2 consecutive upstream internal errors → 5-min
// quarantine. Matches src/auth.js reportInternalError.
func (p *Pool) ReportInternalError(apiKey string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, a := range p.accounts {
		if a.APIKey != apiKey {
			continue
		}
		a.internalErrorStreak++
		if a.internalErrorStreak >= 2 {
			now := time.Now().UnixMilli()
			a.RateLimitedStarted = now
			a.RateLimitedUntil = now + (5 * time.Minute).Milliseconds()
			logx.Warn("Account %s (%s) quarantined 5min after %d internal errors", a.ID, a.Email, a.internalErrorStreak)
			p.saveLocked()
		}
		return
	}
}

// UpdateCapability records the outcome of a canary call against modelKey.
// reason ∈ {"success","model_error","rate_limit","transport_error"}.
func (p *Pool) UpdateCapability(apiKey, modelKey string, ok bool, reason string) {
	// Transient errors don't influence tier inference.
	if reason == "transport_error" {
		return
	}
	if !ok && reason == "rate_limit" {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, a := range p.accounts {
		if a.APIKey != apiKey {
			continue
		}
		if a.Capabilities == nil {
			a.Capabilities = map[string]Capability{}
		}
		a.Capabilities[modelKey] = Capability{OK: ok, LastCheck: time.Now().UnixMilli(), Reason: reason}
		if !a.TierManual {
			a.Tier = inferTier(a.Capabilities)
		}
		p.saveLocked()
		return
	}
}

// ─── Helpers ──────────────────────────────────────────────

func pruneRPM(a *Account, now int64) int {
	cutoff := now - int64(rpmWindow/time.Millisecond)
	keep := a.rpmHistory[:0]
	for _, t := range a.rpmHistory {
		if t >= cutoff {
			keep = append(keep, t)
		}
	}
	a.rpmHistory = keep
	return len(a.rpmHistory)
}

// RateLimitView is the public snapshot of one account's active rate-limit
// state. Zero/empty fields mean "not rate-limited at this layer". Start
// timestamps are exposed so the UI can render "since HH:mm" alongside the
// "ends at HH:mm" countdown.
type RateLimitView struct {
	AccountStarted int64            `json:"accountStarted"`
	AccountUntil   int64            `json:"accountUntil"`
	Models         map[string]int64 `json:"models"`
	ModelStarted   map[string]int64 `json:"modelStarted"`
}

// RateLimitView returns a non-mutable copy of the active rate-limits on an
// account. Expired entries are filtered out so the caller only sees what's
// currently keeping (account, model) pairs out of the selector. Takes the
// pool's read lock internally; safe to call concurrently.
func (p *Pool) RateLimitView(id string) RateLimitView {
	now := time.Now().UnixMilli()
	p.mu.RLock()
	defer p.mu.RUnlock()
	for _, a := range p.accounts {
		if a.ID != id {
			continue
		}
		return buildRLView(a, now)
	}
	return RateLimitView{
		Models:       map[string]int64{},
		ModelStarted: map[string]int64{},
	}
}

// RateLimitViews returns per-id views in a single lock acquisition. Used by
// the dashboard accountsView path, which previously called `RateLimitView`
// N times — with 30+ accounts that's 30 RLock/RUnlock round-trips in the
// hot dashboard poll and contends with every writer (probe, markRateLimited,
// credit refresh), producing 100-ms latency spikes on a busy pool. One RLock
// holds everything for the duration of the walk; view objects are plain
// copies so we don't alias the live maps.
func (p *Pool) RateLimitViews() map[string]RateLimitView {
	now := time.Now().UnixMilli()
	p.mu.RLock()
	defer p.mu.RUnlock()
	out := make(map[string]RateLimitView, len(p.accounts))
	for _, a := range p.accounts {
		out[a.ID] = buildRLView(a, now)
	}
	return out
}

// buildRLView materialises a RateLimitView from an *Account. Caller holds
// the pool lock. Expired windows are elided so the UI only sees what's
// currently keeping a row out of the selector.
func buildRLView(a *Account, now int64) RateLimitView {
	v := RateLimitView{
		Models:       map[string]int64{},
		ModelStarted: map[string]int64{},
	}
	if a.RateLimitedUntil > now {
		v.AccountUntil = a.RateLimitedUntil
		v.AccountStarted = a.RateLimitedStarted
	}
	for k, until := range a.ModelRateLimits {
		if until > now {
			v.Models[k] = until
			if a.ModelRateStarted != nil {
				v.ModelStarted[k] = a.ModelRateStarted[k]
			}
		}
	}
	return v
}

// isRateLimited reports whether account `a` is currently rate-limited for
// `modelKey`. **Expired windows are NOT deleted here** — the caller's lock
// discipline decides whether cleanup is allowed. Use isRateLimitedRW below
// for the write-lock-held path where it's safe to prune.
//
// Previously the single `isRateLimited` also deleted expired entries from
// `a.ModelRateLimits`, but `IsAllRateLimited` (line 542) calls it under
// RLock — mixing a map write with concurrent `MarkRateLimited` writes
// triggers Go's `fatal error: concurrent map read and map write`, which
// takes the whole process down. Separating the two paths removes the
// race without changing behaviour for callers that genuinely need cleanup.
func isRateLimited(a *Account, modelKey string, now int64) bool {
	if a.RateLimitedUntil > now {
		return true
	}
	if modelKey != "" {
		if t, ok := a.ModelRateLimits[modelKey]; ok && t > now {
			return true
		}
	}
	return false
}

// isRateLimitedRW is the "caller holds Pool.mu.Lock" variant — safe to
// prune expired entries.
//
// REQUIRES: caller holds p.mu as WRITER (not RLock). Calling this under a
// RLock — or without any lock — races with MarkRateLimited / ReportError /
// saveLocked writers on the same ModelRateLimits map and triggers Go's
// unrecoverable "concurrent map read and map write" fatal. The only
// legitimate callers today are Acquire / AcquireByKey, both of which
// `p.mu.Lock()` up front. Keep it that way.
//
// The split from isRateLimited (the RLock-safe read-only version) is the
// R2-#1 CRITICAL fix — don't merge these back without re-reading that
// history.
func isRateLimitedRW(a *Account, modelKey string, now int64) bool {
	if a.RateLimitedUntil > now {
		return true
	}
	if modelKey != "" {
		if t, ok := a.ModelRateLimits[modelKey]; ok {
			if t > now {
				return true
			}
			delete(a.ModelRateLimits, modelKey)
		}
	}
	return false
}

func isModelAllowed(a *Account, modelKey string) bool {
	if !models.IsTierAllowed(a.Tier, modelKey) {
		return false
	}
	for _, b := range a.BlockedModels {
		if b == modelKey {
			return false
		}
	}
	return true
}

func inferTier(caps map[string]Capability) string {
	works := func(m string) bool { return caps[m].OK }
	if works("claude-opus-4.6") || works("claude-sonnet-4.6") {
		return "pro"
	}
	if works("gemini-2.5-flash") || works("gpt-4o-mini") {
		return "free"
	}
	if len(caps) > 0 {
		allFail := true
		for _, v := range caps {
			if v.OK {
				allFail = false
				break
			}
		}
		if allFail {
			return "expired"
		}
	}
	return "unknown"
}

func newID() string {
	var b [4]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

func labelOrKey(label, apiKey string) string {
	if label != "" {
		return label
	}
	if len(apiKey) >= 8 {
		return "key-" + apiKey[:8]
	}
	return "key-" + apiKey
}
