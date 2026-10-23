//go:build unix

package kclrender_test

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"golang.org/x/sys/unix"

	"github.com/reliant-labs/forge/internal/kclrender"
	"github.com/reliant-labs/forge/internal/kclvendor"
)

// TestConcurrentRendersEachKeepTheirOwnRefusal pins that concurrent renders
// in ONE process never corrupt each other's verdict (kclplugin.Serialized).
//
// The KCL runtime captures a refusal through a process-global panic hook
// each evaluation swaps in for its own duration. Overlapping evaluations
// interleave those swaps, so one of them refuses through the DEFAULT hook:
// Rust prints `thread '<unnamed>' panicked at …` to stderr and the refusal
// is never recorded. The render then reports whatever its OS thread last
// recorded — nothing (an empty refusal, or success `{}`), or ANOTHER
// render's message. CI saw both as TestKCLModule_NegativeChecks failing a
// different fixture every run.
//
// Which of those a lost refusal turns into is up to the scheduler, but the
// loss itself is not: every lost refusal is a bare Rust panic on fd 2. So
// the test watches fd 2 — where the runtime writes directly, below Go's
// os.Stderr — and requires none, alongside every render naming its own
// message and no other. Without serialization this load produces bare
// panics on every run (measured 1–5 per run over six runs); with it, none.
func TestConcurrentRendersEachKeepTheirOwnRefusal(t *testing.T) {
	if testing.Short() {
		t.Skip("renders several hundred KCL programs")
	}
	t.Cleanup(kclvendor.SetCacheDirForTest(t.TempDir()))
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "kcl.mod"),
		[]byte("[package]\nname = \"concurrency_probe\"\nedition = \"v0.11.0\"\nversion = \"0.0.1\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// `assert`, the refusal forge's render layer uses (kcl/render.k). Its
	// message exists ONLY in the panic payload, so a lost record loses it.
	// The comprehension is evaluation work, so renders genuinely overlap in
	// the window where the hook is swapped.
	const programs, rounds = 32, 12
	files := make([]string, programs)
	for i := range files {
		files[i] = filepath.Join(dir, fmt.Sprintf("probe_%02d.k", i))
		body := fmt.Sprintf("_work = [n * 2 for n in range(%d)]\n\nassert len(_work) < 0, \"probe-%02d refused\"\n", 20000+i*3000, i)
		if err := os.WriteFile(files[i], []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	type verdict struct {
		probe int
		out   []byte
		err   error
	}
	var (
		mu       sync.Mutex
		verdicts []verdict
	)
	stderr := captureFD2(t, func() {
		var wg sync.WaitGroup
		for g := range programs {
			wg.Add(1)
			go func() {
				defer wg.Done()
				// Each round renders a DIFFERENT program, so a stale
				// thread-local record always names some other probe.
				for r := range rounds {
					i := (g + r*7) % programs
					out, err := kclrender.Run(dir, files[i], nil)
					mu.Lock()
					verdicts = append(verdicts, verdict{probe: i, out: out, err: err})
					mu.Unlock()
				}
			}()
		}
		wg.Wait()
	})

	if n := strings.Count(stderr, "panicked at"); n > 0 {
		t.Errorf("%d refusal(s) panicked through the default hook instead of being recorded — "+
			"concurrent KCL evaluations interleaved the runtime's global panic hook:\n%s", n, stderr)
	}
	for _, v := range verdicts {
		want := fmt.Sprintf("probe-%02d refused", v.probe)
		if v.err == nil {
			t.Errorf("probe %02d: a refused render reported SUCCESS (%s) — its refusal was lost",
				v.probe, strings.TrimSpace(string(v.out)))
			continue
		}
		msg := v.err.Error()
		if !strings.Contains(msg, want) {
			t.Errorf("probe %02d: refusal does not carry its own message %q:\n%s", v.probe, want, msg)
		}
		for j := range programs {
			if j != v.probe && strings.Contains(msg, fmt.Sprintf("probe-%02d refused", j)) {
				t.Errorf("probe %02d: refusal carries probe %02d's message — state leaked between evaluations:\n%s", v.probe, j, msg)
			}
		}
	}
}

// captureFD2 runs fn with file descriptor 2 redirected into a pipe and
// returns what was written to it. It redirects the DESCRIPTOR, not
// os.Stderr: the KCL runtime is native code and writes to fd 2 directly.
func captureFD2(t *testing.T, fn func()) string {
	t.Helper()
	saved, err := unix.Dup(2)
	if err != nil {
		t.Fatalf("dup stderr: %v", err)
	}
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	if err := unix.Dup2(int(w.Fd()), 2); err != nil {
		t.Fatalf("redirect stderr: %v", err)
	}
	var buf bytes.Buffer
	done := make(chan struct{})
	go func() {
		_, _ = io.Copy(&buf, r)
		close(done)
	}()

	func() {
		defer func() {
			// Restore fd 2 before anything else can report a failure.
			_ = unix.Dup2(saved, 2)
			_ = unix.Close(saved)
			_ = w.Close()
		}()
		fn()
	}()
	<-done
	_ = r.Close()
	return buf.String()
}
