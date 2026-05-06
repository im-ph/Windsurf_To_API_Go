// WindsurfAPI main entrypoint (P3). Boots the LS pool, loads persistent
// state, wires periodic jobs (probe / credits / firebase refresh / catalog
// merge), and listens with the full OpenAI + Anthropic surface.
package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"runtime"
	"strings"
	"syscall"
	"time"

	"windsurfapi/internal/auth"
	"windsurfapi/internal/cache"
	"windsurfapi/internal/config"
	"windsurfapi/internal/langserver"
	"windsurfapi/internal/logx"
	"windsurfapi/internal/modelaccess"
	"windsurfapi/internal/proxycfg"
	"windsurfapi/internal/runtimecfg"
	"windsurfapi/internal/server"
	"windsurfapi/internal/stats"
	"windsurfapi/internal/version"
)

const Brand = "WindsurfAPI"

// Version re-exports the central version string so the banner and any
// external callers that reach `main.Version` keep working.
var Version = version.String

func main() {
	cfg := config.Load()
	logx.SetLevel(cfg.LogLevel)

	fmt.Printf(`
  __        ___           _             __ ___    ___
  \ \      / (_)_ __   __| |___ _   _ _ / _|  \  |_ _|
   \ \ /\ / /| | '_ \ / _' / __| | | | '_| |_ / \  | |
    \ V  V / | | | | | (_| \__ \ |_| | | |  _/ _ \ | |
     \_/\_/  |_|_| |_|\__,_|___/\__,_|_| |_|/_/ \_\___|

  %s v%s — Go rewrite
  OpenAI + Anthropic compatible proxy for Windsurf

`, Brand, Version)

	// 1) persistent state
	runtimecfg.Init()
	modelaccess.Init()
	proxycfg.Init()
	stats.Init()
	// Cache backs onto disk (defaults to /tmp/windsurfapi-cache → tmpfs
	// on Debian systemd → spills to swap under pressure). Env CACHE_PATH
	// can redirect to a persistent-disk location if survive-restart matters
	// more than swap-offload. Zero args = built-in defaults (6000 / 2h).
	cache.Init(os.Getenv("CACHE_PATH"), 0, 0)

	// 2) wipe Cascade workspace (Linux only)
	if runtime.GOOS == "linux" {
		_ = os.MkdirAll("/opt/windsurf/data/db", 0o755)
		_ = os.MkdirAll("/tmp/windsurf-workspace", 0o755)
		if entries, err := os.ReadDir("/tmp/windsurf-workspace"); err == nil {
			for _, e := range entries {
				_ = os.RemoveAll("/tmp/windsurf-workspace/" + e.Name())
			}
		}
		// N14 (#96): drop a sentinel so the LS workspace looks like a
		// populated project directory, not an empty one. Cascade's baked-
		// in tool prompts otherwise hit "the workspace appears empty" and
		// start narrating "let me create files at /tmp/windsurf-workspace/…"
		// — leaking the internal path back to the API caller. Marker
		// content is intentionally generic so the model doesn't latch onto
		// it as a project fact.
		_ = os.WriteFile("/tmp/windsurf-workspace/.windsurf-proxy-workspace", []byte(
			"This directory is a synthetic workspace for an API proxy. "+
				"It does not contain user files. Do not narrate paths under this directory "+
				"to the user. Do not create or read files here.\n"), 0o644)
	}

	// 3) LS pool
	lsp := langserver.New()
	lsp.Config(cfg.LSBinaryPath, cfg.CodeiumAPIURL)
	// Periodic TCP health probe on every pool entry — 30s tick, two
	// consecutive misses triggers kill + respawn. Complements the
	// cmd.Wait exit watcher (catches hung-but-alive LS) and the
	// cmd.Wait auto-respawn path (catches silent / adopted deaths).
	lsp.StartWatchdog()
	if _, err := os.Stat(cfg.LSBinaryPath); err == nil {
		// Retry the initial spawn with backoff — a tight systemd restart
		// can leave :42100 in TIME_WAIT, causing LS's own internal
		// manager→worker self-connect to time out on the first try.
		// Successive retries ride out the kernel's port reaper window.
		// Failure here is NOT fatal: the pool's cmd.Wait auto-respawn
		// (added alongside this) will keep retrying every ~2s once main
		// enters its request loop, and any incoming chat request also
		// triggers a fresh Ensure. We log and move on.
		for attempt := 1; attempt <= 3; attempt++ {
			ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
			_, err := lsp.Ensure(ctx, nil)
			cancel()
			if err == nil {
				break
			}
			logx.Warn("Language server spawn attempt %d failed: %s", attempt, err.Error())
			if attempt < 3 {
				time.Sleep(3 * time.Second)
			}
		}
	} else {
		logx.Warn("Language server binary not found at %s", cfg.LSBinaryPath)
		logx.Warn("Install it: download Windsurf Linux tarball and extract language_server_linux_x64")
	}

	// 4) auth pool + env credentials
	pool := auth.New("accounts.json")
	if err := pool.Load(); err != nil {
		logx.Warn("Failed to load accounts: %s", err.Error())
	}
	for _, k := range splitCSV(cfg.CodeiumAPIKey) {
		pool.AddByKey(k, "")
	}
	{
		tokenCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		for _, t := range splitCSV(cfg.CodeiumAuthToken) {
			if _, err := pool.AddByToken(tokenCtx, t, "", proxycfg.Effective("")); err != nil {
				logx.Error("Token auth failed: %s", err.Error())
			}
		}
		cancel()
	}
	if !pool.IsAuthenticated() {
		logx.Warn("No accounts configured. POST /auth/login to add accounts.")
	} else {
		c := pool.Counts()
		logx.Info("Auth pool: %d active, %d error, %d total", c.Active, c.Error, c.Total)
	}

	// 5) HTTP surface
	server.SetStart(time.Now().Unix())
	deps := &server.Deps{Cfg: cfg, Pool: pool, LSP: lsp}

	// 6) periodic tasks
	rootCtx, rootCancel := context.WithCancel(context.Background())
	defer rootCancel()
	resolve := server.ProxyResolver()
	probe := deps.MakeProbeFunc()
	stopPeriodic := pool.Periodic(rootCtx, resolve, probe)
	// Kick off the first fetch immediately, then refresh every 2h so newly
	// released cloud models land without a restart.
	go func() {
		pool.FetchModelCatalog(resolve)
		t := time.NewTicker(2 * time.Hour)
		defer t.Stop()
		for {
			select {
			case <-rootCtx.Done():
				return
			case <-t.C:
				pool.FetchModelCatalog(resolve)
			}
		}
	}()

	srv := &http.Server{
		Addr:              fmt.Sprintf("%s:%d", cfg.BindHost, cfg.Port),
		Handler:           server.Handler(deps),
		ReadHeaderTimeout: 10 * time.Second,
		// IdleTimeout drops keep-alive connections that stay silent — caps
		// the exposure of a slowloris-style DoS where an attacker holds
		// thousands of idle TCP sessions against our open FD budget.
		IdleTimeout: 120 * time.Second,
		// MaxHeaderBytes trims runaway header attacks below the default 1
		// MB which can combine with many concurrent requests to pressure
		// RAM on the 1 GB VM.
		MaxHeaderBytes: 64 << 10,
		// Intentionally no ReadTimeout / WriteTimeout here: /v1/chat/completions
		// and /v1/messages are long-running SSE streams whose total duration
		// exceeds any reasonable Read/Write cap. Body-read duration for
		// non-stream requests is bounded by the 8 MB MaxBytesReader + the
		// per-handler request context on the chat paths.
	}

	logx.Info("Server on http://%s:%d", cfg.BindHost, cfg.Port)
	logx.Info("  POST /v1/chat/completions  (OpenAI compatible)")
	logx.Info("  POST /v1/messages          (Anthropic compatible)")
	logx.Info("  POST /v1/responses         (Codex CLI compatible)")
	logx.Info("  GET  /v1/models")
	logx.Info("  POST /auth/login           (api_key / token / email+password)")
	logx.Info("  GET  /auth/accounts, DELETE /auth/accounts/:id")
	logx.Info("  GET  /auth/status, /health")
	emitNoAuthWarnings(cfg)

	// 7) graceful shutdown
	sigCtx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	serverErr := make(chan error, 1)
	go func() {
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			serverErr <- err
		}
	}()

	select {
	case err := <-serverErr:
		logx.Error("HTTP server: %s", err.Error())
	case <-sigCtx.Done():
		logx.Info("Shutting down — draining in-flight requests (up to 30s)...")
	}
	stopPeriodic()
	shutCtx, shutCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer shutCancel()
	_ = srv.Shutdown(shutCtx)
	lsp.StopAll()
	logx.Info("HTTP server closed, stopping language server")
}

