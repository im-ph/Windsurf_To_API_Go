package models

import "strings"

// capabilityScore maps a catalog key → a hand-curated capability score on a
// 0-100 scale. The score is meant as a coarse "which model is smarter" signal
// for the dashboard UI — it is NOT a production routing heuristic.
//
// Anchoring philosophy: flagship frontier models at the time of writing land
// at 90+, strong mid-tier ~80, practical workhorses ~70, small/efficient
// ~60, cheap-and-fast ~50. Thinking/High variants add a few points over the
// base; Low/Minimal/Mini subtract. When the same family ships in multiple
// effort tiers we keep their ordering internally consistent.
var capabilityScore = map[string]int{
	// ── Claude ──
	"claude-3.5-sonnet":             74,
	"claude-3.7-sonnet":             76,
	"claude-3.7-sonnet-thinking":    79,
	"claude-4-sonnet":               80,
	"claude-4-sonnet-thinking":      82,
	"claude-4-opus":                 83,
	"claude-4-opus-thinking":        85,
	"claude-4.1-opus":               85,
	"claude-4.1-opus-thinking":      87,
	"claude-4.5-haiku":              68,
	"claude-4.5-sonnet":             87,
	"claude-4.5-sonnet-thinking":    89,
	"claude-4.5-opus":               91,
	"claude-4.5-opus-thinking":      93,
	"claude-sonnet-4.6":             90,
	"claude-sonnet-4.6-thinking":    92,
	"claude-sonnet-4.6-1m":          90,
	"claude-sonnet-4.6-thinking-1m": 92,
	"claude-opus-4.6":               95,
	"claude-opus-4.6-thinking":      97,
	// Claude 4.7 — cloud catalog uses dash-only keys ("4-7" not "4.7")
	// because the Windsurf modelUid naming convention replaces "." with "-".
	"claude-opus-4-7-low":    93,
	"claude-opus-4-7-medium": 96,
	"claude-opus-4-7-high":   97,
	"claude-opus-4-7-xhigh":  98,
	"claude-opus-4-7-max":    99,
	// BYOK ("bring your own key") mirrors the underlying model — Windsurf
	// just meters it differently, the capability is identical.
	"model-claude-4-opus-byok":            83,
	"model-claude-4-opus-thinking-byok":   85,
	"model-claude-4-sonnet-byok":          80,
	"model-claude-4-sonnet-thinking-byok": 82,

	// ── GPT ──
	"gpt-4o":                    65,
	"gpt-4o-mini":               52,
	"gpt-4.1":                   70,
	"gpt-4.1-mini":              58,
	"gpt-4.1-nano":              48,
	"gpt-5":                     76,
	"gpt-5-medium":              78,
	"gpt-5-high":                82,
	"gpt-5-mini":                58,
	"gpt-5-codex":               74,
	"gpt-5.1":                   78,
	"gpt-5.1-low":               72,
	"gpt-5.1-medium":            80,
	"gpt-5.1-high":              85,
	"gpt-5.1-fast":              77,
	"gpt-5.1-low-fast":          71,
	"gpt-5.1-medium-fast":       79,
	"gpt-5.1-high-fast":         84,
	"gpt-5.1-codex-low":         72,
	"gpt-5.1-codex-medium":      78,
	"gpt-5.1-codex-mini-low":    65,
	"gpt-5.1-codex-mini":        68,
	"gpt-5.1-codex-max-low":     80,
	"gpt-5.1-codex-max-medium":  85,
	"gpt-5.1-codex-max-high":    88,
	"gpt-5.2":                   86,
	"gpt-5.2-none":              78,
	"gpt-5.2-low":               80,
	"gpt-5.2-high":              91,
	"gpt-5.2-xhigh":             94,
	"gpt-5.2-none-fast":         77,
	"gpt-5.2-low-fast":          79,
	"gpt-5.2-medium-fast":       85,
	"gpt-5.2-high-fast":         90,
	"gpt-5.2-xhigh-fast":        93,
	"gpt-5.2-codex-low":         78,
	"gpt-5.2-codex-medium":      83,
	"gpt-5.2-codex-high":        87,
	"gpt-5.2-codex-xhigh":       91,
	"gpt-5.2-codex-low-fast":    77,
	"gpt-5.2-codex-medium-fast": 82,
	"gpt-5.2-codex-high-fast":   86,
	"gpt-5.2-codex-xhigh-fast":  90,
	"gpt-5.3-codex":             86,
	"gpt-5.4-low":               80,
	"gpt-5.4-medium":            85,
	"gpt-5.4-xhigh":             93,
	"gpt-5.4-mini-low":          72,
	"gpt-5.4-mini-medium":       76,
	"gpt-5.4-mini-high":         82,
	"gpt-5.4-mini-xhigh":        87,
	"gpt-oss-120b":              60,

	// ── O-Series ──
	"o3-mini": 68,
	"o3":      76,
	"o3-high": 80,
	"o3-pro":  85,
	"o4-mini": 72,

	// ── Gemini ──
	"gemini-2.5-pro":           76,
	"gemini-2.5-flash":         64,
	"gemini-3.0-pro":           88,
	"gemini-3.0-flash-minimal": 58,
	"gemini-3.0-flash-low":     68,
	"gemini-3.0-flash":         74,
	"gemini-3.0-flash-high":    78,
	"gemini-3.1-pro-low":       89,
	"gemini-3.1-pro-high":      93,

	// ── DeepSeek ──
	"deepseek-v3":   66,
	"deepseek-v3-2": 72,
	"deepseek-r1":   76,

	// ── Grok ──
	"grok-3":               70,
	"grok-3-mini":          55,
	"grok-3-mini-thinking": 60,
	"grok-code-fast-1":     72,

	// ── Qwen ──
	"qwen-3":       70,
	"qwen-3-coder": 74,

	// ── Kimi ──
	"kimi-k2":   73,
	"kimi-k2.5": 76,

	// ── GLM ──
	"glm-4.7": 62,
	"glm-5":   74,
	"glm-5.1": 77,

	// ── MiniMax ──
	"minimax-m2.5": 72,

	// ── Windsurf SWE ──
	"swe-1.5":      64,
	"swe-1.5-fast": 62,
	"swe-1.6":      68,
	"swe-1.6-fast": 66,

	// ── Arena ──
	"arena-fast":  62,
	"arena-smart": 72,
}

