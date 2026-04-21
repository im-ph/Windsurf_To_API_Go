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
	"regexp"
	"sort"
	"sync"
	"time"

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
func (p *Pool) saveLocked() {
	data, err := json.MarshalIndent(p.accounts, "", "  ")
	if err != nil {
		logx.Error("auth save: %s", err.Error())
		return
	}
	tmp := p.file + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		logx.Error("auth save write: %s", err.Error())
		return
	}
	if err := os.Rename(tmp, p.file); err != nil {
		logx.Error("auth save rename: %s", err.Error())
	}
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

// Get returns a snapshot of an account by id, or nil.
func (p *Pool) Get(id string) *Account {
	p.mu.RLock()
	defer p.mu.RUnlock()
	for _, a := range p.accounts {
		if a.ID == id {
			cp := *a
			return &cp
		}
	}
	return nil
}

// All returns a read-only view of every account.
func (p *Pool) All() []*Account {
	p.mu.RLock()
	defer p.mu.RUnlock()
	out := make([]*Account, 0, len(p.accounts))
	for _, a := range p.accounts {
		cp := *a
		out = append(out, &cp)
	}
	return out
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

// SetStatus updates the status on an account.
func (p *Pool) SetStatus(id, status string) bool {
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
		if isRateLimited(a, modelKey, now) {
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
		if isRateLimited(a, modelKey, now) {
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
	return RateLimitView{
		Models:       map[string]int64{},
		ModelStarted: map[string]int64{},
	}
}

func isRateLimited(a *Account, modelKey string, now int64) bool {
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

var _ = regexp.MustCompile // placeholder — regex-backed tier detection lives in cloud layer

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
