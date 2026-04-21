// Package proxycfg holds the global + per-account proxy config, persisted to
// proxy.json. Mirrors src/dashboard/proxy-config.js. Shared between the
// langserver pool (spawn env) and the cloud/REST helpers (CONNECT tunnel).
package proxycfg

import (
	"encoding/json"
	"os"
	"sync"

	"windsurfapi/internal/langserver"
)

type file struct {
	Global     *langserver.Proxy            `json:"global,omitempty"`
	PerAccount map[string]*langserver.Proxy `json:"perAccount"`
}

var (
	mu    sync.RWMutex
	state = file{PerAccount: map[string]*langserver.Proxy{}}
	path  = "proxy.json"
)

// Init loads proxy.json.
func Init() {
	data, err := os.ReadFile(path)
	if err != nil {
		return
	}
	var loaded file
	if err := json.Unmarshal(data, &loaded); err == nil {
		mu.Lock()
		state = loaded
		if state.PerAccount == nil {
			state.PerAccount = map[string]*langserver.Proxy{}
		}
		mu.Unlock()
	}
}

func save() {
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return
	}
	// Atomic write: write to a temp file then rename — consistent with
	// auth.pool.saveLocked() so a mid-write crash never corrupts the config.
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return
	}
	_ = os.Rename(tmp, path)
}

// Get returns the full proxy snapshot for the dashboard.
type Snapshot struct {
	Global     *langserver.Proxy            `json:"global"`
	PerAccount map[string]*langserver.Proxy `json:"perAccount"`
}

func Get() Snapshot {
	mu.RLock()
	defer mu.RUnlock()
	cp := Snapshot{Global: state.Global, PerAccount: map[string]*langserver.Proxy{}}
	for k, v := range state.PerAccount {
		cp.PerAccount[k] = v
	}
	return cp
}

// SetGlobal updates (or clears when p == nil || p.Host == "") the global proxy.
func SetGlobal(p *langserver.Proxy) {
	mu.Lock()
	if p != nil && p.Host != "" {
		if p.Port == 0 {
			p.Port = 8080
		}
		state.Global = p
	} else {
		state.Global = nil
	}
	save()
	mu.Unlock()
}

// SetAccount pins a proxy to one account (p==nil removes the entry).
func SetAccount(accountID string, p *langserver.Proxy) {
	mu.Lock()
	if p != nil && p.Host != "" {
		if p.Port == 0 {
			p.Port = 8080
		}
		state.PerAccount[accountID] = p
	} else {
		delete(state.PerAccount, accountID)
	}
	save()
	mu.Unlock()
}

// Remove drops the global or a single account's proxy.
func Remove(scope, accountID string) {
	mu.Lock()
	switch scope {
	case "global":
		state.Global = nil
	case "account":
		delete(state.PerAccount, accountID)
	}
	save()
	mu.Unlock()
}

// Effective returns the proxy to use for a given accountID (per-account takes
// priority over global). nil = direct.
func Effective(accountID string) *langserver.Proxy {
	mu.RLock()
	defer mu.RUnlock()
	if accountID != "" {
		if p, ok := state.PerAccount[accountID]; ok && p != nil {
			return p
		}
	}
	return state.Global
}
