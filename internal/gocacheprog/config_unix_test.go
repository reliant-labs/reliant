//go:build !windows

package gocacheprog

import (
	"os"
	"strings"
	"testing"
)

func TestCheckTrustedOwnership(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := CheckTrustedOwnership(dir); err != nil {
		t.Fatalf("a 0755 dir of ours: %v", err)
	}
	if err := os.Chmod(dir, 0o775); err != nil {
		t.Fatal(err)
	}
	if err := CheckTrustedOwnership(dir); err == nil || !strings.Contains(err.Error(), "writable") {
		t.Fatalf("a group-writable dir: %v, want a refusal", err)
	}
}
