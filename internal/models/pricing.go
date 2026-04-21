// Package models · pricing table.
//
// Windsurf bills in internal "credits", but operators want to see the dollar
// equivalent assuming the model were accessed on its public provider. The
// rates below follow each underlying model's retail USD / 1M token price
// (input / output), snapshot 2026-04. Numbers are approximate and shift with
// provider price cuts — update as needed. Unlisted models fall back to
// DefaultPricing.
package models

// Pricing is USD per 1M tokens.
type Pricing struct {
	InputPerM  float64 `json:"inputPerM"`
	OutputPerM float64 `json:"outputPerM"`
}

// DefaultPricing is the fallback applied to any model not explicitly listed.
// Picked as a middle-of-road public-provider price so "total cost" never
// reads zero just because we missed a Windsurf-branded alias.
var DefaultPricing = Pricing{InputPerM: 1.00, OutputPerM: 5.00}

// ModelPricing maps Windsurf model IDs to their equivalent public retail
// price. Keep keys in sync with internal/models/seed.go.
var ModelPricing = map[string]Pricing{
	// ─── Anthropic Claude ──────────────────────────────────
	"claude-opus-4-7":            {15.00, 75.00},
	"claude-opus-4-7-max":        {15.00, 75.00},
	"claude-opus-4.6":            {15.00, 75.00},
	"claude-opus-4.5":            {15.00, 75.00},
	"claude-opus-4.6-thinking":   {15.00, 75.00},

	"claude-sonnet-4.6":          {3.00, 15.00},
	"claude-sonnet-4.6-thinking": {3.00, 15.00},
	"claude-4.5-sonnet":          {3.00, 15.00},
	"claude-4.5-sonnet-thinking": {3.00, 15.00},
	"claude-sonnet-4":            {3.00, 15.00},

	"claude-4.5-haiku": {0.80, 4.00},
	"claude-3.5-haiku": {0.80, 4.00},

	// ─── OpenAI GPT ────────────────────────────────────────
	"gpt-5":         {1.25, 10.00},
	"gpt-5-high":    {2.50, 15.00},
	"gpt-5-xhigh":   {5.00, 25.00},
	"gpt-5.2":       {2.00, 10.00},
	"gpt-5.2-high":  {3.00, 15.00},
	"gpt-5.4":       {3.00, 15.00},
	"gpt-5.4-high":  {4.00, 20.00},
	"gpt-5.4-xhigh": {5.00, 25.00},
	"gpt-4.1":       {2.00, 8.00},
	"gpt-4o":        {2.50, 10.00},
	"gpt-4o-mini":   {0.15, 0.60},
	"o3":            {10.00, 40.00},
	"o3-mini":       {1.10, 4.40},
	"o4-mini":       {1.10, 4.40},

	// ─── Google Gemini ─────────────────────────────────────
	"gemini-2.5-flash":          {0.075, 0.30},
	"gemini-2.5-pro":            {1.25, 5.00},
	"gemini-2.5-pro-thinking":   {1.25, 5.00},
	"gemini-3.0":                {2.00, 10.00},
	"gemini-3.0-thinking":       {2.00, 10.00},
	"gemini-3.0-flash":          {0.30, 1.20},

	// ─── xAI Grok ──────────────────────────────────────────
	"grok-3":               {3.00, 15.00},
	"grok-3-mini":          {0.30, 0.50},
	"grok-3-mini-thinking": {0.30, 0.50},
	"grok-4":               {3.00, 15.00},

	// ─── DeepSeek ──────────────────────────────────────────
	"deepseek-v3":    {0.27, 1.10},
	"deepseek-v3.1":  {0.27, 1.10},
	"deepseek-r1":    {0.55, 2.19},
	"deepseek-r1.1":  {0.55, 2.19},

	// ─── Alibaba Qwen ──────────────────────────────────────
	"qwen-3":         {0.40, 1.20},
	"qwen-max":       {1.60, 6.40},
	"qwen-turbo":     {0.20, 0.60},

	// ─── Moonshot Kimi ─────────────────────────────────────
	"kimi-k2":        {0.50, 2.00},
	"moonshot-v2":    {0.80, 3.20},

	// ─── Zhipu GLM ─────────────────────────────────────────
	"glm-5":           {0.11, 0.28},
	"glm-5.1":         {0.11, 0.28},
	"glm-4.6":         {0.11, 0.28},
	"glm-4.6-thinking": {0.11, 0.28},

	// ─── Windsurf fast tier (flat low price) ──────────────
	"windsurf-fast-small": {0.10, 0.40},
	"windsurf-fast-base":  {0.20, 0.80},
}

// PriceFor returns the USD cost for an (in, out) token pair on modelKey.
// Missing models fall back to DefaultPricing.
func PriceFor(modelKey string, inputTokens, outputTokens int64) float64 {
	p, ok := ModelPricing[modelKey]
	if !ok {
		p = DefaultPricing
	}
	// per-million → divide by 1e6
	return float64(inputTokens)*p.InputPerM/1_000_000 +
		float64(outputTokens)*p.OutputPerM/1_000_000
}

// PricingOf returns the raw table entry (falling back to default) so the
// dashboard can show "which rates are we using" when debugging.
func PricingOf(modelKey string) Pricing {
	if p, ok := ModelPricing[modelKey]; ok {
		return p
	}
	return DefaultPricing
}
