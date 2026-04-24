// Higher-level account operations that depend on the cloud + firebase
// packages. Split from pool.go so the core pool stays free of import cycles.
package auth

import (
	"context"
	"regexp"
	"sync"
	"time"

	"windsurfapi/internal/cloud"
	"windsurfapi/internal/firebase"
	"windsurfapi/internal/langserver"
	"windsurfapi/internal/logx"
	"windsurfapi/internal/models"
)

// Tier detection regexes — anchored at word boundaries so "production" /
// "prolific" / "unpaid" don't accidentally match and promote a free account
// to Pro. The literal set is kept narrow on purpose; new upstream plan
// names should be added explicitly rather than widened here.
var proTierRE = regexp.MustCompile(`(?i)\b(pro|teams|enterprise|trial|individual|premium|paid)\b`)
var freeTierRE = regexp.MustCompile(`(?i)\bfree\b`)

// IsRateLimitError detects "upstream told us this account is over quota"
// in every phrasing Cascade / Codeium has returned in production. Shared
// by the canary probe and the chat classifier so both paths agree on
// what counts as a rate limit (and therefore get logged into banhistory).
// Keep in sync — missing a phrasing here means Probe / classify silently
// skip past a real quota event.
var rateLimitRE = regexp.MustCompile(
	`(?i)rate[ _-]?limit` +
		`|too many requests` +
		`|\bquota\b` +
		`|daily (limit|cap)` +
		`|(message|usage|request)s? (limit|cap) (reached|exceeded|hit)` +
		`|(limit|quota) exceeded` +
		`|exceeded (your )?(daily|weekly|monthly) (message|usage|request)` +
		`|retry[ _-]?after`)

// IsRateLimitError reports whether err looks like any flavour of
// upstream rate-limit / quota signal.
func IsRateLimitError(err error) bool {
	if err == nil {
		return false
	}
	return rateLimitRE.MatchString(err.Error())
}

// IsRateLimitMessage is IsRateLimitError for a bare message string —
// chat.go's classify already has the message extracted and avoids the
// allocation of wrapping it back in an error.
func IsRateLimitMessage(msg string) bool {
	return msg != "" && rateLimitRE.MatchString(msg)
}

// AddByToken exchanges an auth token with Codeium and inserts the resulting
// account. Returns an existing account if the api key is already in the pool.
func (p *Pool) AddByToken(ctx context.Context, token, label string, proxy *langserver.Proxy) (*Account, error) {
	reg, err := cloud.RegisterUser(token, proxy)
	if err != nil {
		return nil, err
	}
	p.mu.Lock()
	for _, a := range p.accounts {
		if a.APIKey == reg.APIKey {
			p.mu.Unlock()
			return a, nil
		}
	}
	a := &Account{
		ID:           newID(),
		Email:        firstNonEmpty(label, reg.Name, "token-"+safePrefix(reg.APIKey, 8)),
		APIKey:       reg.APIKey,
		APIServerURL: reg.APIServerURL,
		Method:       "token",
		Status:       StatusActive,
		AddedAt:      time.Now().UnixMilli(),
		Tier:         "unknown",
	}
	p.accounts = append(p.accounts, a)
	p.saveLocked()
	p.mu.Unlock()
	logx.Info("Account added: %s (%s) [token] server=%s", a.ID, a.Email, a.APIServerURL)
	return a, nil
}

// AddByEmail does a full Firebase sign-in and ties the resulting refresh/id
// tokens onto the account so the 50-min refresh loop can keep them alive.
func (p *Pool) AddByEmail(ctx context.Context, email, password string, proxy *langserver.Proxy) (*Account, *firebase.LoginResult, error) {
	res, err := firebase.Login(email, password, proxy)
	if err != nil {
		return nil, nil, err
	}
	p.mu.Lock()
	for _, a := range p.accounts {
		if a.APIKey == res.APIKey {
			a.RefreshToken = res.RefreshToken
			a.IDToken = res.IDToken
			a.Method = "email"
			p.saveLocked()
			p.mu.Unlock()
			return a, &res, nil
		}
	}
	a := &Account{
		ID:           newID(),
		Email:        firstNonEmpty(res.Name, email),
		APIKey:       res.APIKey,
		APIServerURL: res.APIServerURL,
		Method:       "email",
		Status:       StatusActive,
		AddedAt:      time.Now().UnixMilli(),
		Tier:         "unknown",
		RefreshToken: res.RefreshToken,
		IDToken:      res.IDToken,
	}
	p.accounts = append(p.accounts, a)
	p.saveLocked()
	p.mu.Unlock()
	logx.Info("Account added: %s (%s) [email]", a.ID, a.Email)
	return a, &res, nil
}

