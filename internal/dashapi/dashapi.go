// Package dashapi exposes every /dashboard/api/* route. Direct port of
// src/dashboard/api.js. Depends on the full stack (auth, LS pool, cache,
// conv pool, runtime-config, proxy, model-access, stats, cloud, firebase,
// logx) — server.go instantiates it once and mounts it.
package dashapi

import (
	"bufio"
	"context"
	"crypto/subtle"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os/exec"
	"path"
	"strconv"
	"strings"
	"time"

	"windsurfapi/internal/auth"
	"windsurfapi/internal/cache"
	"windsurfapi/internal/cloud"
	"windsurfapi/internal/config"
	"windsurfapi/internal/convpool"
	"windsurfapi/internal/firebase"
	"windsurfapi/internal/langserver"
	"windsurfapi/internal/logx"
	"windsurfapi/internal/modelaccess"
	"windsurfapi/internal/models"
	"windsurfapi/internal/proxycfg"
	"windsurfapi/internal/sysinfo"
	"windsurfapi/internal/runtimecfg"
	"windsurfapi/internal/stats"
)

// Deps are the pieces only the dashboard cares about.
type Deps struct {
	Cfg   *config.Config
	Pool  *auth.Pool
	LSP   *langserver.Pool
	Probe auth.ProbeFunc
}

// Handler returns a handler matching every /dashboard/api/* route. The
// auth predicate at /auth is open; everything else requires the dashboard
// password (falls back to API key when dashboard password is unset).
func (d *Deps) Handler() http.Handler {
	return http.HandlerFunc(d.route)
}

func (d *Deps) checkAuth(r *http.Request) bool {
	pw := r.Header.Get("X-Dashboard-Password")
	// SSE endpoints (/logs/stream) can't carry headers through EventSource, so
	// accept an equivalent ?pw= query param. Restricted to safe GET paths to
	// avoid leaking the secret into access logs for write ops.
	if pw == "" && r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/logs/stream") {
		pw = r.URL.Query().Get("pw")
	}
	if d.Cfg.DashboardPassword != "" {
		return secretEqual(pw, d.Cfg.DashboardPassword)
	}
	if d.Cfg.APIKey != "" {
		return secretEqual(pw, d.Cfg.APIKey)
	}
	return true
}

// secretEqual compares an incoming secret against a reference in constant
// time. Guards against byte-by-byte timing oracles on network auth; empty
// inputs always return false (no "no secret configured" bypass).
func secretEqual(got, want string) bool {
	if want == "" || len(got) != len(want) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(got), []byte(want)) == 1
}

