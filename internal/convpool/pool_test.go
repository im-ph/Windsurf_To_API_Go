package convpool

import "testing"

// Regression: two callers sending an IDENTICAL message history must NOT
// share a cascade_id. Previously FingerprintBefore ignored the caller,
// so any Claude Code session that happened to ship the same fixed system
// prompt + opening user turn would Checkout the previous caller's entry
// and bleed their conversation context into this turn.
func TestFingerprint_DifferentSaltsPartitionIdenticalHistories(t *testing.T) {
	history := []Message{
		{Role: "system", Content: "You are a helpful assistant."},
		{Role: "user", Content: "Hello"},
		{Role: "assistant", Content: "Hi, how can I help?"},
		{Role: "user", Content: "Tell me about Go"},
	}
	a := FingerprintBefore(history, "1.2.3.4")
	b := FingerprintBefore(history, "5.6.7.8")
	if a == "" || b == "" {
		t.Fatalf("expected non-empty fingerprints, got a=%q b=%q", a, b)
	}
	if a == b {
		t.Fatalf("different salts must produce different fingerprints; both = %q", a)
	}
}

// Same caller, same history → same fingerprint (reuse works within a caller).
func TestFingerprint_SameSaltsSameHistoryMatches(t *testing.T) {
	history := []Message{
		{Role: "user", Content: "Q"},
		{Role: "assistant", Content: "A"},
		{Role: "user", Content: "followup"},
	}
	a := FingerprintBefore(history, "1.2.3.4")
	b := FingerprintBefore(history, "1.2.3.4")
	if a == "" {
		t.Fatal("expected non-empty fingerprint")
	}
	if a != b {
		t.Fatalf("same salt + history should match; a=%q b=%q", a, b)
	}
}

// FingerprintAfter of turn N must equal FingerprintBefore of turn N+1 for
// the same caller — the resume/reuse contract depends on this.
func TestFingerprint_BeforeAfterContract(t *testing.T) {
	turnN := []Message{
		{Role: "user", Content: "first"},
		{Role: "assistant", Content: "reply"},
		{Role: "user", Content: "second"},
	}
	salt := "10.0.0.1"
	after := FingerprintAfter(turnN, "reply-to-second", salt)

	turnNPlus1 := append([]Message{}, turnN...)
	turnNPlus1 = append(turnNPlus1,
		Message{Role: "assistant", Content: "reply-to-second"},
		Message{Role: "user", Content: "third"},
	)
	before := FingerprintBefore(turnNPlus1, salt)
	if after == "" || before == "" {
		t.Fatalf("got empty fingerprints: after=%q before=%q", after, before)
	}
	if after != before {
		t.Fatalf("before/after contract broken:\n  after(N)    = %q\n  before(N+1) = %q", after, before)
	}
}

// Empty salt still works (single-user deployments) but partitions identically.
func TestFingerprint_EmptySaltPermitted(t *testing.T) {
	history := []Message{
		{Role: "user", Content: "x"},
		{Role: "assistant", Content: "y"},
		{Role: "user", Content: "z"},
	}
	a := FingerprintBefore(history, "")
	b := FingerprintBefore(history, "")
	if a == "" || a != b {
		t.Fatalf("empty-salt callers should match each other; a=%q b=%q", a, b)
	}
}
