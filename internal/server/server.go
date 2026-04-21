// Package server wires HTTP routes for the public API surface. Dashboard
// routes land in a later phase; this file hosts the OpenAI-shape endpoints
// and the shared helpers.
package server

import (
	"encoding/json"
	"net/http"
	"path"
	"strings"

	"windsurfapi/internal/auth"
	"windsurfapi/internal/config"
	"windsurfapi/internal/dashapi"
	"windsurfapi/internal/langserver"
	"windsurfapi/internal/models"
	"windsurfapi/internal/web"
)

// dashboardFS is the Vite-built SPA rooted at the dist/ directory.
var dashboardFS = web.DistFS()
var dashboardIndex = web.IndexHTML()

// Deps is the set of dependencies injected into handlers.
type Deps struct {
	Cfg  *config.Config
	Pool *auth.Pool
	LSP  *langserver.Pool
}

// Handler builds an *http.ServeMux wired with the API routes.
func Handler(d *Deps) http.Handler {
	mux := http.NewServeMux()

	// /health is open; everything else goes through validateAPIKey.
	mux.HandleFunc("/health", d.Health)
	mux.HandleFunc("/auth/status", d.AuthStatus)
	mux.HandleFunc("/auth/accounts", d.AuthAccounts)
	mux.HandleFunc("/auth/login", d.AuthLogin)

	mux.Handle("/v1/models", d.authMiddleware(http.HandlerFunc(d.ModelsList)))
	mux.Handle("/v1/chat/completions", d.authMiddleware(http.HandlerFunc(d.ChatCompletions)))
	mux.Handle("/v1/messages", d.authMiddleware(http.HandlerFunc(d.Messages)))

	// Dashboard SPA + API.
	mux.HandleFunc("/dashboard", servePanel)
	mux.HandleFunc("/dashboard/", servePanel)
	dapi := (&dashapi.Deps{Cfg: d.Cfg, Pool: d.Pool, LSP: d.LSP, Probe: d.MakeProbeFunc()}).Handler()
	mux.Handle("/dashboard/api/", dapi)

	return cors(d.Cfg, mux)
}

// servePanel serves the Vite-built Vue SPA from the embedded dist/ FS. Asset
// requests (hashed JS/CSS/fonts/images) resolve directly; anything else —
// including deep client-router paths like /dashboard/accounts — falls back to
// index.html so Vue Router can handle the navigation on first load.
func servePanel(w http.ResponseWriter, r *http.Request) {
	sub := strings.TrimPrefix(r.URL.Path, "/dashboard")
	sub = strings.TrimPrefix(sub, "/")
	if sub == "" {
		writeSPAIndex(w)
		return
	}
	clean := path.Clean(sub)
	if strings.HasPrefix(clean, "..") {
		http.NotFound(w, r)
		return
	}
	f, err := dashboardFS.Open(clean)
	if err != nil {
		writeSPAIndex(w)
		return
	}
	stat, err := f.Stat()
	_ = f.Close()
	if err != nil || stat.IsDir() {
		writeSPAIndex(w)
		return
	}
	if strings.HasPrefix(clean, "assets/") {
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	} else {
		w.Header().Set("Cache-Control", "no-cache")
	}
	http.ServeFileFS(w, r, dashboardFS, clean)
}

func writeSPAIndex(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(dashboardIndex)
}


// ─── CORS + auth helpers ──────────────────────────────────