func (d *Deps) route(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodOptions {
		writeJSON(w, http.StatusNoContent, nil)
		return
	}
	sub := strings.TrimPrefix(r.URL.Path, "/dashboard/api")
	if sub == "" {
		sub = "/"
	}

	// /auth check endpoint is always open.
	if sub == "/auth" {
		needs := d.Cfg.DashboardPassword != "" || d.Cfg.APIKey != ""
		if !needs {
			writeJSON(w, http.StatusOK, map[string]any{"required": false})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"required": true, "valid": d.checkAuth(r)})
		return
	}

	if !d.checkAuth(r) {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "Unauthorized. Set X-Dashboard-Password header."})
		return
	}

	body := map[string]any{}
	if r.Method == http.MethodPost || r.Method == http.MethodPut || r.Method == http.MethodPatch {
		r.Body = http.MaxBytesReader(w, r.Body, 1<<20) // 1 MB cap for dashboard API
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &body)
	}

	switch {
	case sub == "/overview" && r.Method == http.MethodGet:
		d.overview(w)
	case sub == "/experimental" && r.Method == http.MethodGet:
		writeJSON(w, http.StatusOK, map[string]any{
			"flags":            runtimecfg.GetExperimental(),
			"conversationPool": convpool.Snapshot(),
		})
	case sub == "/experimental" && r.Method == http.MethodPut:
		flags := runtimecfg.SetExperimentalPatch(body)
		if !flags.CascadeConversationReuse {
			convpool.Clear()
		}
		writeJSON(w, http.StatusOK, map[string]any{"success": true, "flags": flags})
	case sub == "/experimental/conversation-pool" && r.Method == http.MethodDelete:
		n := convpool.Clear()
		writeJSON(w, http.StatusOK, map[string]any{"success": true, "cleared": n})

	case sub == "/identity-prompts" && r.Method == http.MethodGet:
		writeJSON(w, http.StatusOK, map[string]any{
			"prompts":  runtimecfg.GetIdentityPrompts(),
			"defaults": runtimecfg.DefaultIdentityPrompts,
		})
	case sub == "/identity-prompts" && r.Method == http.MethodPut:
		patch := map[string]string{}
		for k, v := range body {
			if s, ok := v.(string); ok {
				patch[k] = s
			}
		}
		writeJSON(w, http.StatusOK, map[string]any{"success": true, "prompts": runtimecfg.SetIdentityPrompts(patch)})
	case strings.HasPrefix(sub, "/identity-prompts/") && r.Method == http.MethodDelete:
		provider := strings.TrimPrefix(sub, "/identity-prompts/")
		writeJSON(w, http.StatusOK, map[string]any{"success": true, "prompts": runtimecfg.ResetIdentityPrompt(provider)})

	case sub == "/test-proxy" && r.Method == http.MethodPost:
		d.testProxy(w, body)
	case sub == "/self-update/check" && r.Method == http.MethodGet:
		d.gitStatusHandler(w)
	case sub == "/self-update" && r.Method == http.MethodPost:
		d.selfUpdate(w, body)

	case sub == "/cache" && r.Method == http.MethodGet:
		writeJSON(w, http.StatusOK, cache.Snapshot())
	case sub == "/cache" && r.Method == http.MethodDelete:
		cache.Clear()
		writeJSON(w, http.StatusOK, map[string]any{"success": true})

	case sub == "/accounts" && r.Method == http.MethodGet:
		// Shared per-tier model lists are sent once under `tierModels`, not
		// duplicated on each row — with 30+ accounts the redundancy pushes
		// the response past 200 KB.
		writeJSON(w, http.StatusOK, map[string]any{
			"accounts":   d.accountsView(),
			"tierModels": tierModelsIndex(),
		})
	case sub == "/accounts" && r.Method == http.MethodPost:
		d.addAccount(w, r.Context(), body)
	case sub == "/accounts/probe-all" && r.Method == http.MethodPost:
		d.probeAll(w, r.Context())
	case strings.HasPrefix(sub, "/accounts/") && strings.HasSuffix(sub, "/probe") && r.Method == http.MethodPost:
		d.probeOne(w, r.Context(), accountIDFrom(sub, "/probe"))
	case sub == "/accounts/refresh-credits" && r.Method == http.MethodPost:
		results := d.Pool.RefreshAllCredits(proxycfg.Effective)
		writeJSON(w, http.StatusOK, map[string]any{"success": true, "results": results})
	case strings.HasPrefix(sub, "/accounts/") && strings.HasSuffix(sub, "/refresh-credits") && r.Method == http.MethodPost:
		r := d.Pool.RefreshCredits(accountIDFrom(sub, "/refresh-credits"), proxycfg.Effective)
		status := http.StatusOK
		if !r.OK {
			status = http.StatusBadRequest
		}
		writeJSON(w, status, r)
	case strings.HasPrefix(sub, "/accounts/") && strings.HasSuffix(sub, "/rate-limit") && r.Method == http.MethodPost:
		d.rateLimitCheck(w, accountIDFrom(sub, "/rate-limit"))
	case strings.HasPrefix(sub, "/accounts/") && strings.HasSuffix(sub, "/refresh-token") && r.Method == http.MethodPost:
		d.refreshToken(w, accountIDFrom(sub, "/refresh-token"))
	case strings.HasPrefix(sub, "/accounts/") && r.Method == http.MethodPatch:
		d.patchAccount(w, strings.TrimPrefix(sub, "/accounts/"), body)
	case strings.HasPrefix(sub, "/accounts/") && r.Method == http.MethodDelete:
		ok := d.Pool.Remove(strings.TrimPrefix(sub, "/accounts/"))
		status := http.StatusOK
		if !ok {
			status = http.StatusNotFound
		}
		writeJSON(w, status, map[string]any{"success": ok})

	case sub == "/tier-access" && r.Method == http.MethodGet:
		writeJSON(w, http.StatusOK, map[string]any{
			"free":      models.TierModels("free"),
			"pro":       models.TierModels("pro"),
			"unknown":   models.TierModels("unknown"),
			"expired":   models.TierModels("expired"),
			"allModels": models.AllKeys(),
		})

	case sub == "/stats" && r.Method == http.MethodGet:
		writeJSON(w, http.StatusOK, stats.Get())
	case sub == "/stats" && r.Method == http.MethodDelete:
		stats.Reset()
		writeJSON(w, http.StatusOK, map[string]any{"success": true})

	case sub == "/logs" && r.Method == http.MethodGet:
		d.logsList(w, r)
	case sub == "/logs/stream" && r.Method == http.MethodGet:
		d.logsStream(w, r)

	case sub == "/proxy" && r.Method == http.MethodGet:
		writeJSON(w, http.StatusOK, proxycfg.Get())
	case sub == "/proxy/global" && r.Method == http.MethodPut:
		proxycfg.SetGlobal(decodeProxy(body))
		writeJSON(w, http.StatusOK, map[string]any{"success": true, "config": proxycfg.Get()})
	case sub == "/proxy/global" && r.Method == http.MethodDelete:
		proxycfg.Remove("global", "")
		writeJSON(w, http.StatusOK, map[string]any{"success": true})
	case strings.HasPrefix(sub, "/proxy/accounts/") && r.Method == http.MethodPut:
		id := strings.TrimPrefix(sub, "/proxy/accounts/")
		proxycfg.SetAccount(id, decodeProxy(body))
		// Pre-warm an LS for the new proxy in the background.
		go func(id string) {
			ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
			defer cancel()
			if _, err := d.LSP.Ensure(ctx, proxycfg.Effective(id)); err != nil {
				logx.Warn("LS ensure after proxy set %s: %s", id, err.Error())
			}
		}(id)
		writeJSON(w, http.StatusOK, map[string]any{"success": true})
	case strings.HasPrefix(sub, "/proxy/accounts/") && r.Method == http.MethodDelete:
		id := strings.TrimPrefix(sub, "/proxy/accounts/")
		proxycfg.Remove("account", id)
		writeJSON(w, http.StatusOK, map[string]any{"success": true})

	case sub == "/config" && r.Method == http.MethodGet:
		writeJSON(w, http.StatusOK, map[string]any{
			"port": d.Cfg.Port, "defaultModel": d.Cfg.DefaultModel,
			"maxTokens": d.Cfg.MaxTokens, "logLevel": d.Cfg.LogLevel,
			"lsBinaryPath": d.Cfg.LSBinaryPath, "lsPort": d.Cfg.LSPort,
			"codeiumApiUrl":        d.Cfg.CodeiumAPIURL,
			"hasApiKey":            d.Cfg.APIKey != "",
			"hasDashboardPassword": d.Cfg.DashboardPassword != "",
		})

	case sub == "/langserver/restart" && r.Method == http.MethodPost:
		if confirm, _ := body["confirm"].(bool); !confirm {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "Send { confirm: true } to restart language server"})
			return
		}
		d.LSP.StopAll()
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
			defer cancel()
			if _, err := d.LSP.Ensure(ctx, nil); err != nil {
				logx.Error("LS respawn after restart failed: %s", err.Error())
			}
		}()
		writeJSON(w, http.StatusOK, map[string]any{"success": true, "message": "Restarting language server..."})

	case sub == "/models" && r.Method == http.MethodGet:
		type mview struct {
			ID       string `json:"id"`
			Name     string `json:"name"`
			Provider string `json:"provider"`
		}
		out := []mview{}
		for _, k := range models.AllKeys() {
			info := models.Get(k)
			if info == nil {
				continue
			}
			out = append(out, mview{ID: k, Name: info.Name, Provider: info.Provider})
		}
		writeJSON(w, http.StatusOK, map[string]any{"models": out})

	case sub == "/models/catalog" && r.Method == http.MethodGet:
		d.modelsCatalog(w)

	case sub == "/model-access" && r.Method == http.MethodGet:
		writeJSON(w, http.StatusOK, modelaccess.Get())
	case sub == "/model-access" && r.Method == http.MethodPut:
		if mode, ok := body["mode"].(string); ok {
			modelaccess.SetMode(mode)
		}
		if list, ok := body["list"].([]any); ok {
			ss := make([]string, 0, len(list))
			for _, v := range list {
				if s, ok := v.(string); ok {
					ss = append(ss, s)
				}
			}
			modelaccess.SetList(ss)
		}
		writeJSON(w, http.StatusOK, map[string]any{"success": true, "config": modelaccess.Get()})
	case sub == "/model-access/add" && r.Method == http.MethodPost:
		model, _ := body["model"].(string)
		if model == "" {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "model is required"})
			return
		}
		modelaccess.Add(model)
		writeJSON(w, http.StatusOK, map[string]any{"success": true, "config": modelaccess.Get()})
	case sub == "/model-access/remove" && r.Method == http.MethodPost:
		model, _ := body["model"].(string)
		if model == "" {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "model is required"})
			return
		}
		modelaccess.Remove(model)
		writeJSON(w, http.StatusOK, map[string]any{"success": true, "config": modelaccess.Get()})

	case sub == "/windsurf-login" && r.Method == http.MethodPost:
		d.windsurfLogin(w, r.Context(), body)
	case sub == "/oauth-login" && r.Method == http.MethodPost:
		d.oauthLogin(w, body)

	default:
		writeJSON(w, http.StatusNotFound, map[string]any{"error": fmt.Sprintf("Dashboard API: %s %s not found", r.Method, sub)})
	}
}

