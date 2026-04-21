// Package config loads environment + .env into a typed Config struct.
// Matches the surface of src/config.js in the Node.js original.
package config

import (
	"bufio"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

type Config struct {
	Port     int
	APIKey   string

	CodeiumAuthToken string
	CodeiumAPIKey    string
	CodeiumEmail     string
	CodeiumPassword  string
	CodeiumAPIURL    string

	DefaultModel string
	MaxTokens    int
	LogLevel     string

	LSBinaryPath string
	LSPort       int

	DashboardPassword string

	// CORSAllowedOrigins is the comma-separated list of origins that may
	// call the API cross-origin. Empty = no CORS headers emitted (browsers
	// block cross-origin calls entirely). `*` = legacy wildcard behaviour.
	// Anything else = strict echo match against the Origin request header.
	CORSAllowedOrigins string
}

// Load reads .env (if present) then process environment, and returns a Config.
// Environment variables already set in the process take precedence over .env —
// matches the JS loader (src/config.js:22).
func Load() *Config {
	loadDotEnv(".env")

	c := &Config{
		Port:              envInt("PORT", 3003),
		APIKey:            os.Getenv("API_KEY"),
		CodeiumAuthToken:  os.Getenv("CODEIUM_AUTH_TOKEN"),
		CodeiumAPIKey:     os.Getenv("CODEIUM_API_KEY"),
		CodeiumEmail:      os.Getenv("CODEIUM_EMAIL"),
		CodeiumPassword:   os.Getenv("CODEIUM_PASSWORD"),
		CodeiumAPIURL:     envStr("CODEIUM_API_URL", "https://server.self-serve.windsurf.com"),
		DefaultModel:      envStr("DEFAULT_MODEL", "claude-4.5-sonnet-thinking"),
		MaxTokens:         envInt("MAX_TOKENS", 8192),
		LogLevel:          envStr("LOG_LEVEL", "info"),
		LSBinaryPath:      resolveLSBinary(),
		LSPort:            envInt("LS_PORT", 42100),
		DashboardPassword:  os.Getenv("DASHBOARD_PASSWORD"),
		CORSAllowedOrigins: os.Getenv("CORS_ALLOWED_ORIGINS"),
	}
	return c
}

func loadDotEnv(path string) {
	f, err := os.Open(path)
	if err != nil {
		return
	}
	defer f.Close()

	s := bufio.NewScanner(f)
	for s.Scan() {
		line := strings.TrimSpace(s.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		eq := strings.IndexByte(line, '=')
		if eq <= 0 {
			continue
		}
		k := strings.TrimSpace(line[:eq])
		v := strings.TrimSpace(line[eq+1:])
		if len(v) >= 2 && (v[0] == '"' && v[len(v)-1] == '"' || v[0] == '\'' && v[len(v)-1] == '\'') {
			v = v[1 : len(v)-1]
		}
		if _, set := os.LookupEnv(k); !set {
			_ = os.Setenv(k, v)
		}
	}
}

func envStr(k, def string) string {
	if v, ok := os.LookupEnv(k); ok && v != "" {
		return v
	}
	return def
}

func envInt(k string, def int) int {
	if v, ok := os.LookupEnv(k); ok && v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}

// resolveLSBinary picks the LS binary to spawn, in priority order:
//
//  1. $LS_BINARY_PATH              — explicit env override
//  2. <exe dir>/bin/language_server_linux_x64  — sibling layout (scp-friendly)
//  3. /opt/windsurf/language_server_linux_x64  — legacy absolute path
//
// Any of these can still be "not present" — main.go logs a warning and keeps
// the HTTP server + dashboard up for non-chat paths.
func resolveLSBinary() string {
	if v, ok := os.LookupEnv("LS_BINARY_PATH"); ok && v != "" {
		return v
	}
	if exe, err := os.Executable(); err == nil {
		candidate := filepath.Join(filepath.Dir(exe), "bin", "language_server_linux_x64")
		if _, err := os.Stat(candidate); err == nil {
			return candidate
		}
	}
	// Relative fallback when running via `go run` from the module root.
	if _, err := os.Stat("bin/language_server_linux_x64"); err == nil {
		abs, _ := filepath.Abs("bin/language_server_linux_x64")
		return abs
	}
	return "/opt/windsurf/language_server_linux_x64"
}