// cors honours the CORS_ALLOWED_ORIGINS env var.
//
//   - empty  → no CORS headers at all; browser blocks cross-origin calls.
//   - "*"    → legacy wildcard.
//   - "a,b"  → only echo Access-Control-Allow-Origin when the request's
//              Origin header exactly matches one of the listed values.
func cors(cfg *config.Config, next http.Handler) http.Handler {
	allowed := parseCORSOrigins(cfg.CORSAllowedOrigins)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		if allowed != nil && origin != "" {
			if ok, matched := checkCORSOrigin(allowed, origin); ok {
				h := w.Header()
				h.Set("Access-Control-Allow-Origin", matched)
				if matched != "*" {
					h.Add("Vary", "Origin")
				}
				h.Set("Access-Control-Allow-Methods", "GET, POST, PUT, PATCH, DELETE, OPTIONS")
				h.Set("Access-Control-Allow-Headers", "Content-Type, Authorization, X-Dashboard-Password")
				h.Set("Access-Control-Allow-Credentials", "false")
			}
		}
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// parseCORSOrigins returns nil when CORS is disabled, a single-element slice
// [""] with value "*" for wildcard mode, or the trimmed list.
func parseCORSOrigins(v string) []string {
	v = strings.TrimSpace(v)
	if v == "" {
		return nil
	}
	if v == "*" {
		return []string{"*"}
	}
	var out []string
	for _, p := range strings.Split(v, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func checkCORSOrigin(allowed []string, origin string) (bool, string) {
	if len(allowed) == 1 && allowed[0] == "*" {
		return true, "*"
	}
	for _, a := range allowed {
		if a == origin {
			return true, origin
		}
	}
	return false, ""
}

func bearerToken(r *http.Request) string {
	h := r.Header.Get("Authorization")
	if strings.HasPrefix(h, "Bearer ") {
		return strings.TrimSpace(h[7:])
	}
	// Anthropic clients sometimes use x-api-key instead.
	if k := r.Header.Get("x-api-key"); k != "" {
		return k
	}
	return h
}

func (d *Deps) authMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if d.Cfg.APIKey == "" {
			next.ServeHTTP(w, r)
			return
		}
		if bearerToken(r) != d.Cfg.APIKey {
			writeJSON(w, http.StatusUnauthorized, errBody("Invalid API key", "auth_error"))
			return
		}
		next.ServeHTTP(w, r)
	})
}

// ─── JSON helpers ─────────────────────────────────────────

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func errBody(msg, typ string) map[string]any {
	return map[string]any{"error": map[string]any{"message": msg, "type": typ}}
}

// ─── Tiny handlers (health/models/auth) ──────────────────

// Health returns an LB-friendly minimal response anonymously. When the caller
// authenticates with the bearer token we include sensitive internals (account
// counts, LS PID/port/uptime breakdown). This keeps "is it up?" checks
// trivially callable while closing the anonymous info-leak on account
// counts + LS pid that existed previously.
func (d *Deps) Health(w http.ResponseWriter, r *http.Request) {
	body := map[string]any{
		"status":   "ok",
		"provider": "WindsurfAPI bydwgx1337",
		"version":  "1.2.0-go",
	}
	// Only expose internals when the caller proves they're authorised.
	if d.Cfg.APIKey == "" || bearerToken(r) == d.Cfg.APIKey {
		body["uptime"] = int64(uptimeSeconds())
		body["accounts"] = d.Pool.Counts()
		body["ls"] = d.LSP.Snapshot()
	}
	writeJSON(w, http.StatusOK, body)
}

func (d *Deps) ModelsList(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"object": "list",
		"data":   models.ListOpenAI(),
	})
}

func (d *Deps) AuthStatus(w http.ResponseWriter, _ *http.Request) {
	c := d.Pool.Counts()
	writeJSON(w, http.StatusOK, map[string]any{
		"authenticated": d.Pool.IsAuthenticated(),
		"total":         c.Total, "active": c.Active, "error": c.Error,
	})
}

func (d *Deps) AuthAccounts(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		// Omit full apiKey from the public-API response — callers only need id/email/status.
		// Full keys are available through the dashboard API (which requires dashboard auth).
		all := d.Pool.All()
		out := make([]map[string]any, 0, len(all))
		for _, a := range all {
			kp := a.APIKey
			if len(kp) > 8 {
				kp = kp[:8] + "..."
			}
			out = append(out, map[string]any{
				"id": a.ID, "email": a.Email, "method": a.Method,
				"status": a.Status, "tier": a.Tier, "keyPrefix": kp,
			})
		}
		writeJSON(w, http.StatusOK, map[string]any{"accounts": out})
		return
	}
	if strings.HasPrefix(r.URL.Path, "/auth/accounts/") && r.Method == http.MethodDelete {
		id := strings.TrimPrefix(r.URL.Path, "/auth/accounts/")
		ok := d.Pool.Remove(id)
		if ok {
			writeJSON(w, http.StatusOK, map[string]any{"success": true})
		} else {
			writeJSON(w, http.StatusNotFound, map[string]any{"success": false})
		}
		return
	}
	writeJSON(w, http.StatusMethodNotAllowed, errBody("method not allowed", "method_not_allowed"))
}

