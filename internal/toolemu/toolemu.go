// Package toolemu implements the prompt-level tool-call emulation used to
// expose OpenAI tools[] through Cascade (which has no native tool slot). It
// covers:
//
//   - BuildPreambleForProto: system-prompt text injected via
//     CascadeConversationalPlannerConfig.tool_calling_section/additional_
//     instructions_section.
//   - NormalizeMessages: rewrite role:tool / assistant.tool_calls into
//     <tool_result>/<tool_call> text forms Cascade can cleanly consume.
//   - StreamParser: streaming parser that strips <tool_call>...</tool_call>
//     and <tool_result ...>...</tool_result> blocks from the cascade text
//     deltas, optionally producing structured ToolCall records.
//
// Direct port of src/handlers/tool-emulation.js.
package toolemu

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// Tool is the subset of an OpenAI tool[] entry we consume.
type Tool struct {
	Type     string       `json:"type"`
	Function ToolFunction `json:"function"`
}

type ToolFunction struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters,omitempty"`
}

// ToolChoice covers both the string form ("auto"/"required"/"none") and the
// {type:function, function:{name:X}} object form.
type ToolChoice struct {
	Mode      string // "auto" / "required" / "none"
	ForceName string
}

// ResolveToolChoice decodes the raw JSON value.
func ResolveToolChoice(raw json.RawMessage) ToolChoice {
	if len(raw) == 0 {
		return ToolChoice{Mode: "auto"}
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		switch s {
		case "auto", "":
			return ToolChoice{Mode: "auto"}
		case "required", "any":
			return ToolChoice{Mode: "required"}
		case "none":
			return ToolChoice{Mode: "none"}
		}
	}
	var obj struct {
		Function struct {
			Name string `json:"name"`
		} `json:"function"`
	}
	if err := json.Unmarshal(raw, &obj); err == nil && obj.Function.Name != "" {
		return ToolChoice{Mode: "required", ForceName: obj.Function.Name}
	}
	return ToolChoice{Mode: "auto"}
}

// ─── Preamble builders ─────────────────────────────────────

const systemHeader = `You have access to the following functions. To invoke a function, emit a block in this EXACT format:

<tool_call>{"name":"<function_name>","arguments":{...}}</tool_call>

Rules:
1. Each <tool_call>...</tool_call> block must fit on ONE line (no line breaks inside the JSON).
2. "arguments" must be a JSON object matching the function's parameter schema.
3. You MAY emit MULTIPLE <tool_call> blocks if the request requires calling several functions in parallel. Emit ALL needed calls consecutively, then STOP generating.
4. After emitting the last <tool_call> block, STOP. Do not write any explanation after it. The caller executes the functions and returns results wrapped in <tool_result tool_call_id="...">...</tool_result> tags in the next user turn.
5. NEVER say "I don't have access to tools" or "I cannot perform that action" — the functions listed below ARE your available tools.`

var choiceSuffix = map[string]string{
	"auto":     "\n6. When a function is relevant to the user's request, you SHOULD call it rather than answering from memory. Prefer using a tool over guessing.",
	"required": "\n6. You MUST call at least one function for every request. Do NOT answer directly in plain text — always use a <tool_call>.",
	"none":     "\n6. Do NOT call any functions. Answer the user's question directly in plain text.",
}

// BuildPreambleForProto is the system-level preamble injected into the
// Cascade planner's section-override slots. Empty when tools is empty.
func BuildPreambleForProto(tools []Tool, choiceRaw json.RawMessage) string {
	if len(tools) == 0 {
		return ""
	}
	choice := ResolveToolChoice(choiceRaw)

	var b strings.Builder
	b.WriteString(systemHeader)
	if s, ok := choiceSuffix[choice.Mode]; ok {
		b.WriteString(s)
	} else {
		b.WriteString(choiceSuffix["auto"])
	}
	if choice.ForceName != "" {
		b.WriteString(fmt.Sprintf("\n7. You MUST call the function %q. No other function and no direct answer.", choice.ForceName))
	}
	b.WriteString("\n\nAvailable functions:")
	for _, t := range tools {
		if t.Type != "function" || t.Function.Name == "" {
			continue
		}
		b.WriteString("\n\n### ")
		b.WriteString(t.Function.Name)
		if t.Function.Description != "" {
			b.WriteByte('\n')
			b.WriteString(t.Function.Description)
		}
		if len(t.Function.Parameters) > 0 {
			b.WriteString("\nParameters:\n```json\n")
			var pretty bytes
			if err := pretty.encode(t.Function.Parameters); err == nil {
				b.Write(pretty.buf)
			} else {
				b.Write(t.Function.Parameters)
			}
			b.WriteString("\n```")
		}
	}
	return b.String()
}