// Score returns the capability score for a catalog key. Uses the hand-curated
// table when possible, and falls back to a rules-based inference (family
// base + suffix adjusters) so cloud-merged models added after this table was
// last updated still show a reasonable number instead of 0.
func Score(key string) int {
	if v, ok := capabilityScore[key]; ok {
		return v
	}
	return inferScore(key)
}

// familyBase is the "middle of the range" score for a family, used only when
// the exact key isn't in capabilityScore. Numbers are deliberately a few
// points below the curated flagships so a surprise cloud model doesn't leap
// ahead of the known top variant.
var familyBase = map[string]int{
	"Claude":       82,
	"GPT":          75,
	"GPT OSS":      58,
	"Gemini":       72,
	"DeepSeek":     68,
	"Grok":         68,
	"Qwen":         70,
	"Kimi":         72,
	"GLM":          72,
	"MiniMax":      70,
	"Windsurf SWE": 64,
	"Arena":        64,
}

// inferScore derives a capability estimate from the key's family and suffix
// conventions. Returns 0 for unknown families so unmapped models still stand
// out in the UI.
func inferScore(key string) int {
	k := strings.ToLower(key)
	base := familyBase[Family(key)]
	if base == 0 {
		return 0
	}

	// Family-specific version / variant nudges.
	switch {
	case strings.Contains(k, "claude"):
		switch {
		case strings.Contains(k, "opus"):
			base += 8
		case strings.Contains(k, "haiku"):
			base -= 12
		}
		switch {
		case strings.Contains(k, "4-7"), strings.Contains(k, "4.7"):
			base += 8
		case strings.Contains(k, "4-6"), strings.Contains(k, "4.6"):
			base += 5
		case strings.Contains(k, "4-5"), strings.Contains(k, "4.5"):
			base += 2
		case strings.Contains(k, "4-1"), strings.Contains(k, "4.1"):
			base -= 1
		case strings.Contains(k, "3-7"), strings.Contains(k, "3.7"):
			base -= 6
		case strings.Contains(k, "3-5"), strings.Contains(k, "3.5"):
			base -= 8
		}
	case strings.Contains(k, "gpt-5"), strings.Contains(k, "gpt5"):
		switch {
		case strings.Contains(k, "gpt-5-4"), strings.Contains(k, "gpt-5.4"):
			base += 10
		case strings.Contains(k, "gpt-5-3"), strings.Contains(k, "gpt-5.3"):
			base += 8
		case strings.Contains(k, "gpt-5-2"), strings.Contains(k, "gpt-5.2"):
			base += 6
		case strings.Contains(k, "gpt-5-1"), strings.Contains(k, "gpt-5.1"):
			base += 3
		}
	case strings.Contains(k, "gemini-3") || strings.Contains(k, "gemini-3-"):
		base += 10
	case strings.Contains(k, "deepseek-r"):
		base += 6
	case strings.Contains(k, "deepseek-v3"):
		base += 2
	}

	// Generic effort-tier adjusters. Applied after family nudges so the
	// relative ordering within a family stays intact.
	switch {
	case strings.Contains(k, "-max"):
		base += 6
	case strings.Contains(k, "-xhigh"):
		base += 4
	case strings.Contains(k, "-high"):
		base += 2
	case strings.Contains(k, "-low"):
		base -= 3
	case strings.Contains(k, "-minimal"), strings.HasSuffix(k, "-none"):
		base -= 8
	}
	if strings.Contains(k, "thinking") {
		base += 2
	}
	if strings.Contains(k, "mini") && !strings.Contains(k, "minimal") {
		base -= 10
	}
	if strings.Contains(k, "nano") {
		base -= 18
	}
	// -fast / -byok are routing tiers, not capability changes.

	if base > 100 {
		base = 100
	}
	if base < 10 {
		base = 10
	}
	return base
}