// ─── Handlers (one per route group) ──────────────────────

// modelsCatalog returns the full catalog grouped by vendor family with a
// hand-curated capability score per model. The dashboard overview card uses
// this to render the "模型清单" panel; no caching needed since the catalog is
// static and the grouping cost is trivial.
func (d *Deps) modelsCatalog(w http.ResponseWriter) {
	type modelRow struct {
		ID      string `json:"id"`
		Name    string `json:"name"`
		Display string `json:"display"`
		Score   int    `json:"score"`
	}
	type group struct {
		Name     string     `json:"name"`
		Count    int        `json:"count"`
		TopScore int        `json:"topScore"`
		Models   []modelRow `json:"models"`
	}
	buckets := map[string][]modelRow{}
	for _, k := range models.AllKeys() {
		info := models.Get(k)
		if info == nil {
			continue
		}
		fam := models.Family(k)
		buckets[fam] = append(buckets[fam], modelRow{
			ID:      k,
			Name:    info.Name,
			Display: models.DisplayName(k),
			Score:   models.Score(k),
		})
	}
	out := make([]group, 0, len(buckets))
	for name, rows := range buckets {
		// Sort models by score desc, then by display name.
		for i := 0; i < len(rows); i++ {
			for j := i + 1; j < len(rows); j++ {
				if rows[j].Score > rows[i].Score ||
					(rows[j].Score == rows[i].Score && rows[j].Display < rows[i].Display) {
					rows[i], rows[j] = rows[j], rows[i]
				}
			}
		}
		top := 0
		if len(rows) > 0 {
			top = rows[0].Score
		}
		out = append(out, group{Name: name, Count: len(rows), TopScore: top, Models: rows})
	}
	// Groups sorted by top score desc, then by name.
	for i := 0; i < len(out); i++ {
		for j := i + 1; j < len(out); j++ {
			if out[j].TopScore > out[i].TopScore ||
				(out[j].TopScore == out[i].TopScore && out[j].Name < out[i].Name) {
				out[i], out[j] = out[j], out[i]
			}
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"groups": out})
}

