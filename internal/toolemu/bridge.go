// N24 — Cascade native tool bridge.
//
// Translates between OpenAI-shaped client tools (Read / Bash / Glob /
// Grep / ...) and Cascade's built-in IDE step kinds (view_file /
// run_command / find / grep_search_v2 / ...).
//
// Why this layer exists
// ─────────────────────
// The prompt-emulation path in toolemu.go injects tool definitions into
// `additional_instructions_section` and asks the model to emit
// `<tool_call>` markup. GPT-5.x and Claude follow that contract reliably;
// non-Anthropic non-OpenAI models (GLM/Kimi/Qwen) are flaky there because
// the gateway's baked-in system prompt outweighs anything we inject. The
// tools the gateway DOES respect are Cascade's own — view_file,
// run_command, grep_search_v2, find — because those names appear inside
// the planner's training distribution as first-class function-calling
// tokens, not as proxy-injected text.
//
// The bridge never enables planner_mode=DEFAULT on its own (that path
// triggers server-side workspace mocking and /tmp/windsurf-workspace
// path leaks). Instead, when ALL caller tools map cleanly to the Cascade
// vocabulary, the bridge:
//
//   1. Forward-translates the caller's OpenAI tool inventory + tool
//      history into Cascade-vocabulary names so the gateway sees a
//      familiar tool list and a sequence of completed cascade-style
//      steps.
//   2. Reverse-translates each trajectory step (view_file, run_command,
//      grep_search_v2, find, list_directory) the planner emits back into
//      the caller's original OpenAI tool name (Read, Bash, Grep, Glob,
//      ...).
//
// When ANY tool the caller declared cannot be mapped, the entire bridge
// is skipped and the regular prompt-emulation path takes over. This is
// deliberately conservative: a partial mapping would surface a confusing
// hybrid (some tools in Cascade vocab, some in OpenAI vocab) and the
// model would have no idea which to use for which step.
//
// Public surface (used by chat.go):
//   - CanUseNativeBridge(tools)        — eligibility check
//   - BuildReverseLookup(tools)        — Cascade-name → OpenAI-name map
//   - TranslateOpenAIToolNameToCascade — one-way name lookup
//
// The actual proto-level forward translation (rewriting the message
// history's tool_call_id / tool_name fields into Cascade's vocabulary)
// is a runtime path that lives in the windsurf package; this file
// provides the static mapping table + helpers.

package toolemu

import (
	"strings"
)

// nativeToolMap is the canonical bridge between OpenAI tool names that
// callers commonly declare (Anthropic Claude Code, OpenAI Responses /
// Codex CLI, third-party agents) and Cascade's native step kinds. Keys
// are case-insensitive on the OpenAI side; the Cascade-side value is
// always the lowercase form Cascade itself emits.
//
// Coverage: deliberately limited to the seven step kinds Cascade has
// strong training on. Anything outside this set forces the bridge to
// skip and prompt-emulation to take over.
var nativeToolMap = map[string]string{
	// File reading. Claude Code's Read, Anthropic Claude tool.
	"read":      "view_file",
	"view_file": "view_file",
	"read_file": "view_file",
	"open":      "view_file",

	// Shell execution. Claude Code's Bash, OpenAI Codex's shell.
	"bash":         "run_command",
	"shell":        "run_command",
	"shell_exec":   "run_command",
	"run_command":  "run_command",
	"execute":      "run_command",
	"run_shell":    "run_command",

	// File globbing. Claude Code's Glob.
	"glob":   "find",
	"find":   "find",
	"search": "find",

	// Content search. Claude Code's Grep, ripgrep wrappers.
	"grep":            "grep_search_v2",
	"grep_search":     "grep_search_v2",
	"grep_search_v2":  "grep_search_v2",
	"ripgrep":         "grep_search_v2",
	"search_content":  "grep_search_v2",

	// Directory listing. Common in agent toolboxes.
	"list_directory": "list_directory",
	"list_dir":       "list_directory",
	"ls":             "list_directory",
	"dir":            "list_directory",

	// File writing — Claude Code's Write/Edit. Cascade has edit_file
	// for this; we map both Write and Edit to the same step kind, which
	// is fine because the model treats them symmetrically.
	"write":      "edit_file",
	"edit":       "edit_file",
	"edit_file":  "edit_file",
	"write_file": "edit_file",

	// Web fetching — Claude Code's WebFetch.
	"webfetch":   "browser_open",
	"web_fetch":  "browser_open",
	"browser_open": "browser_open",
	"fetch_url":  "browser_open",
}

