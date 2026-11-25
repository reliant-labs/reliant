package cli

import (
	"strings"
	"testing"
)

// withNodeNoDeprecation must add --no-deprecation for the node-based
// buf plugins (protoc-gen-es) without clobbering caller-provided
// NODE_OPTIONS, and must be idempotent when the flag is already set.
func TestWithNodeNoDeprecation(t *testing.T) {
	find := func(env []string) (string, int) {
		count := 0
		val := ""
		for _, kv := range env {
			if v, ok := strings.CutPrefix(kv, "NODE_OPTIONS="); ok {
				val = v
				count++
			}
		}
		return val, count
	}

	t.Run("absent", func(t *testing.T) {
		t.Setenv("NODE_OPTIONS", "")
		// t.Setenv with "" still defines the var; unset explicitly via
		// the merge behavior check below instead. An empty NODE_OPTIONS
		// must end up as exactly the flag.
		v, n := find(withNodeNoDeprecation())
		if n != 1 || !strings.Contains(v, "--no-deprecation") {
			t.Fatalf("NODE_OPTIONS = %q (%d entries), want --no-deprecation once", v, n)
		}
	})

	t.Run("merges with existing options", func(t *testing.T) {
		t.Setenv("NODE_OPTIONS", "--max-old-space-size=4096")
		v, n := find(withNodeNoDeprecation())
		if n != 1 {
			t.Fatalf("NODE_OPTIONS defined %d times, want 1", n)
		}
		if !strings.Contains(v, "--max-old-space-size=4096") || !strings.Contains(v, "--no-deprecation") {
			t.Fatalf("NODE_OPTIONS = %q, want both the existing option and --no-deprecation", v)
		}
	})

	t.Run("idempotent", func(t *testing.T) {
		t.Setenv("NODE_OPTIONS", "--no-deprecation")
		v, n := find(withNodeNoDeprecation())
		if n != 1 || strings.Count(v, "--no-deprecation") != 1 {
			t.Fatalf("NODE_OPTIONS = %q (%d entries), want the flag exactly once", v, n)
		}
	})
}