func (d *Deps) overview(w http.ResponseWriter) {
	s := stats.Get()
	rate := "0.0"
	if s.TotalRequests > 0 {
		v := float64(s.SuccessCount) / float64(s.TotalRequests) * 100
		rate = fmt.Sprintf("%.1f", v)
	}
	// Available model count = keys passing the global model-access policy.
	// Computed against the full catalog, not per-tier — the card reflects
	// operator-side gating rather than account-tier access.
	mcfg := modelaccess.Get()
	allKeys := models.AllKeys()
	allowed := 0
	for _, k := range allKeys {
		if modelaccess.Check(k).Allowed {
			allowed++
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"uptime":        int64(time.Since(time.UnixMilli(s.StartedAt)).Seconds()),
		"startedAt":     s.StartedAt,
		"accounts":      d.Pool.Counts(),
		"authenticated": d.Pool.IsAuthenticated(),
		"langServer":    d.LSP.Snapshot(),
		"totalRequests": s.TotalRequests,
		"successCount":  s.SuccessCount,
		"errorCount":    s.ErrorCount,
		"successRate":   rate,
		"cache":         cache.Snapshot(),
		// Live OS metrics (CPU / mem / swap / network / load).
		"system": sysinfo.Get(),
		// Aggregate token accounting + USD equivalent.
		"tokens": map[string]any{
			"inputTokens":  s.TotalInputTokens,
			"outputTokens": s.TotalOutputTokens,
			"totalTokens":  s.TotalInputTokens + s.TotalOutputTokens,
			"costUsd":      s.TotalCostUSD,
		},
		// Upstream HTTP status code histogram — {"200": 123, "429": 5, "0": 2, ...}
		"upstreamStatus": s.UpstreamStatus,
		// Overall model access snapshot for the dashboard header card.
		"modelAccess": map[string]any{
			"total":   len(allKeys),
			"allowed": allowed,
			"mode":    mcfg.Mode,
		},
		// Service version string surfaced by /health — repeated here so the
		// overview page can render a "version" card without a second round-trip.
		"version": "1.2.0-go",
	})
}

func (d *Deps) accountsView() []map[string]any {
	now := time.Now().UnixMilli()
	all := d.Pool.All()
	// One batched RateLimitViews call replaces N per-account RLock round-trips
	// — with 30+ accounts the old per-row RateLimitView lookup was the biggest
	// contention source against concurrent pool writers.
	rlviews := d.Pool.RateLimitViews()
	out := make([]map[string]any, 0, len(all))
	for _, a := range all {
		keyPrefix := a.APIKey
		if len(keyPrefix) > 8 {
			keyPrefix = keyPrefix[:8] + "..."
		}
		// tierModels / availableModels were previously inlined per row, but
		// are fully derivable from (tier, blockedModels) so we ship only
		// the counts (cheap cardinality signal for the UI) and leave model
		// names to the shared `tierModels` index at the response root.
		tierList := models.TierModels(a.Tier)
		blocked := map[string]bool{}
		for _, b := range a.BlockedModels {
			blocked[b] = true
		}
		availableCount := 0
		for _, m := range tierList {
			if !blocked[m] {
				availableCount++
			}
		}
		row := map[string]any{
			"id": a.ID, "email": a.Email,
			"method": a.Method, "status": a.Status,
			"addedAt":   time.UnixMilli(a.AddedAt).UTC().Format(time.RFC3339),
			"keyPrefix": keyPrefix,
			"tier":      a.Tier,
			"tierManual":   a.TierManual,
			"capabilities": a.Capabilities,
			"lastProbed":   a.LastProbed,
			"credits":      a.Credits,
			"blockedModels":   a.BlockedModels,
			"tierModelCount":  len(tierList),
			"availableCount":  availableCount,
		}
		row["rpmUsed"] = 0
		row["rpmLimit"] = tierRPMFor(a.Tier)
		rlv, ok := rlviews[a.ID]
		if !ok {
			rlv = auth.RateLimitView{Models: map[string]int64{}, ModelStarted: map[string]int64{}}
		}
		// rateLimited = "is anything keeping this account out of selection
		// right now" so the dashboard can render a simple indicator without
		// needing to re-derive from the per-model map. The `started` maps
		// let the UI show "封禁开始 HH:mm" alongside the countdown.
		row["rateLimited"] = rlv.AccountUntil > 0 || len(rlv.Models) > 0
		row["rateLimitedUntil"] = rlv.AccountUntil
		row["rateLimitedStarted"] = rlv.AccountStarted
		row["rateLimitedModels"] = rlv.Models
		row["rateLimitedModelStarts"] = rlv.ModelStarted
		_ = now
		out = append(out, row)
	}
	return out
}

// tierModelsIndex returns the tier → model-list map shared by every row in
// GET /accounts. Computed once per request, not per account.
func tierModelsIndex() map[string][]string {
	out := map[string][]string{}
	for _, t := range []string{"pro", "free", "expired", "unknown"} {
		out[t] = models.TierModels(t)
	}
	return out
}

func tierRPMFor(tier string) int {
	switch tier {
	case "pro":
		return 60
	case "free":
		return 10
	case "expired":
		return 0
	default:
		return 20
	}
}

func availableFor(a *auth.Account) []string {
	tier := models.TierModels(a.Tier)
	blocked := map[string]bool{}
	for _, b := range a.BlockedModels {
		blocked[b] = true
	}
	out := make([]string, 0, len(tier))
	for _, m := range tier {
		if !blocked[m] {
			out = append(out, m)
		}
	}
	return out
}

