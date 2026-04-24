package auth

import (
	"errors"
	"testing"
)

// Regression: every phrasing we've observed Cascade / Codeium emit for
// a rate-limit event must match. A miss here means Probe / classify
// silently swallow the event and banhistory loses the record.
func TestIsRateLimitMessage_Positive(t *testing.T) {
	cases := []string{
		"rate limit exceeded, retry after 30s",
		"Rate Limit",
		"rate_limit_exceeded",
		"Too Many Requests",
		"daily quota reached",
		"quota exceeded",
		"daily limit hit",
		"daily cap reached",
		"message limit reached",
		"message limit exceeded",
		"usage limit exceeded",
		"request limit hit",
		"messages limit reached",
		"requests limit exceeded",
		"you have exceeded your daily message limit",
		"Retry-After: 30",
		"please retry after 5 minutes",
	}
	for _, c := range cases {
		if !IsRateLimitMessage(c) {
			t.Errorf("expected match for %q", c)
		}
		if !IsRateLimitError(errors.New(c)) {
			t.Errorf("IsRateLimitError missed %q", c)
		}
	}
}

// Negative cases — make sure we don't over-match and mis-classify other
// errors as rate-limit (which would quarantine healthy accounts).
func TestIsRateLimitMessage_Negative(t *testing.T) {
	cases := []string{
		"",
		"unauthenticated: invalid api key",
		"permission_denied: account not authorized for model",
		"failed_precondition: workspace not initialized",
		"internal error occurred (error ID: abc123)",
		"context canceled",
		"ECONNREFUSED 127.0.0.1:42100",
		"invalid JSON: unexpected token",
		"the model produced an invalid tool call",
		"string to replace not found in file",
	}
	for _, c := range cases {
		if IsRateLimitMessage(c) {
			t.Errorf("false positive on %q", c)
		}
	}
}

func TestIsRateLimitError_NilAndEmpty(t *testing.T) {
	if IsRateLimitError(nil) {
		t.Fatal("nil error must not match")
	}
	if IsRateLimitMessage("") {
		t.Fatal("empty string must not match")
	}
}