// SetTokens persists a fresh apiKey / refresh / id token triple for an
// existing account — used after a successful Firebase refresh.
func (p *Pool) SetTokens(id string, apiKey, refresh, idTok string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, a := range p.accounts {
		if a.ID == id {
			if apiKey != "" {
				a.APIKey = apiKey
			}
			if refresh != "" {
				a.RefreshToken = refresh
			}
			if idTok != "" {
				a.IDToken = idTok
			}
			p.saveLocked()
			return true
		}
	}
	return false
}

// ─── Credit refresh ────────────────────────────────────────

// CreditRefresh represents the outcome of one account's refresh attempt.
type CreditRefresh struct {
	ID    string `json:"id"`
	Email string `json:"email"`
	OK    bool   `json:"ok"`
	Error string `json:"error,omitempty"`
}

// proxyResolver lets callers plug in dashboard/proxycfg without this package
// importing it (avoids a cycle).
type proxyResolver func(accountID string) *langserver.Proxy

// RefreshCredits hits GetUserStatus for one account and updates the tier
// hint + credit payload. Called by the 15-min loop and the dashboard.
func (p *Pool) RefreshCredits(id string, resolve proxyResolver) CreditRefresh {
	p.mu.RLock()
	var a *Account
	for _, x := range p.accounts {
		if x.ID == id {
			a = x
			break
		}
	}
	p.mu.RUnlock()
	if a == nil {
		return CreditRefresh{ID: id, OK: false, Error: "Account not found"}
	}

	px := (*langserver.Proxy)(nil)
	if resolve != nil {
		px = resolve(id)
	}
	st, err := cloud.GetUserStatus(a.APIKey, px)
	if err != nil {
		msg := err.Error()
		logx.Warn("refreshCredits %s failed: %s", id, msg)
		p.mu.Lock()
		if a.Credits == nil {
			a.Credits = &Credits{FetchedAt: time.Now().UnixMilli()}
		}
		a.Credits.LastError = msg
		p.saveLocked()
		p.mu.Unlock()
		return CreditRefresh{ID: id, Email: a.Email, OK: false, Error: msg}
	}

	p.mu.Lock()
	a.Credits = &Credits{
		PlanName:      st.PlanName,
		DailyPercent:  fOr(st.DailyPercent, 0),
		WeeklyPercent: fOr(st.WeeklyPercent, 0),
		DailyResetAt:  st.DailyResetAt,
		WeeklyResetAt: st.WeeklyResetAt,
		FetchedAt:     st.FetchedAt,
	}
	if !a.TierManual {
		switch {
		case proTierRE.MatchString(st.PlanName):
			a.Tier = "pro"
		case freeTierRE.MatchString(st.PlanName) && a.Tier == "unknown":
			a.Tier = "free"
		}
	}
	p.saveLocked()
	p.mu.Unlock()
	return CreditRefresh{ID: id, Email: a.Email, OK: true}
}

// refreshAllInFlight singleflights concurrent RefreshAllCredits — dashboard
// button-spammers or overlapping periodic ticks share a single in-flight
// worker set instead of each spawning their own 4-worker pool. Without this,
// 5 clicks with 30 stuck accounts = 20 goroutines + 5 blocked handlers,
// each holding its own upstream connection.
var (
	refreshAllMu        sync.Mutex
	refreshAllCached    []CreditRefresh
	refreshAllCachedAt  time.Time
)