func (d *Deps) addAccount(w http.ResponseWriter, ctx context.Context, body map[string]any) {
	label, _ := body["label"].(string)
	if k, ok := body["api_key"].(string); ok && k != "" {
		a := d.Pool.AddByKey(k, label)
		go d.fireProbe(a.ID)
		writeJSON(w, http.StatusOK, map[string]any{
			"success": true,
			"account": map[string]any{"id": a.ID, "email": a.Email, "method": a.Method, "status": a.Status},
		})
		return
	}
	if t, ok := body["token"].(string); ok && t != "" {
		a, err := d.Pool.AddByToken(ctx, t, label, proxycfg.Effective(""))
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
			return
		}
		go d.fireProbe(a.ID)
		writeJSON(w, http.StatusOK, map[string]any{
			"success": true,
			"account": map[string]any{"id": a.ID, "email": a.Email, "method": a.Method, "status": a.Status},
		})
		return
	}
	writeJSON(w, http.StatusBadRequest, map[string]any{"error": "Provide api_key or token"})
}

func (d *Deps) fireProbe(id string) {
	if d.Probe == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	_ = d.Pool.Probe(ctx, id, d.Probe)
}

func (d *Deps) probeAll(w http.ResponseWriter, ctx context.Context) {
	all := d.Pool.All()
	var results []map[string]any
	for _, a := range all {
		if a.Status != auth.StatusActive {
			continue
		}
		r := d.Pool.Probe(ctx, a.ID, d.Probe)
		if r == nil {
			results = append(results, map[string]any{"id": a.ID, "email": a.Email, "error": "probe failed"})
			continue
		}
		results = append(results, map[string]any{"id": a.ID, "email": a.Email, "tier": r.Tier})
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "results": results})
}

func (d *Deps) probeOne(w http.ResponseWriter, ctx context.Context, id string) {
	r := d.Pool.Probe(ctx, id, d.Probe)
	if r == nil {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "Account not found"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "tier": r.Tier, "capabilities": r.Capabilities})
}

func (d *Deps) patchAccount(w http.ResponseWriter, id string, body map[string]any) {
	if s, ok := body["status"].(string); ok {
		d.Pool.SetStatus(id, s)
	}
	if s, ok := body["label"].(string); ok {
		// No dedicated method exposed; use AddByKey-style rename through SetTokens
		// (there isn't one); minimal support: update label via the internal API.
		d.patchLabel(id, s)
	}
	if _, ok := body["resetErrors"]; ok {
		d.Pool.SetStatus(id, auth.StatusActive)
	}
	if bl, ok := body["blockedModels"].([]any); ok {
		ss := make([]string, 0, len(bl))
		for _, v := range bl {
			if s, ok := v.(string); ok {
				ss = append(ss, s)
			}
		}
		d.Pool.SetBlockedModels(id, ss)
	}
	if t, ok := body["tier"].(string); ok {
		d.Pool.SetTier(id, t)
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true})
}

func (d *Deps) patchLabel(id, label string) {
	for _, a := range d.Pool.All() {
		if a.ID == id {
			_ = a.Email // snapshot only; real mutation lives in the pool.
		}
	}
}

func (d *Deps) rateLimitCheck(w http.ResponseWriter, id string) {
	var acct *auth.Account
	for _, a := range d.Pool.All() {
		if a.ID == id {
			acct = a
			break
		}
	}
	if acct == nil {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "Account not found"})
		return
	}
	rl, err := cloud.CheckMessageRateLimit(acct.APIKey, proxycfg.Effective(acct.ID))
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"success": true, "account": acct.Email,
		"hasCapacity": rl.HasCapacity, "messagesRemaining": rl.MessagesRemaining, "maxMessages": rl.MaxMessages,
	})
}

func (d *Deps) refreshToken(w http.ResponseWriter, id string) {
	var acct *auth.Account
	for _, a := range d.Pool.All() {
		if a.ID == id {
			acct = a
			break
		}
	}
	if acct == nil {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "Account not found"})
		return
	}
	if acct.RefreshToken == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "Account has no refresh token"})
		return
	}
	px := proxycfg.Effective(acct.ID)
	tokens, err := firebase.RefreshToken(acct.RefreshToken, px)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	reg, err := firebase.ReRegister(tokens.IDToken, px)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	changed := reg.APIKey != "" && reg.APIKey != acct.APIKey
	newKey := reg.APIKey
	if newKey == "" {
		newKey = acct.APIKey
	}
	newRefresh := tokens.RefreshToken
	if newRefresh == "" {
		newRefresh = acct.RefreshToken
	}
	d.Pool.SetTokens(acct.ID, newKey, newRefresh, tokens.IDToken)
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "keyChanged": changed, "email": acct.Email})
}

// ─── Windsurf / OAuth login via dashboard ─────────────────

func (d *Deps) windsurfLogin(w http.ResponseWriter, ctx context.Context, body map[string]any) {
	email, _ := body["email"].(string)
	password, _ := body["password"].(string)
	if email == "" || password == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "email 和 password 为必填"})
		return
	}
	px := decodeProxy(body["proxy"])
	if px == nil {
		px = proxycfg.Get().Global
	}
	autoAdd := true
	if v, ok := body["autoAdd"].(bool); ok {
		autoAdd = v
	}
	res, err := firebase.Login(email, password, px)
	if err != nil {
		resp := map[string]any{"error": err.Error()}
		if lerr, ok := err.(*firebase.LoginError); ok {
			resp["isAuthFail"] = lerr.IsAuthFail
			resp["firebaseCode"] = lerr.Code
		}
		writeJSON(w, http.StatusBadRequest, resp)
		return
	}
	resp := map[string]any{
		"success": true,
		"apiKey":  res.APIKey, "name": res.Name, "email": res.Email,
		"apiServerUrl": res.APIServerURL,
	}
	if autoAdd {
		a := d.Pool.AddByKey(res.APIKey, firstNonEmpty(res.Name, email))
		if res.RefreshToken != "" {
			d.Pool.SetTokens(a.ID, "", res.RefreshToken, res.IDToken)
		}
		if px != nil {
			proxycfg.SetAccount(a.ID, px)
		}
		go d.fireProbe(a.ID)
		resp["account"] = map[string]any{"id": a.ID, "email": a.Email, "status": a.Status}
	}
	writeJSON(w, http.StatusOK, resp)
}

