package toolemu

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestParseToolCallBody_StrictWellFormed(t *testing.T) {
	body := `{"name":"Write","arguments":{"file_path":"/tmp/a.py","content":"print('hi')"}}`
	tc, ok := parseToolCallBody(body, 0)
	if !ok {
		t.Fatalf("expected strict parse to succeed")
	}
	if tc.Name != "Write" {
		t.Fatalf("name = %q, want Write", tc.Name)
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(tc.ArgumentsJSON), &m); err != nil {
		t.Fatalf("arguments not valid JSON: %v", err)
	}
	if m["file_path"] != "/tmp/a.py" {
		t.Fatalf("file_path = %v", m["file_path"])
	}
}

// The exact failure mode the user reported: Claude Code's Write tool
// with multi-line content would fall through to literal text on the old
// strict-only parser.
func TestParseToolCallBody_LiteralNewlineInContent(t *testing.T) {
	body := "{\"name\":\"Write\",\"arguments\":{\"file_path\":\"/tmp/a.py\",\"content\":\"line1\nline2\nline3\"}}"
	tc, ok := parseToolCallBody(body, 0)
	if !ok {
		t.Fatalf("expected repair to succeed on literal newline in string value")
	}
	if tc.Name != "Write" {
		t.Fatalf("name = %q, want Write", tc.Name)
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(tc.ArgumentsJSON), &m); err != nil {
		t.Fatalf("repaired arguments not valid JSON: %v\n%s", err, tc.ArgumentsJSON)
	}
	content, _ := m["content"].(string)
	if content != "line1\nline2\nline3" {
		t.Fatalf("content = %q, want line1\\nline2\\nline3", content)
	}
}

func TestParseToolCallBody_LiteralTab(t *testing.T) {
	body := "{\"name\":\"Bash\",\"arguments\":{\"command\":\"grep\tfoo\tbar.txt\"}}"
	tc, ok := parseToolCallBody(body, 0)
	if !ok {
		t.Fatalf("expected repair to succeed on literal tab")
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(tc.ArgumentsJSON), &m); err != nil {
		t.Fatalf("not valid JSON: %v", err)
	}
	if c := m["command"].(string); c != "grep\tfoo\tbar.txt" {
		t.Fatalf("command = %q", c)
	}
}

func TestParseToolCallBody_MarkdownFence(t *testing.T) {
	body := "```json\n{\"name\":\"Read\",\"arguments\":{\"file_path\":\"/x\"}}\n```"
	tc, ok := parseToolCallBody(body, 0)
	if !ok {
		t.Fatalf("expected repair to succeed on markdown fence")
	}
	if tc.Name != "Read" {
		t.Fatalf("name = %q, want Read", tc.Name)
	}
}

func TestParseToolCallBody_FenceAndNewlines(t *testing.T) {
	body := "```\n{\"name\":\"Edit\",\"arguments\":{\"file_path\":\"/x\",\"old_string\":\"a\nb\",\"new_string\":\"c\nd\"}}\n```"
	tc, ok := parseToolCallBody(body, 0)
	if !ok {
		t.Fatalf("expected repair to succeed on fence + literal newlines")
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(tc.ArgumentsJSON), &m); err != nil {
		t.Fatalf("not valid JSON: %v", err)
	}
	if m["old_string"] != "a\nb" || m["new_string"] != "c\nd" {
		t.Fatalf("bad strings: %v %v", m["old_string"], m["new_string"])
	}
}

// Pretty-printed JSON is already valid — make sure repair doesn't
// over-escape whitespace between tokens.
func TestParseToolCallBody_PrettyPrintedStillWorks(t *testing.T) {
	body := `{
  "name": "Read",
  "arguments": {
    "file_path": "/tmp/a.py"
  }
}`
	tc, ok := parseToolCallBody(body, 0)
	if !ok {
		t.Fatalf("expected strict parse to succeed on pretty-printed JSON")
	}
	if tc.Name != "Read" {
		t.Fatalf("name = %q", tc.Name)
	}
}

// Already-escaped \n must not be double-escaped.
func TestParseToolCallBody_DoesNotDoubleEscape(t *testing.T) {
	body := `{"name":"Write","arguments":{"content":"a\nb"}}`
	tc, ok := parseToolCallBody(body, 0)
	if !ok {
		t.Fatalf("strict parse should succeed")
	}
	var m map[string]any
	_ = json.Unmarshal([]byte(tc.ArgumentsJSON), &m)
	if m["content"] != "a\nb" {
		t.Fatalf("content = %q, want a\\nb", m["content"])
	}
}