// Family returns the vendor grouping label for a catalog key. OpenAI's GPT
// and O-series share a single "GPT" group (they're the same vendor and the
// UI doesn't gain anything from the split). Cloud-merged keys land here as
// `model-...` literals (e.g. `model-claude-4-5-opus`, `model-gpt-oss-120b`,
// `model-private-2`) — we match substrings so those land in the right bucket
// without a second lookup table.
func Family(key string) string {
	k := strings.ToLower(key)
	switch {
	case strings.HasPrefix(k, "claude-"), strings.Contains(k, "-claude-"):
		return "Claude"
	case strings.HasPrefix(k, "gpt-oss"), strings.Contains(k, "gpt-oss"):
		return "GPT OSS"
	case strings.HasPrefix(k, "gpt-"),
		strings.Contains(k, "-gpt-"),
		strings.HasPrefix(k, "o3"), strings.HasPrefix(k, "o4"),
		strings.Contains(k, "-o3-"), strings.Contains(k, "-o4-"),
		strings.Contains(k, "-o3"):
		return "GPT"
	case strings.HasPrefix(k, "gemini-"), strings.Contains(k, "gemini"):
		return "Gemini"
	case strings.HasPrefix(k, "deepseek-"), strings.Contains(k, "deepseek"):
		return "DeepSeek"
	case strings.HasPrefix(k, "grok-"), strings.Contains(k, "grok"):
		return "Grok"
	case strings.HasPrefix(k, "qwen-"), strings.Contains(k, "qwen"):
		return "Qwen"
	case strings.HasPrefix(k, "kimi-"), strings.Contains(k, "kimi"):
		return "Kimi"
	case strings.HasPrefix(k, "glm-"), strings.Contains(k, "glm"):
		return "GLM"
	case strings.HasPrefix(k, "minimax-"), strings.Contains(k, "minimax"):
		return "MiniMax"
	case strings.HasPrefix(k, "swe-"), strings.Contains(k, "-swe-"):
		return "Windsurf SWE"
	case strings.HasPrefix(k, "arena-"):
		return "Arena"
	}
	// Cloud-merged keys that carry a provider tag (`MODEL_PRIVATE_*`,
	// `MODEL_CHAT_*`, anything we didn't name-match) fall through here —
	// use the provider field so they still bucket correctly.
	info := Get(key)
	if info != nil {
		switch info.Provider {
		case "anthropic":
			return "Claude"
		case "openai":
			return "GPT"
		case "google":
			return "Gemini"
		case "deepseek":
			return "DeepSeek"
		case "xai":
			return "Grok"
		case "alibaba":
			return "Qwen"
		case "moonshot":
			return "Kimi"
		case "zhipu":
			return "GLM"
		case "minimax":
			return "MiniMax"
		case "windsurf":
			return "Windsurf SWE"
		}
		if info.Provider != "" {
			return info.Provider
		}
	}
	return "Other"
}

// DisplayName turns a catalog key into a human-readable label. Segments split
// by "-" are title-cased and joined by spaces; adjacent pure-numeric tokens
// (e.g. cloud modelUIDs like "claude-opus-4-7-high") are merged back into
// dotted versions ("4.7") so the output matches the marketing name.
func DisplayName(key string) string {
	parts := strings.Split(key, "-")
	out := make([]string, 0, len(parts))
	for i := 0; i < len(parts); i++ {
		p := parts[i]
		if p == "" {
			continue
		}
		switch strings.ToLower(p) {
		case "gpt", "glm", "swe", "oss":
			out = append(out, strings.ToUpper(p))
			continue
		}
		// Pure-digit followed by pure-digit → "X.Y" (handles "4-7" → "4.7").
		if isPureDigits(p) && i+1 < len(parts) && isPureDigits(parts[i+1]) {
			out = append(out, p+"."+parts[i+1])
			i++
			continue
		}
		// Leave all-numeric / version-style tokens alone ("4.6", "120b").
		if isVersionish(p) {
			out = append(out, p)
			continue
		}
		out = append(out, strings.ToUpper(p[:1])+p[1:])
	}
	return strings.Join(out, " ")
}

func isPureDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// isVersionish reports whether the token looks like a version number or
// size/parameter identifier (e.g. "4.6", "120b", "1m") — i.e. not something
// we should title-case.
func isVersionish(s string) bool {
	hasDigit := false
	for _, r := range s {
		switch {
		case r >= '0' && r <= '9':
			hasDigit = true
		case r == '.' || r == 'b' || r == 'B' || r == 'm' || r == 'M' || r == 'k' || r == 'K':
			// allowed size / version characters
		default:
			return false
		}
	}
	return hasDigit
}
