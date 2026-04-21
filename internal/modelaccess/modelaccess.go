// Package modelaccess is the global model allow/block list, persisted to
// model-access.json. Mirrors src/dashboard/model-access.js.
package modelaccess

import (
	"encoding/json"
	"os"
	"sync"

	"windsurfapi/internal/atomicfile"
)

const (
	ModeAll       = "all"
	ModeAllowlist = "allowlist"
	ModeBlocklist = "blocklist"
)

type Config struct {
	Mode string   `json:"mode"`
	List []string `json:"list"`
}

var (
	mu   sync.RWMutex
	cfg  = Config{Mode: ModeAll, List: []string{}}
	path = "model-access.json"
)

// Init reads model-access.json.
func Init() {
	data, err := os.ReadFile(path)
	if err != nil {
		return
	}
	var loaded Config
	if err := json.Unmarshal(data, &loaded); err == nil {
		mu.Lock()
		cfg = loaded
		if cfg.List == nil {
			cfg.List = []string{}
		}
		mu.Unlock()
	}
}

// saveMu serialises disk writes so parallel save() calls land in a
// deterministic order without holding the main mu across I/O.
var saveMu sync.Mutex

func save() {
	data, _ := json.MarshalIndent(cfg, "", "  ")
	// Disk flush runs async — hot-path `Check()` readers only contend on mu.
	go func(payload []byte) {
		saveMu.Lock()
		defer saveMu.Unlock()
		_ = atomicfile.Write(path, payload)
	}(data)
}

// Get returns a copy of the current config. The list is always non-nil so
// `encoding/json` renders it as `[]` — nil slices marshal to `null` which
// breaks frontend consumers that call `.length` on the decoded value.
func Get() Config {
	mu.RLock()
	defer mu.RUnlock()
	out := cfg
	list := make([]string, 0, len(cfg.List))
	list = append(list, cfg.List...)
	out.List = list
	return out
}

// SetMode updates the mode (ignored if mode is invalid).
func SetMode(mode string) {
	if mode != ModeAll && mode != ModeAllowlist && mode != ModeBlocklist {
		return
	}
	mu.Lock()
	cfg.Mode = mode
	save()
	mu.Unlock()
}

// SetList replaces the list.
func SetList(list []string) {
	mu.Lock()
	cfg.List = append([]string(nil), list...)
	save()
	mu.Unlock()
}

// Add appends modelID if it's not already present.
func Add(modelID string) {
	mu.Lock()
	for _, m := range cfg.List {
		if m == modelID {
			mu.Unlock()
			return
		}
	}
	cfg.List = append(cfg.List, modelID)
	save()
	mu.Unlock()
}

// Remove drops modelID.
func Remove(modelID string) {
	mu.Lock()
	keep := cfg.List[:0]
	for _, m := range cfg.List {
		if m != modelID {
			keep = append(keep, m)
		}
	}
	cfg.List = keep
	save()
	mu.Unlock()
}

// Check reports whether a model is allowed by the current policy. Returns a
// user-facing 简体中文 reason on denial to match the JS response.
type Decision struct {
	Allowed bool
	Reason  string
}

func Check(modelID string) Decision {
	mu.RLock()
	defer mu.RUnlock()
	switch cfg.Mode {
	case ModeAllowlist:
		for _, m := range cfg.List {
			if m == modelID {
				return Decision{Allowed: true}
			}
		}
		return Decision{Reason: "模型 " + modelID + " 不在允许清单中"}
	case ModeBlocklist:
		for _, m := range cfg.List {
			if m == modelID {
				return Decision{Reason: "模型 " + modelID + " 已被封锁"}
			}
		}
		return Decision{Allowed: true}
	default:
		return Decision{Allowed: true}
	}
}
