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
)

const (
	Brand   = "WindsurfAPI bydwgx1337"
	Version = "1.2.0-go"
)

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
	}

	// 3) LS pool
	lsp := langserver.New()
	lsp.Config(cfg.LSBinaryPath, cfg.CodeiumAPIURL)
	if _, err := os.Stat(cfg.LSBinaryPath); err == nil {
		ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
		if _, err := lsp.Ensure(ctx, nil); err != nil {
			logx.Error("Language server failed to start: %s", err.Error())
		}
		cancel()
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
	}

	logx.Info("Server on http://%s:%d", cfg.BindHost, cfg.Port)
	logx.Info("  POST /v1/chat/completions  (OpenAI compatible)")
	logx.Info("  POST /v1/messages          (Anthropic compatible)")
	logx.Info("  GET  /v1/models")
	logx.Info("  POST /auth/login           (api_key / token / email+password)")
	logx.Info("  GET  /auth/accounts, DELETE /auth/accounts/:id")
	logx.Info("  GET  /auth/status, /health")

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