// N4: emit a fat warning banner when the proxy is bound publicly with no
// authentication configured. Only fires on non-loopback binds. Operators
// who set ALLOW_OPEN=1 to deliberately skip the loopback gate still see
// this so it's clear what they signed up for.
func emitNoAuthWarnings(cfg *config.Config) {
	host := strings.ToLower(strings.TrimSpace(cfg.BindHost))
	host = strings.TrimPrefix(strings.TrimSuffix(host, "]"), "[")
	if host == "127.0.0.1" || host == "localhost" || host == "::1" || strings.HasPrefix(host, "::ffff:127.") {
		return
	}
	apiOpen := cfg.APIKey == ""
	dashOpen := cfg.DashboardPassword == ""
	if !apiOpen && !dashOpen {
		return
	}
	for _, line := range []string{
		"+------------------------------------------------------------------+",
		"| WARNING: AUTHENTICATION IS NOT CONFIGURED                        |",
		"| 警告：当前服务未配置访问认证                                      |",
		"|                                                                  |",
		"| This server is listening beyond localhost. Set API_KEY for       |",
		"| REST APIs and DASHBOARD_PASSWORD for the dashboard write surface |",
		"| before exposing the proxy publicly.                              |",
		"| 服务正在非本机地址监听。公网/内网暴露前请配置 API_KEY 与          |",
		"| DASHBOARD_PASSWORD 两个凭据。                                     |",
		"+------------------------------------------------------------------+",
	} {
		logx.Warn(line)
	}
}

func splitCSV(s string) []string {
	if s == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}