// AuthLogin accepts api_key / token / email+password (and batches of any).
func (d *Deps) AuthLogin(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, errBody("method not allowed", "method_not_allowed"))
		return
	}
	var body struct {
		APIKey   string `json:"api_key"`
		Token    string `json:"token"`
		Email    string `json:"email"`
		Password string `json:"password"`
		Label    string `json:"label"`
		Accounts []struct {
			APIKey   string `json:"api_key"`
			Token    string `json:"token"`
			Email    string `json:"email"`
			Password string `json:"password"`
			Label    string `json:"label"`
		} `json:"accounts"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20) // 1 MB cap
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "Invalid JSON"})
		return
	}
	ctx := r.Context()
	globalProxy := proxycfgEffective("")
	if len(body.Accounts) > 0 {
		out := make([]map[string]any, 0, len(body.Accounts))
		for _, a := range body.Accounts {
			switch {
			case a.APIKey != "":
				acct := d.Pool.AddByKey(a.APIKey, a.Label)
				out = append(out, map[string]any{"id": acct.ID, "email": acct.Email, "status": acct.Status})
			case a.Token != "":
				acct, err := d.Pool.AddByToken(ctx, a.Token, a.Label, globalProxy)
				if err != nil {
					out = append(out, map[string]any{"email": a.Label, "error": err.Error()})
					continue
				}
				out = append(out, map[string]any{"id": acct.ID, "email": acct.Email, "status": acct.Status})
			case a.Email != "" && a.Password != "":
				acct, _, err := d.Pool.AddByEmail(ctx, a.Email, a.Password, globalProxy)
				if err != nil {
					out = append(out, map[string]any{"email": a.Email, "error": err.Error()})
					continue
				}
				out = append(out, map[string]any{"id": acct.ID, "email": acct.Email, "status": acct.Status})
			default:
				out = append(out, map[string]any{"error": "Missing credentials"})
			}
		}
		c := d.Pool.Counts()
		writeJSON(w, http.StatusOK, map[string]any{"results": out, "total": c.Total, "active": c.Active, "error": c.Error})
		return
	}
	switch {
	case body.APIKey != "":
		acct := d.Pool.AddByKey(body.APIKey, body.Label)
		writeJSON(w, http.StatusOK, map[string]any{
			"success": true,
			"account": map[string]any{"id": acct.ID, "email": acct.Email, "method": acct.Method, "status": acct.Status},
		})
	case body.Token != "":
		acct, err := d.Pool.AddByToken(ctx, body.Token, body.Label, globalProxy)
		if err != nil {
			writeJSON(w, http.StatusUnauthorized, map[string]any{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"success": true,
			"account": map[string]any{"id": acct.ID, "email": acct.Email, "method": acct.Method, "status": acct.Status},
		})
	case body.Email != "" && body.Password != "":
		acct, res, err := d.Pool.AddByEmail(ctx, body.Email, body.Password, globalProxy)
		if err != nil {
			writeJSON(w, http.StatusUnauthorized, map[string]any{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"success": true,
			"account": map[string]any{"id": acct.ID, "email": acct.Email, "method": acct.Method, "status": acct.Status},
			"apiKey":  res.APIKey, "name": res.Name,
		})
	default:
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "Provide api_key, token, or email+password"})
	}
}

// ─── Uptime ────────────────────────────────────────────────
// uptimeSeconds is rewired from main via SetStart.

var startUnix int64

// SetStart is called by main right before listening so /health can show uptime.
func SetStart(unixSeconds int64) { startUnix = unixSeconds }
func uptimeSeconds() int64 {
	return int64(nowSec() - startUnix)
}

// Overridable time source for tests.
var nowSec = func() int64 {
	return defaultNowSec()
}
