// Higher-level account operations that depend on the cloud + firebase
// packages. Split from pool.go so the core pool stays free of import cycles.
package auth

import (
	"context"
	"regexp"
	"strings"
	"sync"
	"time"

	"windsurfapi/internal/cloud"
	"windsurfapi/internal/firebase"
	"windsurfapi/internal/langserver"
	"windsurfapi/internal/logx"
	"windsurfapi/internal/models"
)

var proTierRE = regexp.MustCompile(`(?i)pro|teams|enterprise|trial|individual|premium|paid`)
var freeTierRE = regexp.MustCompile(`(?i)free`)

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

// RefreshAllCredits walks every active account sequentially (mirrors JS).
func (p *Pool) RefreshAllCredits(resolve proxyResolver) []CreditRefresh {
	p.mu.RLock()
	var ids []string
	for _, a := range p.accounts {
		if a.Status == StatusActive {
			ids = append(ids, a.ID)
		}
	}
	p.mu.RUnlock()
	out := make([]CreditRefresh, 0, len(ids))
	for _, id := range ids {
		out = append(out, p.RefreshCredits(id, resolve))
	}
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
		if strings.Contains(strings.ToLower(err.Error()), "rate limit") {
			logx.Info("  %s: RATE_LIMITED (skipped)", m)
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

	wg.Add(1)
	go func() {
		defer wg.Done()
		// Run an immediate refresh in the background, but track it in the WaitGroup
		// so Periodic's cancel func waits for it to finish before returning.
		wg.Add(1)
		go func() { defer wg.Done(); p.RefreshAllCredits(resolve) }()
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

	wg.Add(1)
	go func() {
		defer wg.Done()
		wg.Add(1)
		go func() { defer wg.Done(); p.RefreshFirebase(resolve) }()
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
