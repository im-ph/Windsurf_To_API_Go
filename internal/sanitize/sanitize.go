// Package sanitize strips server-internal filesystem paths from outgoing text.
// Direct port of src/sanitize.js — the streaming version holds back any tail
// that could be an incomplete prefix of a sensitive literal so paths can't
// leak across chunk boundaries.
package sanitize

import (
	"regexp"
	"strings"
)

// patterns mirrors the JS PATTERNS array. Char class includes backslash
// so Windows-style paths (`C:\home\user\projects\workspace-x\src`) keep
// extending past `\` until whitespace / quote / shell-syntax char closes
// the run. Workspace replacement uses an explicit `<workspace>` marker
// rather than `.` — the dot would feed back as a relative path and
// trigger Cascade tool-call echo loops.
var patterns = []struct {
	re   *regexp.Regexp
	repl string
}{
	{regexp.MustCompile(`/tmp/windsurf-workspace(?:[/\\][^\s"'` + "`" + `<>)}\],*;]*)?`), "<workspace>"},
	// N5 — Windows / mixed-separator user-projects-workspace pattern (#86).
	// Covers /home/user/projects/workspace-x, \home\user\projects\workspace-x,
	// C:\home\user\..., and GLM-style mixed C:\home/user/...
	{regexp.MustCompile(`(?:[A-Za-z]:)?[/\\]home[/\\]user[/\\]projects[/\\]workspace-[a-z0-9]+(?:[/\\][^\s"'` + "`" + `<>)}\],*;]*)?`), "<workspace>"},
	{regexp.MustCompile(`/opt/windsurf(?:[/\\][^\s"'` + "`" + `<>)}\],*;]*)?`), "[internal]"},
	{regexp.MustCompile(`/root/WindsurfAPI(?:[/\\][^\s"'` + "`" + `<>)}\],*;]*)?`), "[internal]"},
	// N5 — Cascade upstream injects these XML blocks into its system
	// prompt to describe sandbox state; the model sometimes echoes them
	// verbatim into output, leaking server-internal sandbox metadata.
	{regexp.MustCompile(`(?s)<workspace_information>.*?</workspace_information>`), ""},
	{regexp.MustCompile(`(?s)<workspace_layout>.*?</workspace_layout>`), ""},
	{regexp.MustCompile(`(?s)<user_information>.*?</user_information>`), ""},
}

var sensitiveLiterals = []string{
	"/tmp/windsurf-workspace",
	"/home/user/projects/workspace-",
	"\\home\\user\\projects\\workspace-",
	"/opt/windsurf",
	"/root/WindsurfAPI",
}

// Text applies every redaction in one pass. Empty input returns "".
func Text(s string) string {
	if s == "" {
		return s
	}
	out := s
	for _, p := range patterns {
		out = p.re.ReplaceAllString(out, p.repl)
	}
	return out
}

// Stream is the incremental sanitizer for streamed deltas.
type Stream struct {
	buf strings.Builder
}

// Feed adds a delta and returns everything safe to emit so far.
func (s *Stream) Feed(delta string) string {
	if delta == "" {
		return ""
	}
	s.buf.WriteString(delta)
	cut := safeCutPoint(s.buf.String())
	if cut == 0 {
		return ""
	}
	full := s.buf.String()
	safe := full[:cut]
	rest := full[cut:]
	s.buf.Reset()
	s.buf.WriteString(rest)
	return Text(safe)
}

// Flush emits any held tail, sanitized, and resets state.
func (s *Stream) Flush() string {
	out := Text(s.buf.String())
	s.buf.Reset()
	return out
}

// safeCutPoint finds the largest index such that buf[:cut] contains no match
// that could still extend. See the JS comment for the two held-back cases.
func safeCutPoint(buf string) int {
	cut := len(buf)

	// (1) unterminated full literal — path body ran to EOF
	for _, lit := range sensitiveLiterals {
		searchFrom := 0
		for searchFrom < len(buf) {
			idx := strings.Index(buf[searchFrom:], lit)
			if idx < 0 {
				break
			}
			idx += searchFrom
			end := idx + len(lit)
			for end < len(buf) && isPathBody(buf[end]) {
				end++
			}
			if end == len(buf) {
				if idx < cut {
					cut = idx
				}
				break
			}
			searchFrom = end + 1
		}
	}

	// (2) partial-prefix tail
	for _, lit := range sensitiveLiterals {
		max := len(lit) - 1
		if max > len(buf) {
			max = len(buf)
		}
		for plen := max; plen > 0; plen-- {
			if strings.HasSuffix(buf, lit[:plen]) {
				start := len(buf) - plen
				if start < cut {
					cut = start
				}
				break
			}
		}
	}
	return cut
}

func isPathBody(c byte) bool {
	switch c {
	case ' ', '\t', '\n', '\r', '"', '\'', '`', '<', '>', ')', '}', ']', ',', '*', ';':
		return false
	}
	return true
}
