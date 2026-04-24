package models

// seed is the hand-curated 107-model catalog, verbatim from src/models.js.
// Editing this table is how new models are added before the cloud catalog
// fetch completes.
var seed = map[string]Info{
	// ── Claude (Anthropic) ──
	"claude-3.5-sonnet":             {Name: "claude-3.5-sonnet", Provider: "anthropic", Enum: 166, Credit: 2},
	"claude-3.7-sonnet":             {Name: "claude-3.7-sonnet", Provider: "anthropic", Enum: 226, Credit: 2},
	"claude-3.7-sonnet-thinking":    {Name: "claude-3.7-sonnet-thinking", Provider: "anthropic", Enum: 227, Credit: 3},
	"claude-4-sonnet":               {Name: "claude-4-sonnet", Provider: "anthropic", Enum: 281, ModelUID: "MODEL_CLAUDE_4_SONNET", Credit: 2},
	"claude-4-sonnet-thinking":      {Name: "claude-4-sonnet-thinking", Provider: "anthropic", Enum: 282, ModelUID: "MODEL_CLAUDE_4_SONNET_THINKING", Credit: 3},
	"claude-4-opus":                 {Name: "claude-4-opus", Provider: "anthropic", Enum: 290, ModelUID: "MODEL_CLAUDE_4_OPUS", Credit: 4},
	"claude-4-opus-thinking":        {Name: "claude-4-opus-thinking", Provider: "anthropic", Enum: 291, ModelUID: "MODEL_CLAUDE_4_OPUS_THINKING", Credit: 5},
	"claude-4.1-opus":               {Name: "claude-4.1-opus", Provider: "anthropic", Enum: 328, ModelUID: "MODEL_CLAUDE_4_1_OPUS", Credit: 4},
	"claude-4.1-opus-thinking":      {Name: "claude-4.1-opus-thinking", Provider: "anthropic", Enum: 329, ModelUID: "MODEL_CLAUDE_4_1_OPUS_THINKING", Credit: 5},
	"claude-4.5-haiku":              {Name: "claude-4.5-haiku", Provider: "anthropic", ModelUID: "MODEL_PRIVATE_11", Credit: 1},
	"claude-4.5-sonnet":             {Name: "claude-4.5-sonnet", Provider: "anthropic", Enum: 353, ModelUID: "MODEL_PRIVATE_2", Credit: 2},
	"claude-4.5-sonnet-thinking":    {Name: "claude-4.5-sonnet-thinking", Provider: "anthropic", Enum: 354, ModelUID: "MODEL_PRIVATE_3", Credit: 3},
	"claude-4.5-opus":               {Name: "claude-4.5-opus", Provider: "anthropic", Enum: 391, ModelUID: "MODEL_CLAUDE_4_5_OPUS", Credit: 4},
	"claude-4.5-opus-thinking":      {Name: "claude-4.5-opus-thinking", Provider: "anthropic", Enum: 392, ModelUID: "MODEL_CLAUDE_4_5_OPUS_THINKING", Credit: 5},
	"claude-sonnet-4.6":             {Name: "claude-sonnet-4.6", Provider: "anthropic", ModelUID: "claude-sonnet-4-6", Credit: 4},
	"claude-sonnet-4.6-thinking":    {Name: "claude-sonnet-4.6-thinking", Provider: "anthropic", ModelUID: "claude-sonnet-4-6-thinking", Credit: 6},
	"claude-sonnet-4.6-1m":          {Name: "claude-sonnet-4.6-1m", Provider: "anthropic", ModelUID: "claude-sonnet-4-6-1m", Credit: 12},
	"claude-sonnet-4.6-thinking-1m": {Name: "claude-sonnet-4.6-thinking-1m", Provider: "anthropic", ModelUID: "claude-sonnet-4-6-thinking-1m", Credit: 16},
	"claude-opus-4.6":               {Name: "claude-opus-4.6", Provider: "anthropic", ModelUID: "claude-opus-4-6", Credit: 6},
	"claude-opus-4.6-thinking":      {Name: "claude-opus-4.6-thinking", Provider: "anthropic", ModelUID: "claude-opus-4-6-thinking", Credit: 8},

	// Opus 4.7 ships upstream as 5 reasoning tiers (low / medium / high /
	// xhigh / max) rather than a single model; credits estimated from the
	// 4.5 / 4.6 progression, override via runtime catalog fetch if cloud
	// creditMultiplier disagrees.
	"claude-opus-4.7-low":    {Name: "claude-opus-4.7-low", Provider: "anthropic", ModelUID: "claude-opus-4-7-low", Credit: 4},
	"claude-opus-4.7-medium": {Name: "claude-opus-4.7-medium", Provider: "anthropic", ModelUID: "claude-opus-4-7-medium", Credit: 5},
	"claude-opus-4.7-high":   {Name: "claude-opus-4.7-high", Provider: "anthropic", ModelUID: "claude-opus-4-7-high", Credit: 6},
	"claude-opus-4.7-xhigh":  {Name: "claude-opus-4.7-xhigh", Provider: "anthropic", ModelUID: "claude-opus-4-7-xhigh", Credit: 8},
	"claude-opus-4.7-max":    {Name: "claude-opus-4.7-max", Provider: "anthropic", ModelUID: "claude-opus-4-7-max", Credit: 10},

	// ── GPT (OpenAI) ──
	"gpt-4o":      {Name: "gpt-4o", Provider: "openai", Enum: 109, ModelUID: "MODEL_CHAT_GPT_4O_2024_08_06", Credit: 1},
	"gpt-4o-mini": {Name: "gpt-4o-mini", Provider: "openai", Enum: 113, Credit: 0.5},
	"gpt-4.1":     {Name: "gpt-4.1", Provider: "openai", Enum: 259, ModelUID: "MODEL_CHAT_GPT_4_1_2025_04_14", Credit: 1},
	"gpt-4.1-mini": {Name: "gpt-4.1-mini", Provider: "openai", Enum: 260, Credit: 0.5},
	"gpt-4.1-nano": {Name: "gpt-4.1-nano", Provider: "openai", Enum: 261, Credit: 0.25},
	"gpt-5":        {Name: "gpt-5", Provider: "openai", Enum: 340, ModelUID: "MODEL_PRIVATE_6", Credit: 0.5},
	"gpt-5-medium": {Name: "gpt-5-medium", Provider: "openai", ModelUID: "MODEL_PRIVATE_7", Credit: 1},
	"gpt-5-high":   {Name: "gpt-5-high", Provider: "openai", ModelUID: "MODEL_PRIVATE_8", Credit: 2},
	"gpt-5-mini":   {Name: "gpt-5-mini", Provider: "openai", Enum: 337, Credit: 0.25},
	"gpt-5-codex":  {Name: "gpt-5-codex", Provider: "openai", Enum: 346, ModelUID: "MODEL_CHAT_GPT_5_CODEX", Credit: 0.5},

	"gpt-5.1":             {Name: "gpt-5.1", Provider: "openai", ModelUID: "MODEL_PRIVATE_12", Credit: 0.5},
	"gpt-5.1-low":         {Name: "gpt-5.1-low", Provider: "openai", ModelUID: "MODEL_PRIVATE_13", Credit: 0.5},
	"gpt-5.1-medium":      {Name: "gpt-5.1-medium", Provider: "openai", ModelUID: "MODEL_PRIVATE_14", Credit: 1},
	"gpt-5.1-high":        {Name: "gpt-5.1-high", Provider: "openai", ModelUID: "MODEL_PRIVATE_15", Credit: 2},
	"gpt-5.1-fast":        {Name: "gpt-5.1-fast", Provider: "openai", ModelUID: "MODEL_PRIVATE_20", Credit: 1},
	"gpt-5.1-low-fast":    {Name: "gpt-5.1-low-fast", Provider: "openai", ModelUID: "MODEL_PRIVATE_21", Credit: 1},
	"gpt-5.1-medium-fast": {Name: "gpt-5.1-medium-fast", Provider: "openai", ModelUID: "MODEL_PRIVATE_22", Credit: 2},
	"gpt-5.1-high-fast":   {Name: "gpt-5.1-high-fast", Provider: "openai", ModelUID: "MODEL_PRIVATE_23", Credit: 4},

	"gpt-5.1-codex-low":        {Name: "gpt-5.1-codex-low", Provider: "openai", ModelUID: "MODEL_GPT_5_1_CODEX_LOW", Credit: 0.5},
	"gpt-5.1-codex-medium":     {Name: "gpt-5.1-codex-medium", Provider: "openai", ModelUID: "MODEL_PRIVATE_9", Credit: 1},
	"gpt-5.1-codex-mini-low":   {Name: "gpt-5.1-codex-mini-low", Provider: "openai", ModelUID: "MODEL_GPT_5_1_CODEX_MINI_LOW", Credit: 0.25},
	"gpt-5.1-codex-mini":       {Name: "gpt-5.1-codex-mini", Provider: "openai", ModelUID: "MODEL_PRIVATE_19", Credit: 0.5},
	"gpt-5.1-codex-max-low":    {Name: "gpt-5.1-codex-max-low", Provider: "openai", ModelUID: "MODEL_GPT_5_1_CODEX_MAX_LOW", Credit: 1},
	"gpt-5.1-codex-max-medium": {Name: "gpt-5.1-codex-max-medium", Provider: "openai", ModelUID: "MODEL_GPT_5_1_CODEX_MAX_MEDIUM", Credit: 1.25},
	"gpt-5.1-codex-max-high":   {Name: "gpt-5.1-codex-max-high", Provider: "openai", ModelUID: "MODEL_GPT_5_1_CODEX_MAX_HIGH", Credit: 1.5},

	"gpt-5.2":             {Name: "gpt-5.2", Provider: "openai", Enum: 401, ModelUID: "MODEL_GPT_5_2_MEDIUM", Credit: 2},
	"gpt-5.2-none":        {Name: "gpt-5.2-none", Provider: "openai", ModelUID: "MODEL_GPT_5_2_NONE", Credit: 1},
	"gpt-5.2-low":         {Name: "gpt-5.2-low", Provider: "openai", Enum: 400, ModelUID: "MODEL_GPT_5_2_LOW", Credit: 1},
	"gpt-5.2-high":        {Name: "gpt-5.2-high", Provider: "openai", Enum: 402, ModelUID: "MODEL_GPT_5_2_HIGH", Credit: 3},
	"gpt-5.2-xhigh":       {Name: "gpt-5.2-xhigh", Provider: "openai", Enum: 403, ModelUID: "MODEL_GPT_5_2_XHIGH", Credit: 8},
	"gpt-5.2-none-fast":   {Name: "gpt-5.2-none-fast", Provider: "openai", ModelUID: "MODEL_GPT_5_2_NONE_PRIORITY", Credit: 2},
	"gpt-5.2-low-fast":    {Name: "gpt-5.2-low-fast", Provider: "openai", ModelUID: "MODEL_GPT_5_2_LOW_PRIORITY", Credit: 2},
	"gpt-5.2-medium-fast": {Name: "gpt-5.2-medium-fast", Provider: "openai", ModelUID: "MODEL_GPT_5_2_MEDIUM_PRIORITY", Credit: 4},
	"gpt-5.2-high-fast":   {Name: "gpt-5.2-high-fast", Provider: "openai", ModelUID: "MODEL_GPT_5_2_HIGH_PRIORITY", Credit: 6},
	"gpt-5.2-xhigh-fast":  {Name: "gpt-5.2-xhigh-fast", Provider: "openai", ModelUID: "MODEL_GPT_5_2_XHIGH_PRIORITY", Credit: 16},

	"gpt-5.2-codex-low":         {Name: "gpt-5.2-codex-low", Provider: "openai", ModelUID: "MODEL_GPT_5_2_CODEX_LOW", Credit: 1},
	"gpt-5.2-codex-medium":      {Name: "gpt-5.2-codex-medium", Provider: "openai", ModelUID: "MODEL_GPT_5_2_CODEX_MEDIUM", Credit: 1},
	"gpt-5.2-codex-high":        {Name: "gpt-5.2-codex-high", Provider: "openai", ModelUID: "MODEL_GPT_5_2_CODEX_HIGH", Credit: 2},
	"gpt-5.2-codex-xhigh":       {Name: "gpt-5.2-codex-xhigh", Provider: "openai", ModelUID: "MODEL_GPT_5_2_CODEX_XHIGH", Credit: 3},
	"gpt-5.2-codex-low-fast":    {Name: "gpt-5.2-codex-low-fast", Provider: "openai", ModelUID: "MODEL_GPT_5_2_CODEX_LOW_PRIORITY", Credit: 2},
	"gpt-5.2-codex-medium-fast": {Name: "gpt-5.2-codex-medium-fast", Provider: "openai", ModelUID: "MODEL_GPT_5_2_CODEX_MEDIUM_PRIORITY", Credit: 2},
	"gpt-5.2-codex-high-fast":   {Name: "gpt-5.2-codex-high-fast", Provider: "openai", ModelUID: "MODEL_GPT_5_2_CODEX_HIGH_PRIORITY", Credit: 4},
	"gpt-5.2-codex-xhigh-fast":  {Name: "gpt-5.2-codex-xhigh-fast", Provider: "openai", ModelUID: "MODEL_GPT_5_2_CODEX_XHIGH_PRIORITY", Credit: 6},

	"gpt-5.3-codex": {Name: "gpt-5.3-codex", Provider: "openai", ModelUID: "gpt-5-3-codex-medium", Credit: 1},

	"gpt-5.4-low":         {Name: "gpt-5.4-low", Provider: "openai", ModelUID: "gpt-5-4-low", Credit: 1},
	"gpt-5.4-medium":      {Name: "gpt-5.4-medium", Provider: "openai", ModelUID: "gpt-5-4-medium", Credit: 2},
	"gpt-5.4-xhigh":       {Name: "gpt-5.4-xhigh", Provider: "openai", ModelUID: "gpt-5-4-xhigh", Credit: 8},
	"gpt-5.4-mini-low":    {Name: "gpt-5.4-mini-low", Provider: "openai", ModelUID: "gpt-5-4-mini-low", Credit: 1.5},
	"gpt-5.4-mini-medium": {Name: "gpt-5.4-mini-medium", Provider: "openai", ModelUID: "gpt-5-4-mini-medium", Credit: 1.5},
	"gpt-5.4-mini-high":   {Name: "gpt-5.4-mini-high", Provider: "openai", ModelUID: "gpt-5-4-mini-high", Credit: 4.5},
	"gpt-5.4-mini-xhigh":  {Name: "gpt-5.4-mini-xhigh", Provider: "openai", ModelUID: "gpt-5-4-mini-xhigh", Credit: 12},

	"gpt-oss-120b": {Name: "gpt-oss-120b", Provider: "openai", ModelUID: "MODEL_GPT_OSS_120B", Credit: 0.25},

	// ── O-series ──
	"o3-mini": {Name: "o3-mini", Provider: "openai", Enum: 207, Credit: 0.5},
	"o3":      {Name: "o3", Provider: "openai", Enum: 218, ModelUID: "MODEL_CHAT_O3", Credit: 1},
	"o3-high": {Name: "o3-high", Provider: "openai", ModelUID: "MODEL_CHAT_O3_HIGH", Credit: 1},
	"o3-pro":  {Name: "o3-pro", Provider: "openai", Enum: 294, Credit: 4},
	"o4-mini": {Name: "o4-mini", Provider: "openai", Enum: 264, Credit: 0.5},

	// ── Gemini ──
	"gemini-2.5-pro":           {Name: "gemini-2.5-pro", Provider: "google", Enum: 246, ModelUID: "MODEL_GOOGLE_GEMINI_2_5_PRO", Credit: 1},
	"gemini-2.5-flash":         {Name: "gemini-2.5-flash", Provider: "google", Enum: 312, ModelUID: "MODEL_GOOGLE_GEMINI_2_5_FLASH", Credit: 0.5},
	"gemini-3.0-pro":           {Name: "gemini-3.0-pro", Provider: "google", Enum: 412, ModelUID: "MODEL_GOOGLE_GEMINI_3_0_PRO_LOW", Credit: 1},
	"gemini-3.0-flash-minimal": {Name: "gemini-3.0-flash-minimal", Provider: "google", ModelUID: "MODEL_GOOGLE_GEMINI_3_0_FLASH_MINIMAL", Credit: 0.75},
	"gemini-3.0-flash-low":     {Name: "gemini-3.0-flash-low", Provider: "google", ModelUID: "MODEL_GOOGLE_GEMINI_3_0_FLASH_LOW", Credit: 1},
	"gemini-3.0-flash":         {Name: "gemini-3.0-flash", Provider: "google", Enum: 415, ModelUID: "MODEL_GOOGLE_GEMINI_3_0_FLASH_MEDIUM", Credit: 1},
	"gemini-3.0-flash-high":    {Name: "gemini-3.0-flash-high", Provider: "google", ModelUID: "MODEL_GOOGLE_GEMINI_3_0_FLASH_HIGH", Credit: 1.75},
	"gemini-3.1-pro-low":       {Name: "gemini-3.1-pro-low", Provider: "google", ModelUID: "gemini-3-1-pro-low", Credit: 1},
	"gemini-3.1-pro-high":      {Name: "gemini-3.1-pro-high", Provider: "google", ModelUID: "gemini-3-1-pro-high", Credit: 2},

	// ── DeepSeek ──
	"deepseek-v3":   {Name: "deepseek-v3", Provider: "deepseek", Enum: 205, Credit: 0.5},
	"deepseek-v3-2": {Name: "deepseek-v3-2", Provider: "deepseek", Enum: 409, Credit: 0.5},
	"deepseek-r1":   {Name: "deepseek-r1", Provider: "deepseek", Enum: 206, Credit: 1},

	// ── Grok ──
	"grok-3":               {Name: "grok-3", Provider: "xai", Enum: 217, ModelUID: "MODEL_XAI_GROK_3", Credit: 1},
	"grok-3-mini":          {Name: "grok-3-mini", Provider: "xai", Enum: 234, Credit: 0.5},
	"grok-3-mini-thinking": {Name: "grok-3-mini-thinking", Provider: "xai", ModelUID: "MODEL_XAI_GROK_3_MINI_REASONING", Credit: 0.125},
	"grok-code-fast-1":     {Name: "grok-code-fast-1", Provider: "xai", ModelUID: "MODEL_PRIVATE_4", Credit: 0.5},

	// ── Qwen ──
	"qwen-3":       {Name: "qwen-3", Provider: "alibaba", Enum: 324, Credit: 0.5},
	"qwen-3-coder": {Name: "qwen-3-coder", Provider: "alibaba", Enum: 325, Credit: 0.5},

	// ── Kimi ──
	"kimi-k2":   {Name: "kimi-k2", Provider: "moonshot", ModelUID: "MODEL_KIMI_K2", Credit: 0.5},
	"kimi-k2.5": {Name: "kimi-k2.5", Provider: "moonshot", ModelUID: "kimi-k2-5", Credit: 1},

	// ── GLM ──
	"glm-4.7": {Name: "glm-4.7", Provider: "zhipu", Enum: 417, ModelUID: "MODEL_GLM_4_7", Credit: 0.25},
	"glm-5":   {Name: "glm-5", Provider: "zhipu", ModelUID: "glm-5", Credit: 1.5},
	"glm-5.1": {Name: "glm-5.1", Provider: "zhipu", ModelUID: "glm-5-1", Credit: 1.5},

	// ── MiniMax ──
	"minimax-m2.5": {Name: "minimax-m2.5", Provider: "minimax", ModelUID: "minimax-m2-5", Credit: 1},

	// ── Windsurf SWE ──
	"swe-1.5":      {Name: "swe-1.5", Provider: "windsurf", Enum: 369, ModelUID: "MODEL_SWE_1_5_SLOW", Credit: 0.5},
	"swe-1.5-fast": {Name: "swe-1.5-fast", Provider: "windsurf", Enum: 359, ModelUID: "MODEL_SWE_1_5", Credit: 0.5},
	"swe-1.6":      {Name: "swe-1.6", Provider: "windsurf", ModelUID: "swe-1-6", Credit: 0.5},
	"swe-1.6-fast": {Name: "swe-1.6-fast", Provider: "windsurf", ModelUID: "swe-1-6-fast", Credit: 0.5},

	// ── Arena ──
	"arena-fast":  {Name: "arena-fast", Provider: "windsurf", ModelUID: "arena-fast", Credit: 0.5},
	"arena-smart": {Name: "arena-smart", Provider: "windsurf", ModelUID: "arena-smart", Credit: 1},
}