// RefreshAllCredits walks every active account. Parallelised with a small
// concurrency limit so one slow / unreachable GetUserStatus (e.g. upstream
// network glitch, rate-limit on the seat-management endpoint) can't stall
// the whole 15-min refresh loop.
//
// Reentrancy guard: calls that arrive while another RefreshAllCredits is
// already running return the latest cached result (staleness ≤ 5 s) rather
// than queueing another refresh storm.
func (p *Pool) RefreshAllCredits(resolve proxyResolver) []CreditRefresh {
	// If a refresh finished within the last 5 s AND we're racing a dashboard
	// spam-click, return the cached view instead of starting a fresh one.
	// The 15-min periodic loop is sequential so this branch effectively only
	// affects the dashboard, which is what we want.
	refreshAllMu.Lock()
	if time.Since(refreshAllCachedAt) < 5*time.Second && refreshAllCached != nil {
		cached := refreshAllCached
		refreshAllMu.Unlock()
		return cached
	}
	refreshAllMu.Unlock()
	p.mu.RLock()
	var ids []string
	for _, a := range p.accounts {
		if a.Status == StatusActive {
			ids = append(ids, a.ID)
		}
	}
	p.mu.RUnlock()
	out := make([]CreditRefresh, len(ids))
	const workers = 4
	sem := make(chan struct{}, workers)
	var wg sync.WaitGroup
	for i, id := range ids {
		// Acquire the slot INSIDE the goroutine so the caller never
		// blocks on a sem push — otherwise N>workers jobs with dead
		// upstreams would hang the caller for minutes before the first
		// worker slot frees.
		wg.Add(1)
		go func(i int, id string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			out[i] = p.RefreshCredits(id, resolve)
		}(i, id)
	}
	wg.Wait()
	refreshAllMu.Lock()
	refreshAllCached = out
	refreshAllCachedAt = time.Now()
	refreshAllMu.Unlock()
	return out
}

// ─── Firebase refresh loop ─────────────────────────────────

// RefreshFirebase refreshes tokens for every account that has a stored
// refresh token. Called by the 50-min loop.
func (p *Pool) RefreshFirebase(resolve proxyResolver) {
	p.mu.RLock()
	type job struct {
		ID, Email, Refresh, APIKey string
	}
	var jobs []job
	for _, a := range p.accounts {
		if a.Status != StatusActive || a.RefreshToken == "" {
			continue
		}
		jobs = append(jobs, job{a.ID, a.Email, a.RefreshToken, a.APIKey})
	}
	p.mu.RUnlock()

	for _, j := range jobs {
		px := (*langserver.Proxy)(nil)
		if resolve != nil {
			px = resolve(j.ID)
		}
		tokens, err := firebase.RefreshToken(j.Refresh, px)
		if err != nil {
			logx.Warn("Firebase refresh %s failed: %s", j.Email, err.Error())
			continue
		}
		reg, err := firebase.ReRegister(tokens.IDToken, px)
		if err != nil {
			logx.Warn("Firebase re-register %s failed: %s", j.Email, err.Error())
			continue
		}
		p.SetTokens(j.ID, reg.APIKey, tokens.RefreshToken, tokens.IDToken)
		if reg.APIKey != j.APIKey {
			logx.Info("Firebase refresh: %s got new API key", j.Email)
		}
	}
}

// ─── Capability probe ──────────────────────────────────────

var canaries = []string{"gpt-4o-mini", "gemini-2.5-flash", "claude-sonnet-4.6", "claude-opus-4.6"}

// ProbeResult is what Probe returns for the dashboard.
type ProbeResult struct {
	Tier         string                `json:"tier"`
	Capabilities map[string]Capability `json:"capabilities"`
}

// Probe is called after account add / on schedule. It tests each canary
// model with a tiny "hi" turn and stamps capability results. Supplying a
// probeFunc keeps this package free of a direct dependency on client+langserver.
type ProbeFunc func(ctx context.Context, apiKey, modelKey string) error