// tiny helper to pretty-print a JSON RawMessage without another allocation
type bytes struct {
	buf []byte
}

func (x *bytes) encode(raw json.RawMessage) error {
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return err
	}
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	x.buf = b
	return nil
}

// ─── Message normalisation ─────────────────────────────────

// OAIMessage is the incoming OpenAI message before normalisation.
type OAIMessage struct {
	Role       string          `json:"role"`
	Content    json.RawMessage `json:"content,omitempty"`
	ToolCalls  []OAIToolCall   `json:"tool_calls,omitempty"`
	ToolCallID string          `json:"tool_call_id,omitempty"`
	Name       string          `json:"name,omitempty"`
}

type OAIToolCall struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

// NormalMessage is the post-normalisation shape — what the Cascade path expects.
type NormalMessage struct {
	Role    string
	Content string
}

// contentToString collapses OpenAI "content" (string | [{type:"text",text}]) into a string.
func contentToString(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	var arr []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if err := json.Unmarshal(raw, &arr); err == nil {
		var b strings.Builder
		for _, p := range arr {
			if p.Type == "text" {
				b.WriteString(p.Text)
			}
		}
		return b.String()
	}
	return string(raw)
}

// Normalize rewrites the messages array so Cascade sees a valid conversation:
//   - role:tool → synthetic user with <tool_result tool_call_id="X">...</tool_result>
//   - assistant.tool_calls → render prior calls as inline <tool_call>...
func Normalize(msgs []OAIMessage) []NormalMessage {
	out := make([]NormalMessage, 0, len(msgs))
	for _, m := range msgs {
		switch m.Role {
		case "tool":
			id := m.ToolCallID
			if id == "" {
				id = "unknown"
			}
			body := contentToString(m.Content)
			out = append(out, NormalMessage{
				Role:    "user",
				Content: fmt.Sprintf("<tool_result tool_call_id=%q>\n%s\n</tool_result>", id, body),
			})
		case "assistant":
			var parts []string
			if text := contentToString(m.Content); text != "" {
				parts = append(parts, text)
			}
			for _, tc := range m.ToolCalls {
				name := tc.Function.Name
				if name == "" {
					name = "unknown"
				}
				var args any
				_ = json.Unmarshal([]byte(tc.Function.Arguments), &args)
				if args == nil {
					args = map[string]any{}
				}
				enc, _ := json.Marshal(map[string]any{"name": name, "arguments": args})
				parts = append(parts, fmt.Sprintf("<tool_call>%s</tool_call>", string(enc)))
			}
			out = append(out, NormalMessage{Role: "assistant", Content: strings.Join(parts, "\n")})
		default:
			out = append(out, NormalMessage{Role: m.Role, Content: contentToString(m.Content)})
		}
	}
	return out
}

// ─── Streaming parser ─────────────────────────────────────

const (
	tcOpen   = "<tool_call>"
	tcClose  = "</tool_call>"
	trPrefix = "<tool_result"
	trClose  = "</tool_result>"
)

// ToolCall is a parsed tool call ready for the OpenAI response.
type ToolCall struct {
	ID            string
	Name          string
	ArgumentsJSON string
}

// StreamParser buffers deltas and yields safe-to-emit text + closed tool calls.
type StreamParser struct {
	buf          strings.Builder
	inCall       bool
	inResult     bool
	totalCalls   int
}

// FeedResult is what a single Feed call returns.
type FeedResult struct {
	Text      string
	ToolCalls []ToolCall
}

// Feed consumes a delta and returns everything ready to emit.
func (p *StreamParser) Feed(delta string) FeedResult {
	if delta == "" {
		return FeedResult{}
	}
	p.buf.WriteString(delta)
	return p.drain()
}

// Flush returns anything held back (malformed dangling tags become literal text).
func (p *StreamParser) Flush() FeedResult {
	remaining := p.buf.String()
	p.buf.Reset()
	if p.inCall {
		p.inCall = false
		return FeedResult{Text: tcOpen + remaining}
	}
	if p.inResult {
		p.inResult = false
		return FeedResult{}
	}
	return FeedResult{Text: remaining}
}

