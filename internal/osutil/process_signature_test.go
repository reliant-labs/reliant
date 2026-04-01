package osutil

import (
	"testing"
	"time"
)

func TestGenerateProcessSignature_Deterministic(t *testing.T) {
	start := time.Unix(1700000000, 123456789)
	cmd := "npm run dev"

	s1 := GenerateProcessSignature(cmd, start)
	s2 := GenerateProcessSignature(cmd, start)

	if s1 == "" {
		t.Fatalf("expected non-empty signature")
	}
	if s1 != s2 {
		t.Fatalf("expected deterministic signature, got %q vs %q", s1, s2)
	}
}

func TestGenerateProcessSignature_ChangesWhenInputsChange(t *testing.T) {
	start := time.Unix(1700000000, 123456789)
	cmd := "npm run dev"

	base := GenerateProcessSignature(cmd, start)
	if base == "" {
		t.Fatalf("expected non-empty signature")
	}

	// Different command
	if got := GenerateProcessSignature("npm run build", start); got == base {
		t.Fatalf("expected signature to change when command changes")
	}

	// Different start time
	if got := GenerateProcessSignature(cmd, start.Add(time.Nanosecond)); got == base {
		t.Fatalf("expected signature to change when start time changes")
	}
}
