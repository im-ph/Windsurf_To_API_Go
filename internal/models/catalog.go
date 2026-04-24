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
		// Suppress BYOK (Bring-Your-Own-Key) variants. Upstream ships
		// `MODEL_..._BYOK` / `model-...-byok` entries as billing-layer
		// aliases — they route to the same underlying model as the non-
		// BYOK uid but let Windsurf account quota against the caller's
		// own provider key instead of Windsurf's pool. For this reverse
		// proxy the provider key IS Windsurf's pooled key, so there's
		// no routing advantage, and exposing 4 near-duplicate entries
		// in the model picker confuses end-users. Normalize underscore
		// → hyphen before suffix-matching because upstream emits both
		// `MODEL_CLAUDE_4_OPUS_BYOK` and `model-claude-4-opus-byok`.
		// Filtered here rather than via model-access blocklist so the
		// filter survives a blocklist wipe.
		normalized := strings.ToLower(strings.ReplaceAll(m.ModelUID, "_", "-"))
		if strings.HasSuffix(normalized, "-byok") {
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

	// Official dated-name aliases. OpenAI + Anthropic SDKs and the Claude Code
	// CLI send dated model names like "claude-opus-4-5-20251101" or
	// "gpt-4o-2024-11-20"; without these the router returns 404 "model not
	// found" even though the underlying short-name is present. Also cover the
	// "-latest" convention Anthropic uses for always-pointing-to-newest and
	// Claude's "-0" suffix for the first released variant in a line.
	dated := map[string]string{
		// Claude Opus 4.7 — cloud surfaces 5 effort tiers (low/medium/high/xhigh/max);
		// "claude-opus-4-7" bare maps to the default medium variant so SDKs
		// using the short name still land on something reasonable.
		"claude-opus-4-7":        "claude-opus-4-7-medium",
		"claude-opus-4.7":        "claude-opus-4-7-medium",
		"claude-opus-4-7-latest": "claude-opus-4-7-max",

		// Claude 4.6 dated names (Anthropic SDK convention).
		"claude-sonnet-4-6-latest":    "claude-sonnet-4.6",
		"claude-opus-4-6-latest":      "claude-opus-4.6",

		// Claude 4.5 dated rollouts.
		"claude-opus-4-5-20251101":         "claude-4.5-opus",
		"claude-sonnet-4-5-20251101":       "claude-4.5-sonnet",
		"claude-opus-4-5-latest":           "claude-4.5-opus",
		"claude-sonnet-4-5-latest":         "claude-4.5-sonnet",

		// Claude 3.7.
		"claude-3-7-sonnet-20250219": "claude-3.7-sonnet",
		"claude-3-7-sonnet-latest":   "claude-3.7-sonnet",

		// Claude 3.5.
		"claude-3-5-sonnet-20241022": "claude-3.5-sonnet",
		"claude-3-5-sonnet-20240620": "claude-3.5-sonnet",
		"claude-3-5-sonnet-latest":   "claude-3.5-sonnet",

		// GPT-4o dated snapshots — OpenAI SDKs pin to these, not short names.
		"gpt-4o-2024-05-13":       "gpt-4o",
		"gpt-4o-2024-08-06":       "gpt-4o",
		"gpt-4o-2024-11-20":       "gpt-4o",
		"gpt-4o-mini-2024-07-18":  "gpt-4o-mini",
		"gpt-4.1-2025-04-14":      "gpt-4.1",
		"gpt-4.1-mini-2025-04-14": "gpt-4.1-mini",
		"gpt-4.1-nano-2025-04-14": "gpt-4.1-nano",

		// GPT-5 + codex dated names.
		"gpt-5-2025-08-07":       "gpt-5",
		"gpt-5-mini-2025-08-07":  "gpt-5-mini",
		"gpt-5-codex-2025-09-17": "gpt-5-codex",

		// Gemini.
		"gemini-2.5-pro-latest":   "gemini-2.5-pro",
		"gemini-2.5-flash-latest": "gemini-2.5-flash",

		// O-series.
		"o3-2025-04-16":     "o3",
		"o3-mini-2025-01-31": "o3-mini",
		"o4-mini-2025-04-16": "o4-mini",
	}
	for k, v := range dated {
		// Skip entries whose target isn't in the catalog (e.g. claude-opus-4-7
		// only lands after a cloud fetch). Without this guard, Resolve() would
		// return a dangling key and the chat handler later can't find the Info.
		if _, ok := catalog[v]; !ok {
			continue
		}
		lookup[k] = v
		lookup[strings.ToLower(k)] = v
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