// CanUseNativeBridge reports whether every tool in `tools` can be mapped
// to a Cascade native step kind. When this returns true, chat.go can opt
// into the bridge path. Returns false (with no work done) on the first
// unmappable tool — better to fall back cleanly to prompt-emulation than
// to surface a partial mapping that confuses the model about which dialect
// to use.
//
// Empty tool list → true (nothing to map; no eligibility issue).
func CanUseNativeBridge(tools []Tool) bool {
	if len(tools) == 0 {
		return true
	}
	for _, t := range tools {
		if t.Type != "function" || t.Function.Name == "" {
			return false
		}
		if _, ok := nativeToolMap[strings.ToLower(t.Function.Name)]; !ok {
			return false
		}
	}
	return true
}

// TranslateOpenAIToolNameToCascade returns the Cascade step name for the
// given OpenAI-side declared tool name, or "" when no mapping exists.
// Case-insensitive on input.
func TranslateOpenAIToolNameToCascade(name string) string {
	return nativeToolMap[strings.ToLower(name)]
}

// BuildReverseLookup creates a Cascade-name → OpenAI-name map for the
// caller's declared tools. Used by chat.go on the way back: when Cascade
// emits a `view_file` trajectory step, look up which OpenAI name the
// caller originally declared (Read vs view_file vs read_file) so the
// emitted tool_call carries the name the caller's SDK expects.
//
// Multiple OpenAI names mapping to the same Cascade name → first wins.
// This is rare in practice; tools[] usually has each name once.
func BuildReverseLookup(tools []Tool) map[string]string {
	out := map[string]string{}
	for _, t := range tools {
		if t.Type != "function" || t.Function.Name == "" {
			continue
		}
		cascadeName := nativeToolMap[strings.ToLower(t.Function.Name)]
		if cascadeName == "" {
			continue
		}
		if _, exists := out[cascadeName]; !exists {
			out[cascadeName] = t.Function.Name
		}
	}
	return out
}

// IsNativeStepKind reports whether `cascadeName` is a step kind the
// bridge knows how to reverse-translate. Used by chat.go to decide
// whether to drop the step (untranslatable native tool, e.g. Cascade's
// internal todo/wait/done) or surface it as a tool_call to the caller.
func IsNativeStepKind(cascadeName string) bool {
	switch strings.ToLower(cascadeName) {
	case "view_file", "run_command", "find", "grep_search_v2",
		"list_directory", "edit_file", "browser_open":
		return true
	}
	return false
}

// CascadeStepToOpenAIToolCall builds a synthetic OpenAI-shaped ToolCall
// from a Cascade trajectory step. `cascadeName` is the step kind, `args`
// is the JSON-encoded argument string Cascade emitted, and `reverse` is
// the map from BuildReverseLookup. Returns ("", false) when the step
// can't be reverse-translated (caller drops it).
//
// Note: the args string is passed through unchanged — Cascade's argument
// shape for view_file (`{"path": "..."}`) and Claude Code's Read schema
// (`{"file_path": "..."}`) are NOT identical, but the model is free to
// learn the difference from the schema we send in the tools[]. If the
// caller wants strict shape conversion they can add a second translation
// table; for now the bridge stops at name translation.
func CascadeStepToOpenAIToolCall(cascadeName, argsJSON string, reverse map[string]string, counter int) (ToolCall, bool) {
	openAIName, ok := reverse[strings.ToLower(cascadeName)]
	if !ok {
		return ToolCall{}, false
	}
	if argsJSON == "" {
		argsJSON = "{}"
	}
	return ToolCall{
		ID:            buildSyntheticID(counter),
		Name:          openAIName,
		ArgumentsJSON: argsJSON,
	}, true
}

func buildSyntheticID(counter int) string {
	// Synthetic ID for bridge-emitted calls. The shape matches the
	// existing ParseAll synthesised IDs so downstream callers (the
	// Anthropic translation in messages.go) can't tell a bridge-call
	// apart from a markup-call by the ID alone.
	return "call_bridge_" + itoa(counter)
}

// itoa is a tiny stand-in for strconv.Itoa to keep this file dependency-free.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