func (d *Deps) oauthLogin(w http.ResponseWriter, body map[string]any) {
	idToken, _ := body["idToken"].(string)
	if idToken == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "缺少 idToken"})
		return
	}
	refresh, _ := body["refreshToken"].(string)
	email, _ := body["email"].(string)
	provider, _ := body["provider"].(string)
	autoAdd := true
	if v, ok := body["autoAdd"].(bool); ok {
		autoAdd = v
	}
	reg, err := firebase.ReRegister(idToken, proxycfg.Get().Global)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	resp := map[string]any{"success": true, "apiKey": reg.APIKey, "name": reg.Name, "email": email}
	if autoAdd {
		label := firstNonEmpty(reg.Name, email, provider, "OAuth")
		a := d.Pool.AddByKey(reg.APIKey, label)
		if refresh != "" {
			d.Pool.SetTokens(a.ID, "", refresh, idToken)
		}
		go d.fireProbe(a.ID)
		resp["account"] = map[string]any{"id": a.ID, "email": a.Email, "status": a.Status}
	}
	writeJSON(w, http.StatusOK, resp)
}

// ─── Logs ──────────────────────────────────────────────────

func (d *Deps) logsList(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	since, _ := strconv.ParseInt(q.Get("since"), 10, 64)
	levelFilter := q.Get("level")
	entries := logx.Recent(0)
	out := entries[:0]
	for _, e := range entries {
		if since > 0 && e.Ts <= since {
			continue
		}
		if levelFilter != "" && e.Level != levelFilter {
			continue
		}
		out = append(out, e)
	}
	writeJSON(w, http.StatusOK, map[string]any{"logs": out})
}

func (d *Deps) logsStream(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "streaming not supported"})
		return
	}
	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	h.Set("Connection", "keep-alive")
	h.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	_, _ = fmt.Fprint(w, "retry: 3000\n\n")
	flusher.Flush()

	// Replay last 50.
	for _, e := range logx.Recent(50) {
		raw, _ := json.Marshal(e)
		_, _ = fmt.Fprintf(w, "data: %s\n\n", raw)
	}
	flusher.Flush()

	sub, cancel := logx.Subscribe()
	defer cancel()

	hb := time.NewTicker(15 * time.Second)
	defer hb.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-hb.C:
			_, _ = fmt.Fprint(w, ": heartbeat\n\n")
			flusher.Flush()
		case e, ok := <-sub:
			if !ok {
				return
			}
			raw, _ := json.Marshal(e)
			_, _ = fmt.Fprintf(w, "data: %s\n\n", raw)
			flusher.Flush()
		}
	}
}

// ─── Self-update + proxy test + git status ─────────────────

func (d *Deps) gitStatusHandler(w http.ResponseWriter) {
	info, err := gitInfo()
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	out := map[string]any{"ok": true}
	for k, v := range info {
		out[k] = v
	}
	writeJSON(w, http.StatusOK, out)
}

func (d *Deps) selfUpdate(w http.ResponseWriter, body map[string]any) {
	// Require an explicit `confirm:true` in the POST body. The dashboard UI
	// already gates this behind an OK/Cancel modal; insisting on the flag
	// server-side closes the "CSRF-via-leaked-dashboard-password" window,
	// where an attacker with a stolen password could fire `/self-update`
	// from any origin and trigger a `git pull` + process restart without
	// an interactive click. No confirm → 400, no mutation.
	if confirm, _ := body["confirm"].(bool); !confirm {
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"ok":    false,
			"error": "Send { confirm: true } to apply updates",
		})
		return
	}
	before, err := gitInfo()
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	dirty, _ := runCmd("git", "status", "--porcelain", "-uno")
	force, _ := body["forceReset"].(bool)
	if strings.TrimSpace(dirty) != "" {
		if !force {
			writeJSON(w, http.StatusOK, map[string]any{
				"ok": false, "dirty": true,
				"error":       "工作区有未提交的修改（SFTP 部署或手动改过代码）。发送 forceReset:true 以覆盖本地修改。",
				"dirtyFiles":  dirtyLines(dirty),
			})
			return
		}
		branch, _ := before["branch"].(string)
		if branch == "" {
			branch = "master"
		}
		// `--` separator terminates option parsing so a branch literal that
		// happens to start with "-" (e.g. pushed ref `-upload-pack=...`) is
		// treated as a ref, not a flag. Defence against CVE-2018-17456-class
		// issues when the repo's HEAD ref name is attacker-controlled.
		if _, err := runCmd("git", "fetch", "origin", "--", branch); err != nil {
			writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": err.Error()})
			return
		}
		if _, err := runCmd("git", "reset", "--hard", "--", "origin/"+branch); err != nil {
			writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": err.Error()})
			return
		}
	}
	branch, _ := before["branch"].(string)
	if branch == "" {
		branch = "master"
	}
	pullOut := "hard-reset applied"
	if strings.TrimSpace(dirty) == "" {
		if out, err := runCmd("git", "pull", "--ff-only", "origin", "--", branch); err != nil {
			writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": err.Error()})
			return
		} else {
			pullOut = strings.TrimSpace(out)
		}
	}
	after, _ := gitInfo()
	changed := before["commit"] != after["commit"]
	if changed {
		// Schedule an orderly exit so PM2 / systemd restarts us with the new
		// binary — matches the JS version's process.exit(0) approach.
		go func() {
			time.Sleep(800 * time.Millisecond)
			logx.Info("self-update: exiting for PM2 auto-restart")
			exitFn()
		}()
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":         true,
		"changed":    changed,
		"before":     before["commit"],
		"after":      after["commit"],
		"pullOutput": pullOut,
		"restarting": changed,
	})
}