// The exact "朋友那边报错你这边正常" bug: model emitted an arguments
// value that was a JSON string containing non-JSON prose, which then
// broke Claude Code's partial_json accumulator.
func TestParseToolCallBody_ArgumentsStringWithNonJSONDegradesToEmpty(t *testing.T) {
	body := `{"name":"Read","arguments":"Error: permission denied"}`
	tc, ok := parseToolCallBody(body, 0)
	if !ok {
		t.Fatalf("expected parse to succeed (degraded, not rejected)")
	}
	if tc.ArgumentsJSON != "{}" {
		t.Fatalf("expected args to degrade to {}, got %q", tc.ArgumentsJSON)
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(tc.ArgumentsJSON), &m); err != nil {
		t.Fatalf("args must always be valid JSON object: %v", err)
	}
}

func TestParseToolCallBody_ArgumentsBareArrayDegradesToEmpty(t *testing.T) {
	body := `{"name":"Read","arguments":["/x"]}`
	tc, ok := parseToolCallBody(body, 0)
	if !ok {
		t.Fatalf("expected parse to succeed (degraded)")
	}
	if tc.ArgumentsJSON != "{}" {
		t.Fatalf("expected args to degrade to {}, got %q", tc.ArgumentsJSON)
	}
}

func TestParseToolCallBody_ArgumentsAsStringifiedObjectUnwraps(t *testing.T) {
	// OpenAI-style round-trip: arguments is a JSON string whose value IS a JSON object.
	body := `{"name":"Read","arguments":"{\"file_path\":\"/x\"}"}`
	tc, ok := parseToolCallBody(body, 0)
	if !ok {
		t.Fatalf("expected parse to succeed")
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(tc.ArgumentsJSON), &m); err != nil {
		t.Fatalf("args not valid JSON: %v", err)
	}
	if m["file_path"] != "/x" {
		t.Fatalf("unwrap failed, args = %s", tc.ArgumentsJSON)
	}
}

func TestParseToolCallBody_MissingArgumentsDegradesToEmpty(t *testing.T) {
	body := `{"name":"Read"}`
	tc, ok := parseToolCallBody(body, 0)
	if !ok {
		t.Fatalf("expected parse to succeed even with missing args")
	}
	if tc.ArgumentsJSON != "{}" {
		t.Fatalf("expected args = {}, got %q", tc.ArgumentsJSON)
	}
}

// Guarantee: for ANY tool call we emit, ArgumentsJSON must be parseable
// as a JSON object. This is the contract Claude Code's Anthropic SDK
// depends on at content_block_stop time.
func TestParseToolCallBody_InvariantAlwaysValidJSONObject(t *testing.T) {
	cases := []string{
		`{"name":"A","arguments":{"k":"v"}}`,
		`{"name":"A","arguments":{}}`,
		`{"name":"A","arguments":"{\"k\":\"v\"}"}`,
		`{"name":"A","arguments":"[Error]"}`,
		`{"name":"A","arguments":null}`,
		`{"name":"A","arguments":[]}`,
		`{"name":"A","arguments":42}`,
		`{"name":"A"}`,
	}
	for _, body := range cases {
		tc, ok := parseToolCallBody(body, 0)
		if !ok {
			t.Fatalf("case %q: parse failed", body)
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(tc.ArgumentsJSON), &m); err != nil {
			t.Fatalf("case %q: args not valid JSON object: %v (got %q)", body, err, tc.ArgumentsJSON)
		}
	}
}

func TestParseToolCallBody_TrulyMalformedStillReturnsFalse(t *testing.T) {
	// Missing closing brace — repair can't recover this.
	body := `{"name":"Write","arguments":{"file_path":"/x"`
	if _, ok := parseToolCallBody(body, 0); ok {
		t.Fatalf("expected parse failure on missing brace")
	}
}

// Drive the streaming parser end-to-end with a delta that contains a
// tool_call whose content has literal newlines. Prior behaviour emitted
// the whole block as literal text; the new behaviour should yield exactly
// one ToolCall and no trailing tool_call text.
func TestStreamParser_ToolCallWithLiteralNewlines(t *testing.T) {
	var p StreamParser
	input := "sure, let me write that file:\n<tool_call>{\"name\":\"Write\"," +
		"\"arguments\":{\"file_path\":\"/tmp/a.py\",\"content\":\"line1\nline2\"}}" +
		"</tool_call>\n"
	r := p.Feed(input)
	flush := p.Flush()
	allText := r.Text + flush.Text
	calls := append([]ToolCall(nil), r.ToolCalls...)
	calls = append(calls, flush.ToolCalls...)

	if len(calls) != 1 {
		t.Fatalf("expected 1 tool call, got %d (text=%q)", len(calls), allText)
	}
	if strings.Contains(allText, "<tool_call>") {
		t.Fatalf("tool_call leaked into text: %q", allText)
	}
	if calls[0].Name != "Write" {
		t.Fatalf("call name = %q", calls[0].Name)
	}
}
