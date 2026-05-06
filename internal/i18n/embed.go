// Package i18n holds the canonical locale JSON files for the dashboard
// SPA. The same files exist at go/web/src/i18n/{zh-CN,en}.json so the
// Vue bundle has a build-time fallback; this package is the runtime
// source of truth that GET /dashboard/api/i18n/:locale serves.
//
// Edit either copy and the SPA picks up the change on next reload — the
// composable's `loadFromBackend` overwrites the bundled fallback. Keep
// the two copies in sync; in particular, after editing the source-of-
// truth JSON here, copy it to web/src/i18n/ before running `pnpm build`
// (the Vue build only sees web/src/i18n/).
package i18n

import (
	"embed"
	"errors"
)

//go:embed *.json
var fs embed.FS

// ErrLocaleNotFound is returned when a requested locale does not exist.
// dashapi turns this into a 404 (not 500) so operators can probe the
// locale list with a script.
var ErrLocaleNotFound = errors.New("locale not found")

// Locale returns the JSON bytes for `code` (e.g. "zh-CN", "en"). The
// returned slice is read-only — callers must not modify it.
func Locale(code string) ([]byte, error) {
	if code == "" {
		return nil, ErrLocaleNotFound
	}
	if !isSafeLocaleCode(code) {
		return nil, ErrLocaleNotFound
	}
	data, err := fs.ReadFile(code + ".json")
	if err != nil {
		return nil, ErrLocaleNotFound
	}
	return data, nil
}

// AvailableLocales returns the list of bundled locale codes. Used by
// the dashboard API to populate the language switcher.
func AvailableLocales() []string {
	entries, err := fs.ReadDir(".")
	if err != nil {
		return nil
	}
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		name := e.Name()
		if !endsWith(name, ".json") {
			continue
		}
		code := name[:len(name)-len(".json")]
		out = append(out, code)
	}
	return out
}

// isSafeLocaleCode permits letters, digits, and hyphens only — same
// regex shape as the existing /dashboard/i18n/*.json route in server.go.
// Path traversal attempts (../etc/passwd) bounce here.
func isSafeLocaleCode(s string) bool {
	if len(s) == 0 || len(s) > 16 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '-' {
			continue
		}
		return false
	}
	return true
}

func endsWith(s, suffix string) bool {
	return len(s) >= len(suffix) && s[len(s)-len(suffix):] == suffix
}
