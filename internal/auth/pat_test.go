// Copyright (c) 2025 Reliant Labs
package auth

import (
	"strings"
	"testing"
)

func TestGeneratePAT(t *testing.T) {
	raw, hash, prefix, err := GeneratePAT()
	if err != nil {
		t.Fatalf("GeneratePAT: %v", err)
	}
	if !strings.HasPrefix(raw, PATPrefix) {
		t.Errorf("raw token %q missing %q prefix", raw, PATPrefix)
	}
	if len(raw) != len(PATPrefix)+patRandomBytes {
		t.Errorf("raw token length = %d, want %d", len(raw), len(PATPrefix)+patRandomBytes)
	}
	if hash != HashPAT(raw) {
		t.Errorf("returned hash does not match HashPAT(raw)")
	}
	if hash == raw {
		t.Error("hash equals raw token")
	}
	if len(hash) != 64 {
		t.Errorf("hash length = %d, want 64 (sha256 hex)", len(hash))
	}
	if !strings.HasPrefix(raw, prefix) {
		t.Errorf("display prefix %q does not prefix the raw token", prefix)
	}
	if len(prefix) != len(PATPrefix)+PATTokenPrefixLen {
		t.Errorf("display prefix length = %d, want %d", len(prefix), len(PATPrefix)+PATTokenPrefixLen)
	}

	// Two tokens never collide.
	raw2, _, _, err := GeneratePAT()
	if err != nil {
		t.Fatalf("second GeneratePAT: %v", err)
	}
	if raw == raw2 {
		t.Error("two generated tokens are identical")
	}
}

func TestIsPATFormat(t *testing.T) {
	valid, _, _, err := GeneratePAT()
	if err != nil {
		t.Fatalf("GeneratePAT: %v", err)
	}
	cases := []struct {
		token string
		want  bool
	}{
		{valid, true},
		{PATPrefix + strings.Repeat("x", 40), true},
		{"", false},
		{"not-a-token", false},
		{PATPrefix, false},                        // prefix only
		{PATPrefix + "short", false},              // too short
		{"rlt_" + strings.Repeat("x", 40), false}, // retired parallel-system prefix
		{"Bearer " + valid, false},
	}
	for _, c := range cases {
		if got := IsPATFormat(c.token); got != c.want {
			t.Errorf("IsPATFormat(%q) = %v, want %v", c.token, got, c.want)
		}
	}
}