func (p *StreamParser) drain() FeedResult {
	var safe strings.Builder
	var done []ToolCall

	for {
		buf := p.buf.String()

		if p.inResult {
			idx := strings.Index(buf, trClose)
			if idx < 0 {
				break
			}
			rest := buf[idx+len(trClose):]
			p.buf.Reset()
			p.buf.WriteString(rest)
			p.inResult = false
			continue
		}
		if p.inCall {
			idx := strings.Index(buf, tcClose)
			if idx < 0 {
				break
			}
			body := strings.TrimSpace(buf[:idx])
			rest := buf[idx+len(tcClose):]
			p.buf.Reset()
			p.buf.WriteString(rest)
			p.inCall = false

			if tc, ok := parseToolCallBody(body, p.totalCalls); ok {
				done = append(done, tc)
				p.totalCalls++
			} else {
				safe.WriteString(tcOpen)
				safe.WriteString(body)
				safe.WriteString(tcClose)
			}
			continue
		}

		tcIdx := strings.Index(buf, tcOpen)
		trIdx := strings.Index(buf, trPrefix)
		var nextIdx int = -1
		isResult := false
		if tcIdx != -1 && (trIdx == -1 || tcIdx <= trIdx) {
			nextIdx = tcIdx
		} else if trIdx != -1 {
			nextIdx = trIdx
			isResult = true
		}

		if nextIdx == -1 {
			// Hold back a tail that could be the start of either tag.
			holdLen := 0
			for _, prefix := range []string{tcOpen, trPrefix} {
				max := len(prefix) - 1
				if max > len(buf) {
					max = len(buf)
				}
				for l := max; l > 0; l-- {
					if strings.HasSuffix(buf, prefix[:l]) {
						if l > holdLen {
							holdLen = l
						}
						break
					}
				}
			}
			emit := len(buf) - holdLen
			if emit > 0 {
				safe.WriteString(buf[:emit])
			}
			p.buf.Reset()
			if holdLen > 0 {
				p.buf.WriteString(buf[emit:])
			}
			break
		}

		if nextIdx > 0 {
			safe.WriteString(buf[:nextIdx])
		}
		if !isResult {
			rest := buf[nextIdx+len(tcOpen):]
			p.buf.Reset()
			p.buf.WriteString(rest)
			p.inCall = true
		} else {
			// find closing > of the opening tag
			rest := buf[nextIdx:]
			closeAngle := strings.IndexByte(rest, '>')
			if closeAngle == -1 {
				// incomplete open — hold from here
				p.buf.Reset()
				p.buf.WriteString(rest)
				break
			}
			after := rest[closeAngle+1:]
			p.buf.Reset()
			p.buf.WriteString(after)
			p.inResult = true
		}
	}
	return FeedResult{Text: safe.String(), ToolCalls: done}
}

func parseToolCallBody(body string, counter int) (ToolCall, bool) {
	var parsed struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	}
	if err := json.Unmarshal([]byte(body), &parsed); err != nil || parsed.Name == "" {
		return ToolCall{}, false
	}
	args := string(parsed.Arguments)
	if args == "" || args == "null" {
		args = "{}"
	} else {
		// If arguments is a string (escaped JSON), leave as-is; else re-marshal
		// so downstream sees a clean object literal.
		var s string
		if err := json.Unmarshal(parsed.Arguments, &s); err != nil {
			// not a string — already a JSON value; normalise whitespace
			if b, err := json.Marshal(json.RawMessage(parsed.Arguments)); err == nil {
				args = string(b)
			}
		} else {
			args = s
		}
	}
	return ToolCall{
		ID:            fmt.Sprintf("call_%d_%s", counter, time.Now().Format("150405.000")),
		Name:          parsed.Name,
		ArgumentsJSON: args,
	}, true
}

// ParseAll runs a complete (non-streamed) text through the parser in one shot.
// Convenience wrapper for the non-stream response path.
func ParseAll(text string) FeedResult {
	var p StreamParser
	a := p.Feed(text)
	b := p.Flush()
	return FeedResult{
		Text:      a.Text + b.Text,
		ToolCalls: append(a.ToolCalls, b.ToolCalls...),
	}
}