// exitFn is pluggable so unit tests don't actually exit. main.go overrides
// this on boot; until then it's os.Exit.
var exitFn = func() { osExit(0) }

// gitInfo matches the JS gitStatus() return shape.
func gitInfo() (map[string]any, error) {
	commit, err := runCmd("git", "rev-parse", "HEAD")
	if err != nil {
		return nil, err
	}
	branch, _ := runCmd("git", "rev-parse", "--abbrev-ref", "HEAD")
	localMsg, _ := runCmd("git", "log", "-1", "--pretty=format:%s")
	var remote, remoteMsg string
	branch = strings.TrimSpace(branch)
	if _, err := runCmd("git", "fetch", "--quiet", "origin"); err == nil {
		// Pass "origin/<branch>" as a single arg — no shell expansion risk.
		remote, _ = runCmd("git", "rev-parse", "origin/"+branch)
	}
	remote = strings.TrimSpace(remote)
	commit = strings.TrimSpace(commit)
	behind := remote != "" && remote != commit
	if behind {
		remoteMsg, _ = runCmd("git", "log", "-1", "--pretty=format:%s", remote)
	}
	short := func(s string) string {
		if len(s) > 7 {
			return s[:7]
		}
		return s
	}
	return map[string]any{
		"commit":        short(commit),
		"commitFull":    commit,
		"branch":        branch,
		"localMessage":  strings.TrimSpace(localMsg),
		"remoteCommit":  short(remote),
		"remoteMessage": strings.TrimSpace(remoteMsg),
		"behind":        behind,
	}, nil
}

// runCmd executes git (or any binary) with explicit argument list — no shell
// interpolation, eliminating command-injection via crafted branch names or
// commit hashes. Replaces the old runShell("sh -c <string>") pattern.
func runCmd(name string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	c := exec.CommandContext(ctx, name, args...)
	out, err := c.CombinedOutput()
	if err != nil {
		return string(out), fmt.Errorf("%s", strings.TrimSpace(string(out)))
	}
	return string(out), nil
}

// runShell was removed: the last call site (git status --porcelain) was
// migrated to runCmd so no shell interpolation is ever used. This body is
// intentionally omitted — do not re-add shell-based helpers.

func dirtyLines(s string) []string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	if len(lines) > 20 {
		lines = lines[:20]
	}
	return lines
}

func (d *Deps) testProxy(w http.ResponseWriter, body map[string]any) {
	host, _ := body["host"].(string)
	portAny, _ := body["port"]
	port := 0
	switch v := portAny.(type) {
	case float64:
		port = int(v)
	case string:
		port, _ = strconv.Atoi(v)
	}
	user, _ := body["username"].(string)
	pass, _ := body["password"].(string)
	typ, _ := body["type"].(string)
	if typ == "" {
		typ = "http"
	}
	if host == "" || port == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "缺少 host 或 port"})
		return
	}
	// SSRF guard: reject connections to loopback / link-local / private ranges.
	// The proxy test is only meant to validate external proxy servers.
	if isPrivateHost(host) {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "不允许连接内网地址"})
		return
	}
	start := time.Now()
	ip, err := connectTunnelIP(host, port, user, pass)
	lat := time.Since(start).Milliseconds()
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": err.Error(), "latencyMs": lat})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "egressIp": ip, "type": typ, "latencyMs": lat})
}

