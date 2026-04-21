// Package models is the 107-model catalog plus tier access table. The catalog
// is a direct port of src/models.js — enum values and modelUids match the
// JS original exactly so routing decisions are identical.
package models

import (
	"strings"
	"sync"
	"time"
)

// Info describes one model — routing rules:
//   - ModelUID != ""        → Cascade flow
//   - ModelUID == "" && Enum>0 → legacy RawGetChatMessage
type Info struct {
	Name     string
	Provider string
	Enum     uint64
	ModelUID string
	Credit   float64
}

var (
	mu      sync.RWMutex
	catalog = map[string]Info{}
	lookup  = map[string]string{} // various ids → canonical key
)

func init() {
	for k, v := range seed {
		catalog[k] = v
	}
	rebuildLookup()
}

// Resolve turns a user-supplied id into the canonical catalog key. Returns
// the input unchanged if no mapping exists.
func Resolve(id string) string {
	if id == "" {
		return ""
	}
	mu.RLock()
	defer mu.RUnlock()
	if k, ok := lookup[id]; ok {
		return k
	}
	if k, ok := lookup[strings.ToLower(id)]; ok {
		return k
	}
	return id
}

// Get returns the catalog entry or nil.
func Get(key string) *Info {
	mu.RLock()
	defer mu.RUnlock()
	if v, ok := catalog[key]; ok {
		return &v
	}
	return nil
}

// OpenAIModel is the shape returned by /v1/models.
type OpenAIModel struct {
	ID          string `json:"id"`
	Object      string `json:"object"`
	Created     int64  `json:"created"`
	OwnedBy     string `json:"owned_by"`
	WindsurfID  string `json:"_windsurf_id"`
}

// ListOpenAI returns the full catalog in OpenAI /v1/models shape.
func ListOpenAI() []OpenAIModel {
	mu.RLock()
	defer mu.RUnlock()
	out := make([]OpenAIModel, 0, len(catalog))
	ts := time.Now().Unix()
	for k, v := range catalog {
		out = append(out, OpenAIModel{
			ID: v.Name, Object: "model", Created: ts, OwnedBy: v.Provider, WindsurfID: k,
		})
	}
	return out
}

// AllKeys returns every catalog key.
func AllKeys() []string {
	mu.RLock()
	defer mu.RUnlock()
	out := make([]string, 0, len(catalog))
	for k := range catalog {
		out = append(out, k)
	}
	return out
}

// MergeCloud merges GetCascadeModelConfigs results — adds NEW uids only so
// hand-curated enum values in the seed are never overwritten.
func MergeCloud(entries []CloudModel) int {
	mu.Lock()
	defer mu.Unlock()
	providerMap := map[string]string{
		"MODEL_PROVIDER_ANTHROPIC": "anthropic",
		"MODEL_PROVIDER_OPENAI":    "openai",
		"MODEL_PROVIDER_GOOGLE":    "google",
		"MODEL_PROVIDER_DEEPSEEK":  "deepseek",
		"MODEL_PROVIDER_XAI":       "xai",
		"MODEL_PROVIDER_WINDSURF":  "windsurf",
		"MODEL_PROVIDER_MOONSHOT":  "moonshot",
	}
	added := 0
	for _, m := range entries {
		if m.ModelUID == "" {
			continue
		}
		if _, ok := lookup[m.ModelUID]; ok {
			continue
		}
		if _, ok := lookup[strings.ToLower(m.ModelUID)]; ok {
			continue
		}
		key := strings.ToLower(strings.ReplaceAll(m.ModelUID, "_", "-"))
		if _, ok := catalog[key]; ok {
			continue
		}
		provider := providerMap[m.Provider]
		if provider == "" {
			provider = strings.ToLower(strings.TrimPrefix(m.Provider, "MODEL_PROVIDER_"))
		}
		catalog[key] = Info{
			Name:     key,
			Provider: provider,
			ModelUID: m.ModelUID,
			Credit:   m.CreditMultiplier,
		}
		lookup[key] = key
		lookup[m.ModelUID] = key
		lookup[strings.ToLower(m.ModelUID)] = key
		added++
	}
	return added
}

// CloudModel is the subset of ClientModelConfig we consume.
type CloudModel struct {
	ModelUID         string  `json:"modelUid"`
	Provider         string  `json:"provider"`
	CreditMultiplier float64 `json:"creditMultiplier"`
	Label            string  `json:"label"`
}

func rebuildLookup() {
	lookup = map[string]string{}
	for k, v := range catalog {
		lookup[k] = k
		lookup[strings.ToLower(k)] = k
		lookup[v.Name] = k
		lookup[strings.ToLower(v.Name)] = k
		if v.ModelUID != "" {
			lookup[v.ModelUID] = k
			lookup[strings.ToLower(v.ModelUID)] = k
		}
	}
	// Legacy aliases (copied from src/models.js)
	legacy := map[string]string{
		"claude-sonnet-4-6-thinking":        "claude-sonnet-4.6-thinking",
		"claude-opus-4-6-thinking":          "claude-opus-4.6-thinking",
		"claude-sonnet-4-6":                 "claude-sonnet-4.6",
		"claude-opus-4-6":                   "claude-opus-4.6",
		"MODEL_CLAUDE_4_5_SONNET":           "claude-4.5-sonnet",
		"MODEL_CLAUDE_4_5_SONNET_THINKING":  "claude-4.5-sonnet-thinking",
		"claude-sonnet-4-6-1m":              "claude-sonnet-4.6-1m",
		"claude-sonnet-4-6-thinking-1m":     "claude-sonnet-4.6-thinking-1m",
		"gpt-5-4-low":                       "gpt-5.4-low",
		"gpt-5-4-medium":                    "gpt-5.4-medium",
		"gpt-5-4-xhigh":                     "gpt-5.4-xhigh",
		"gpt-5-4-mini-low":                  "gpt-5.4-mini-low",
		"gpt-5-4-mini-medium":               "gpt-5.4-mini-medium",
		"gpt-5-4-mini-high":                 "gpt-5.4-mini-high",
		"gpt-5-4-mini-xhigh":                "gpt-5.4-mini-xhigh",
	}
	for k, v := range legacy {
		lookup[k] = v
	}
}

// ─── Tier access ───────────────────────────────────────────

var freeTier = []string{"gpt-4o-mini", "gemini-2.5-flash"}

// TierModels returns the list of catalog keys a given tier is entitled to.
func TierModels(tier string) []string {
	switch tier {
	case "pro":
		return AllKeys()
	case "expired":
		return nil
	default: // free / unknown
		out := make([]string, 0, len(freeTier))
		for _, m := range freeTier {
			out = append(out, m)
		}
		return out
	}
}

// IsTierAllowed reports whether a tier can call modelKey.
func IsTierAllowed(tier, modelKey string) bool {
	switch tier {
	case "pro":
		_, ok := catalog[modelKey]
		return ok
	case "expired":
		return false
	default:
		for _, m := range freeTier {
			if m == modelKey {
				return true
			}
		}
		return false
	}
}