func (p *Pool) Probe(ctx context.Context, id string, probe ProbeFunc) *ProbeResult {
	p.mu.RLock()
	var apiKey, email string
	var known bool
	for _, a := range p.accounts {
		if a.ID == id {
			apiKey = a.APIKey
			email = a.Email
			known = true
			break
		}
	}
	p.mu.RUnlock()
	if !known {
		return nil
	}
	logx.Info("Probing account %s (%s) across %d models", id, email, len(canaries))
	for _, m := range canaries {
		err := probe(ctx, apiKey, m)
		if err == nil {
			p.UpdateCapability(apiKey, m, true, "success")
			logx.Info("  %s: OK", m)
			continue
		}
		if IsRateLimitError(err) {
			// Treat probe-observed rate limits the same as live-request
			// ones: quarantine the (account, model) pair for 5 min and
			// append to banhistory so the dashboard "异常监测" panel
			// actually reflects what Probe saw. Previously this branch
			// only printed an Info log, so rate-limit events detected by
			// the 6h probe cycle never made it into history — operators
			// would see models drop off the live Bans view with no trace
			// of why.
			p.MarkRateLimited(apiKey, 5*time.Minute, m)
			logx.Warn("  %s: RATE_LIMITED (probe) — quarantined 5m", m)
			continue
		}
		p.UpdateCapability(apiKey, m, false, "model_error")
		logx.Info("  %s: FAIL (%s)", m, truncate(err.Error(), 80))
	}
	p.mu.Lock()
	for _, a := range p.accounts {
		if a.ID == id {
			a.LastProbed = time.Now().UnixMilli()
			break
		}
	}
	p.saveLocked()
	p.mu.Unlock()
	// capture current tier/caps for the return
	p.mu.RLock()
	defer p.mu.RUnlock()
	for _, a := range p.accounts {
		if a.ID == id {
			caps := map[string]Capability{}
			for k, v := range a.Capabilities {
				caps[k] = v
			}
			return &ProbeResult{Tier: a.Tier, Capabilities: caps}
		}
	}
	return nil
}

// ─── Periodic tasks ────────────────────────────────────────

// Periodic starts the 6h probe + 15min credit refresh + 50min Firebase
// refresh background goroutines. Returns a cancel func.
func (p *Pool) Periodic(ctx context.Context, resolve proxyResolver, probe ProbeFunc) func() {
	ctx, cancel := context.WithCancel(ctx)

	var wg sync.WaitGroup

	wg.Add(1)
	go func() {
		defer wg.Done()
		t := time.NewTicker(6 * time.Hour)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				for _, a := range p.All() {
					if a.Status != StatusActive || probe == nil {
						continue
					}
					_ = p.Probe(ctx, a.ID, probe)
				}
			}
		}
	}()

	// Credits refresh — immediate kick + 15-minute tick. The earlier layout
	// called `wg.Add(1)` from *inside* an already-running goroutine, which
	// sync.WaitGroup explicitly forbids ("Add calls with a positive delta
	// that occur when the counter is zero must happen before a Wait"). With
	// Go's race detector enabled it reports a data race; without it, the
	// undefined behaviour surfaces as `cancel()` returning while the "kick"
	// goroutine is still running (and potentially writing to accounts.json
	// after main exited). Fix: hoist both Add(1) calls above the spawn so
	// the counter is non-zero before any Wait could observe it.
	wg.Add(2)
	go func() { defer wg.Done(); p.RefreshAllCredits(resolve) }()
	go func() {
		defer wg.Done()
		t := time.NewTicker(15 * time.Minute)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				p.RefreshAllCredits(resolve)
			}
		}
	}()

	// Firebase refresh — same hoisted-Add pattern.
	wg.Add(2)
	go func() { defer wg.Done(); p.RefreshFirebase(resolve) }()
	go func() {
		defer wg.Done()
		t := time.NewTicker(50 * time.Minute)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				p.RefreshFirebase(resolve)
			}
		}
	}()

	return func() { cancel(); wg.Wait() }
}

// FetchModelCatalog is invoked once at startup so NEW cloud models merge
// into the hand-curated seed.
func (p *Pool) FetchModelCatalog(resolve proxyResolver) {
	p.mu.RLock()
	var apiKey, id string
	for _, a := range p.accounts {
		if a.Status == StatusActive && a.APIKey != "" {
			apiKey = a.APIKey
			id = a.ID
			break
		}
	}
	p.mu.RUnlock()
	if apiKey == "" {
		logx.Debug("No active account for model catalog fetch")
		return
	}
	var px *langserver.Proxy
	if resolve != nil {
		px = resolve(id)
	}
	entries, err := cloud.GetCascadeModelConfigs(apiKey, px)
	if err != nil {
		logx.Warn("Model catalog fetch: %s", err.Error())
		return
	}
	added := models.MergeCloud(entries)
	logx.Info("Model catalog: %d cloud models, %d new entries merged", len(entries), added)
}

// ─── helpers ──────────────────────────────────────────────

func firstNonEmpty(ss ...string) string {
	for _, s := range ss {
		if s != "" {
			return s
		}
	}
	return ""
}
func safePrefix(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
func fOr(p *float64, d float64) float64 {
	if p == nil {
		return d
	}
	return *p
}
