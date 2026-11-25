package oauth2

import (
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestNewStateIsUniqueAndURLSafe(t *testing.T) {
	const n = 256
	seen := make(map[string]int, n)
	for i := 0; i < n; i++ {
		s, err := NewState()
		if err != nil {
			t.Fatalf("NewState call %d: %v", i, err)
		}
		if s == "" {
			t.Fatalf("NewState call %d returned an empty state", i)
		}
		if _, err := base64.RawURLEncoding.DecodeString(s); err != nil {
			t.Errorf("state %q is not unpadded base64url: %v", s, err)
		}
		if strings.ContainsAny(s, "=+/&?# ") {
			t.Errorf("state %q contains characters that need escaping in a query string", s)
		}
		if prev, dup := seen[s]; dup {
			t.Fatalf("NewState returned the same value on calls %d and %d", prev, i)
		}
		seen[s] = i
	}
	if len(seen) != n {
		t.Fatalf("collected %d distinct states from %d calls, want %d", len(seen), n, n)
	}

	// Entropy is derived from the decoded byte length, not asserted as a
	// character count: 32 bytes is the security property.
	for s := range seen {
		raw, err := base64.RawURLEncoding.DecodeString(s)
		if err != nil {
			t.Fatalf("decode state: %v", err)
		}
		if len(raw) < 16 {
			t.Fatalf("state carries only %d bytes of entropy, want at least 16", len(raw))
		}
	}
}

// TestCompareStateAcceptsOnlyExactMatch covers the CSRF check's decision table.
// The mismatch cases are built by mutating a real state, so none of them is a
// hand-written string that happens to differ.
func TestCompareStateAcceptsOnlyExactMatch(t *testing.T) {
	sent, err := NewState()
	if err != nil {
		t.Fatalf("NewState: %v", err)
	}

	if err := CompareState(sent, sent); err != nil {
		t.Errorf("CompareState(s, s) = %v, want nil", err)
	}

	mutations := map[string]string{
		"different value":  mustOtherState(t, sent),
		"truncated":        sent[:len(sent)-1],
		"extra character":  sent + "x",
		"first byte off":   flipFirst(sent),
		"last byte off":    flipLast(sent),
		"case changed":     swapCase(sent),
		"leading space":    " " + sent,
		"trailing newline": sent + "\n",
	}
	for name, received := range mutations {
		if received == sent {
			t.Fatalf("mutation %q did not actually change the state; the case would be vacuous", name)
		}
		err := CompareState(sent, received)
		if err == nil {
			t.Errorf("CompareState rejected nothing for %s", name)
			continue
		}
		if !errors.Is(err, ErrStateMismatch) {
			t.Errorf("CompareState(%s) = %v, want an error wrapping ErrStateMismatch", name, err)
		}
	}
}

// TestCompareStateRejectsEmpty is the guard against a check that cannot fail:
// if empty state were compared normally, a provider echoing no state plus a
// session that stored none would compare "" to "" and pass.
func TestCompareStateRejectsEmpty(t *testing.T) {
	real, err := NewState()
	if err != nil {
		t.Fatalf("NewState: %v", err)
	}
	cases := map[string][2]string{
		"both empty":     {"", ""},
		"sent empty":     {"", real},
		"received empty": {real, ""},
	}
	for name, pair := range cases {
		err := CompareState(pair[0], pair[1])
		if err == nil {
			t.Errorf("CompareState(%s) returned nil; empty state must never satisfy the CSRF check", name)
			continue
		}
		if !errors.Is(err, ErrEmptyState) {
			t.Errorf("CompareState(%s) = %v, want an error wrapping ErrEmptyState", name, err)
		}
		// An empty comparison must not be reported as a match by any route.
		if errors.Is(err, nil) {
			t.Errorf("CompareState(%s) reported success", name)
		}
	}
}

// TestCompareStateIsConstantTime checks the property that makes the comparison
// resistant to a timing oracle: how long it takes must not depend on how much
// of a same-length candidate is correct.
//
// A byte-by-byte comparison with early return leaks the length of the matching
// prefix, which lets an attacker recover state one byte at a time. The test
// compares the cost of an all-wrong candidate against a candidate that shares
// all but the final byte; with strings.Compare or == on a hot path these differ
// measurably, while subtle.ConstantTimeCompare keeps them within noise.
//
// Timing tests are inherently noisy, so the assertion is deliberately loose (a
// 3× ratio) and the measurement is repeated; the goal is to catch a
// short-circuiting rewrite, not to certify the CPU.
func TestCompareStateIsConstantTime(t *testing.T) {
	if testing.Short() {
		t.Skip("timing measurement skipped under -short")
	}

	const width = 64
	sent := strings.Repeat("a", width)
	allWrong := strings.Repeat("b", width)
	almostRight := strings.Repeat("a", width-1) + "b"

	if len(allWrong) != len(sent) || len(almostRight) != len(sent) {
		t.Fatal("candidates must be the same length as the state, or the test measures length handling instead of content handling")
	}
	if CompareState(sent, allWrong) == nil || CompareState(sent, almostRight) == nil {
		t.Fatal("both candidates must be rejected for the timing comparison to be meaningful")
	}

	measure := func(candidate string) time.Duration {
		const iters = 300000
		start := time.Now()
		for i := 0; i < iters; i++ {
			_ = CompareState(sent, candidate)
		}
		return time.Since(start)
	}

	// Best-of-several: take the minimum, which is the least
	// scheduler-contaminated sample.
	best := func(candidate string) time.Duration {
		lo := time.Duration(1 << 62)
		for i := 0; i < 5; i++ {
			if d := measure(candidate); d < lo {
				lo = d
			}
		}
		return lo
	}

	wrong := best(allWrong)
	almost := best(almostRight)
	if wrong == 0 || almost == 0 {
		t.Fatal("timing measurement produced zero duration; the loop was optimized away")
	}

	ratio := float64(almost) / float64(wrong)
	if ratio > 3 || ratio < 1.0/3 {
		t.Errorf("comparison time depends on the matching prefix length: all-wrong %v vs all-but-last-byte-right %v (ratio %.2f); an early-return comparison leaks state via timing", wrong, almost, ratio)
	}
	t.Logf("all-wrong %v, all-but-last-right %v, ratio %.3f", wrong, almost, ratio)
}

// TestStateMismatchIsDistinguishable pins that a caller can branch on CSRF
// specifically, rather than having to string-match a generic failure.
func TestStateMismatchIsDistinguishable(t *testing.T) {
	err := CompareState("expected-state-value", "attacker-supplied-value")
	if !errors.Is(err, ErrStateMismatch) {
		t.Fatalf("mismatch error = %v, not identifiable as ErrStateMismatch", err)
	}
	if errors.Is(err, ErrEmptyState) {
		t.Error("a mismatch must not also report as ErrEmptyState")
	}
	if errors.Is(CompareState("", ""), ErrStateMismatch) {
		t.Error("an empty-state error must be distinguishable from a mismatch")
	}
}

func mustOtherState(t *testing.T, notThis string) string {
	t.Helper()
	for i := 0; i < 100; i++ {
		s, err := NewState()
		if err != nil {
			t.Fatalf("NewState: %v", err)
		}
		if s != notThis {
			return s
		}
	}
	t.Fatal("could not generate a state different from the original")
	return ""
}

func flipFirst(s string) string {
	b := []byte(s)
	b[0] = pickDifferent(b[0])
	return string(b)
}

func flipLast(s string) string {
	b := []byte(s)
	b[len(b)-1] = pickDifferent(b[len(b)-1])
	return string(b)
}

// pickDifferent returns a character from the base64url alphabet that is not c,
// so a mutation is guaranteed to change the value and stay URL-safe.
func pickDifferent(c byte) byte {
	const alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_"
	for i := 0; i < len(alphabet); i++ {
		if alphabet[i] != c {
			return alphabet[i]
		}
	}
	panic(fmt.Sprintf("no alternative character for %q", c))
}

func swapCase(s string) string {
	b := []byte(s)
	for i := range b {
		switch {
		case b[i] >= 'a' && b[i] <= 'z':
			b[i] -= 32
			return string(b)
		case b[i] >= 'A' && b[i] <= 'Z':
			b[i] += 32
			return string(b)
		}
	}
	// No letters to swap; fall back to a guaranteed change.
	return flipFirst(s)
}
