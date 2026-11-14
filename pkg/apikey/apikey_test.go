package apikey

import (
	"fmt"
	"strings"
	"testing"
)

// hexAlphabet is the alphabet hex.EncodeToString draws from, and therefore
// the full set of values a hash's last character can take. The tamper below
// has to change every one of them.
const hexAlphabet = "0123456789abcdef"

// tamperHash returns h with its final hex digit replaced by a DIFFERENT one.
//
// The tamper used to be spelled inline as `h[:len(h)-1]+"0"`, which is not a
// tamper at all when the digit is already '0' — it hands Verify the untouched
// hash, Verify correctly accepts it, and the assertion "a tampered hash is
// rejected" fails. One key in sixteen draws such a hash, so the test passed
// fifteen runs out of sixteen and the sixteenth read as a flake.
func tamperHash(h string) string {
	if h == "" {
		return "0"
	}
	repl := byte('0')
	if h[len(h)-1] == repl {
		repl = '1'
	}
	return h[:len(h)-1] + string(repl)
}

func TestGenerate_FormatAndUniqueness(t *testing.T) {
	seen := make(map[string]struct{}, 100)
	for i := 0; i < 100; i++ {
		k, err := Generate()
		if err != nil {
			t.Fatalf("Generate: %v", err)
		}
		if !strings.HasPrefix(k, KeyPrefix) {
			t.Fatalf("key %q missing prefix %q", k, KeyPrefix)
		}
		if len(k) != len(KeyPrefix)+randomLen {
			t.Fatalf("key %q length = %d, want %d", k, len(k), len(KeyPrefix)+randomLen)
		}
		if _, dup := seen[k]; dup {
			t.Fatalf("duplicate key generated: %q", k)
		}
		seen[k] = struct{}{}
	}
}

func TestLookupPrefix(t *testing.T) {
	k, err := Generate()
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	p, err := LookupPrefix(k)
	if err != nil {
		t.Fatalf("LookupPrefix: %v", err)
	}
	if len(p) != LookupPrefixLen {
		t.Errorf("prefix len = %d, want %d", len(p), LookupPrefixLen)
	}
	if got := KeyPrefix + p; !strings.HasPrefix(k, got) {
		t.Errorf("prefix %q is not the head of %q", got, k)
	}

	for _, bad := range []string{"", "fk_", "nope_abcdefgh", "fk_short"} {
		if _, err := LookupPrefix(bad); err == nil {
			t.Errorf("LookupPrefix(%q) expected error", bad)
		}
	}
}

// fixedKey and otherKey are literal, well-formed keys. Hash/Verify are pure
// functions of their input, so drawing the input from crypto/rand bought this
// test nothing and cost it reproducibility: a failure landed on whichever key
// that run happened to draw.
const (
	fixedKey = KeyPrefix + "0000000000000000000000000000000a"
	otherKey = KeyPrefix + "0000000000000000000000000000000b"
)

func TestHashAndVerify(t *testing.T) {
	k := fixedKey
	h := Hash(k)
	if h == k {
		t.Fatal("Hash returned the plaintext")
	}
	if len(h) != 64 { // hex-encoded SHA-256
		t.Errorf("hash len = %d, want 64", len(h))
	}
	if !Verify(k, h) {
		t.Error("Verify(correct key) = false, want true")
	}

	if Verify(otherKey, h) {
		t.Error("Verify(wrong key) = true, want false")
	}
	if Verify(k, "") {
		t.Error("Verify against empty hash = true, want false")
	}
	if Verify(k, tamperHash(h)) {
		t.Error("Verify against tampered hash = true, want false")
	}
}

// TestVerify_RejectsATamperInEveryResidue drives the tamper against a hash
// ending in each of the sixteen hex digits.
//
// The keys are found by walking a FIXED sequence — the residue a random key
// lands in is exactly what used to decide whether this assertion tested
// anything, so the sixteen cases are enumerated rather than sampled. Each case
// asserts BOTH halves: that the tamper actually changed the hash, and that
// Verify then rejects it. The first assertion is the one that matters — a
// tamper that silently no-ops turns the second into a green light for
// verifying a hash against itself.
func TestVerify_RejectsATamperInEveryResidue(t *testing.T) {
	const maxProbe = 100_000
	keyByResidue := make(map[byte]string, len(hexAlphabet))
	for i := 0; len(keyByResidue) < len(hexAlphabet); i++ {
		if i > maxProbe {
			t.Fatalf("only %d of %d final-digit residues found in %d keys",
				len(keyByResidue), len(hexAlphabet), maxProbe)
		}
		k := fmt.Sprintf("%s%032d", KeyPrefix, i)
		h := Hash(k)
		last := h[len(h)-1]
		if _, seen := keyByResidue[last]; !seen {
			keyByResidue[last] = k
		}
	}

	for _, digit := range []byte(hexAlphabet) {
		k := keyByResidue[digit]
		h := Hash(k)
		tampered := tamperHash(h)
		if tampered == h {
			t.Errorf("hash ending in %q: the tamper returned the hash unchanged, so the rejection "+
				"assertion below verifies a hash against itself and can only pass", string(digit))
			continue
		}
		if Verify(k, tampered) {
			t.Errorf("hash ending in %q: Verify accepted a tampered hash", string(digit))
		}
	}
}
