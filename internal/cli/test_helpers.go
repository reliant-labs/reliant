// Shared helpers for cli package tests.
//
// Today this is just `captureStderr`, originally defined inside
// lint_buf_test.go and consumed from generate_rename_check_test.go via
// the shared `cli` package's file-set. Moving it to its own file
// makes the helper trivially discoverable (file name advertises
// what's inside) and avoids the "this helper lives in a random test
// file" code-review trap. New cross-file test helpers should land
// here so the canonical home stays obvious.
//
// FRICTION 2026-06-02: cp-forge codegen-safety agent flagged that
// `captureStderr` was hidden behind a file-name (`lint_buf_test.go`)
// that doesn't advertise its presence — easy to accidentally
// re-declare in a new _test.go file and break the build. Centralising
// the helper here removes the foot-gun.
//
// The file is in the production package (not `_test.go`-scoped)
// because the task asked for that location. The `testing` import is
// dead-code-eliminated by the linker for production builds.

package cli

import (
	"os"
	"strings"
	"testing"
)

// captureStderr redirects os.Stderr to a pipe and returns a Builder
// the caller queries after invoking restore(). Shared with CLI tests
// that need to assert on warning/hint text printed to stderr.
//
// Usage:
//
//	buf, restore := captureStderr(t)
//	doThingThatLogsToStderr()
//	restore()
//	if !strings.Contains(buf.String(), "expected hint") {
//	    t.Fatalf("missing hint: %s", buf.String())
//	}
//
// The reader goroutine drains the pipe until the writer end is closed
// (by restore), so the Builder is safe to read AFTER restore returns.
// Calling restore is mandatory — otherwise os.Stderr stays redirected
// for subsequent tests and they'll print into the dead pipe.
func captureStderr(t *testing.T) (*strings.Builder, func()) {
	t.Helper()
	orig := os.Stderr
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	os.Stderr = w
	buf := &strings.Builder{}
	done := make(chan struct{})
	go func() {
		defer close(done)
		b := make([]byte, 4096)
		for {
			n, rerr := r.Read(b)
			if n > 0 {
				buf.Write(b[:n])
			}
			if rerr != nil {
				return
			}
		}
	}()
	return buf, func() {
		_ = w.Close()
		<-done
		os.Stderr = orig
	}
}