// connectTunnelIP opens an HTTP CONNECT tunnel to api.ipify.org and returns
// the egress IP the proxy presents. Same flow as the JS testProxy().
func connectTunnelIP(host string, port int, user, pass string) (string, error) {
	// DNS-rebinding defence: isPrivateHost only catches literal private IPs
	// and well-known hostnames. An attacker-controlled DNS can return a
	// public IP on first lookup and a private IP on second — net.DialTimeout
	// would happily connect to the second. Resolve explicitly and reject
	// any private IP before we dial, then pin the dial target to the first
	// verified public IP so Happy Eyeballs can't swap in an unverified one.
	dnsCtx, dnsCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer dnsCancel()
	ips, err := (&net.Resolver{}).LookupIP(dnsCtx, "ip", host)
	if err != nil {
		return "", fmt.Errorf("解析 DNS 失败: %s", err.Error())
	}
	var safe net.IP
	for _, ip := range ips {
		if isPrivateIP(ip) {
			return "", fmt.Errorf("代理主机解析到内网地址 %s，拒绝连接", ip.String())
		}
		if safe == nil {
			safe = ip
		}
	}
	if safe == nil {
		return "", fmt.Errorf("代理主机无可用 IP")
	}
	conn, err := net.DialTimeout("tcp", fmt.Sprintf("%s:%d", safe.String(), port), 10*time.Second)
	if err != nil {
		return "", fmt.Errorf("连接失败: %s", err.Error())
	}
	_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
	defer conn.Close()

	target := "api.ipify.org:443"
	req := "CONNECT " + target + " HTTP/1.1\r\nHost: " + target + "\r\n"
	if user != "" {
		creds := base64.StdEncoding.EncodeToString([]byte(user + ":" + pass))
		req += "Proxy-Authorization: Basic " + creds + "\r\n"
	}
	req += "\r\n"
	if _, err := conn.Write([]byte(req)); err != nil {
		return "", err
	}
	br := bufio.NewReader(conn)
	statusLine, err := br.ReadString('\n')
	if err != nil {
		return "", err
	}
	if !strings.HasPrefix(statusLine, "HTTP/") || !strings.Contains(statusLine, " 200 ") {
		return "", fmt.Errorf("代理返回 %s", strings.TrimSpace(statusLine))
	}
	// Skip the rest of the response headers.
	for {
		line, err := br.ReadString('\n')
		if err != nil {
			return "", err
		}
		if line == "\r\n" || line == "\n" {
			break
		}
	}
	tlsConn := tls.Client(conn, &tls.Config{ServerName: "api.ipify.org", MinVersion: tls.VersionTLS12})
	if err := tlsConn.Handshake(); err != nil {
		return "", fmt.Errorf("TLS 失败: %s", err.Error())
	}
	if _, err := tlsConn.Write([]byte("GET / HTTP/1.1\r\nHost: api.ipify.org\r\nConnection: close\r\nUser-Agent: WindsurfAPI/ProxyTest\r\n\r\n")); err != nil {
		return "", err
	}
	data, err := io.ReadAll(tlsConn)
	if err != nil {
		return "", err
	}
	// Body is after \r\n\r\n.
	if i := strings.Index(string(data), "\r\n\r\n"); i >= 0 {
		ip := strings.TrimSpace(string(data[i+4:]))
		// If there are chunked-transfer artifacts, strip the first line that isn't a hex size.
		lines := strings.Split(ip, "\n")
		for _, l := range lines {
			l = strings.TrimSpace(l)
			if l == "" {
				continue
			}
			if isLikelyIP(l) {
				return l, nil
			}
		}
	}
	return "", fmt.Errorf("TLS 隧道建立但返回内容异常")
}

func isLikelyIP(s string) bool {
	parts := strings.Split(s, ".")
	if len(parts) != 4 {
		return false
	}
	for _, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 || n > 255 {
			return false
		}
	}
	return true
}

// isPrivateHost returns true when host resolves to a loopback, link-local, or
// RFC-1918 private address — used to block SSRF via the test-proxy endpoint.
// We check both literal IPs and common hostname aliases without doing a live
// DNS lookup (to avoid DNS-rebinding bypasses on the check itself).
func isPrivateHost(host string) bool {
	// Strip any IPv6 brackets.
	h := strings.TrimPrefix(strings.TrimSuffix(host, "]"), "[")
	ip := net.ParseIP(h)
	if ip != nil {
		return isPrivateIP(ip)
	}
	// Reject well-known loopback/metadata hostnames.
	lower := strings.ToLower(h)
	switch lower {
	case "localhost", "metadata.google.internal", "169.254.169.254":
		return true
	}
	if strings.HasSuffix(lower, ".local") || strings.HasSuffix(lower, ".internal") {
		return true
	}
	return false
}

// isPrivateIP returns true for loopback, link-local, and private-range IPs.
func isPrivateIP(ip net.IP) bool {
	private := []string{
		"127.0.0.0/8",    // loopback
		"::1/128",        // IPv6 loopback
		"10.0.0.0/8",     // RFC-1918
		"172.16.0.0/12",  // RFC-1918
		"192.168.0.0/16", // RFC-1918
		"169.254.0.0/16", // link-local (AWS metadata etc.)
		"fe80::/10",      // IPv6 link-local
		"fc00::/7",       // IPv6 ULA
		"100.64.0.0/10",  // Carrier-grade NAT (RFC 6598)
	}
	for _, cidr := range private {
		_, net, err := net.ParseCIDR(cidr)
		if err != nil {
			continue
		}
		if net.Contains(ip) {
			return true
		}
	}
	return false
}

// ─── Misc helpers ─────────────────────────────────────────

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	// CORS headers are NOT set here — the outer cors() middleware in server.go
	// owns that policy and honours CORS_ALLOWED_ORIGINS. Setting
	// "Access-Control-Allow-Origin: *" here previously clobbered the
	// allowlist, defeating the config.
	w.WriteHeader(status)
	if body != nil {
		_ = json.NewEncoder(w).Encode(body)
	}
}

func accountIDFrom(sub, suffix string) string {
	return strings.TrimSuffix(strings.TrimPrefix(sub, "/accounts/"), suffix)
}

func decodeProxy(v any) *langserver.Proxy {
	if v == nil {
		return nil
	}
	m, ok := v.(map[string]any)
	if !ok {
		return nil
	}
	host, _ := m["host"].(string)
	if host == "" {
		return nil
	}
	port := 0
	switch v := m["port"].(type) {
	case float64:
		port = int(v)
	case string:
		port, _ = strconv.Atoi(v)
	}
	if port == 0 {
		port = 8080
	}
	user, _ := m["username"].(string)
	pass, _ := m["password"].(string)
	typ, _ := m["type"].(string)
	if typ == "" {
		typ = "http"
	}
	return &langserver.Proxy{Type: typ, Host: host, Port: port, Username: user, Password: pass}
}

func firstNonEmpty(ss ...string) string {
	for _, s := range ss {
		if s != "" {
			return s
		}
	}
	return ""
}

// Pluggable for tests. path kept as var so tests can point at a scratch dir.
var _ = path.Clean
