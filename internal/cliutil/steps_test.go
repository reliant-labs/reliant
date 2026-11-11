package cliutil

import (
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// terminalOutput runs f with os.Stdout and os.Stderr pointed at one file —
// the terminal a StepRunner prints its lines to — and returns what landed
// there.
func terminalOutput(t *testing.T, f func()) string {
	t.Helper()
	term, err := os.CreateTemp(t.TempDir(), "terminal")
	if err != nil {
		t.Fatal(err)
	}
	// Closed before t.TempDir's cleanup: Windows will not remove an open file.
	defer func() { _ = term.Close() }()
	prevOut, prevErr := os.Stdout, os.Stderr
	os.Stdout, os.Stderr = term, term
	defer func() { os.Stdout, os.Stderr = prevOut, prevErr }()
	f()
	os.Stdout, os.Stderr = prevOut, prevErr
	b, err := os.ReadFile(term.Name())
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func readLog(t *testing.T, r *StepRunner) string {
	t.Helper()
	b, err := os.ReadFile(r.LogPath())
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// A step's own lines, its log lines, and a child process's stdout and stderr
// all go to the log; the terminal gets one line with the step's summary.
func TestStepRunnerCapturesEverythingAndPrintsOneLine(t *testing.T) {
	r := NewStepRunner("forge project new", false)
	t.Cleanup(func() { _ = r.Close(); _ = os.Remove(r.path) })

	term := terminalOutput(t, func() {
		err := r.Run(Step{
			Title:   "frontend dependencies",
			Header:  "🔧 Installing frontend dependencies...",
			Summary: func(out string) string { return fmt.Sprintf("%d lines seen", strings.Count(out, "\n")) },
			Run: func() error {
				fmt.Println("[WARN] deprecated eslint@9.39.5")
				fmt.Fprintln(os.Stderr, "Downloading next@16.4.5: 0.00 B/43.65 MB")
				log.Print("a log line")
				if runtime.GOOS != "windows" {
					c := exec.Command("sh", "-c", "echo child-stdout; echo child-stderr >&2")
					c.Stdout, c.Stderr = os.Stdout, os.Stderr
					if err := c.Run(); err != nil {
						return err
					}
				}
				return nil
			},
		})
		if err != nil {
			t.Errorf("Run: %v", err)
		}
	})

	if lines := strings.Split(strings.TrimSpace(term), "\n"); len(lines) != 1 {
		t.Fatalf("the terminal got %d lines, want the step's one:\n%s", len(lines), term)
	}
	if !strings.HasPrefix(term, "  ✓ frontend dependencies — ") || !strings.Contains(term, "lines seen (") {
		t.Errorf("step line = %q", term)
	}
	for _, noise := range []string{"[WARN]", "Downloading", "a log line", "child-", "🔧"} {
		if strings.Contains(term, noise) {
			t.Errorf("the terminal got the step's output %q:\n%s", noise, term)
		}
	}
	logText := readLog(t, r)
	want := []string{"[WARN] deprecated eslint@9.39.5", "Downloading next@16.4.5", "a log line", "── frontend dependencies ──", "frontend dependencies: done in"}
	if runtime.GOOS != "windows" {
		want = append(want, "child-stdout", "child-stderr")
	}
	for _, w := range want {
		if !strings.Contains(logText, w) {
			t.Errorf("the log lacks %q:\n%s", w, logText)
		}
	}
}

// A failed step prints its tail — the last lines, not the first — and where
// the rest is, and returns the step's error.
func TestStepRunnerFailurePrintsTheTailAndTheLogPath(t *testing.T) {
	r := NewStepRunner("forge project new", false)
	t.Cleanup(func() { _ = r.Close(); _ = os.Remove(r.path) })
	boom := errors.New("pnpm install failed")

	var got error
	term := terminalOutput(t, func() {
		got = r.Run(Step{
			Title: "frontend dependencies",
			Run: func() error {
				for i := 1; i <= 40; i++ {
					fmt.Printf("line %02d\n", i)
				}
				fmt.Println("ERR_PNPM_FETCH_404  GET https://registry.npmjs.org/nxet: Not Found - 404")
				return boom
			},
		})
	})
	if !errors.Is(got, boom) {
		t.Fatalf("Run returned %v, want the step's error", got)
	}
	if !strings.HasPrefix(term, "  ✗ frontend dependencies (") {
		t.Errorf("no failure line:\n%s", term)
	}
	if !strings.Contains(term, "    │ ERR_PNPM_FETCH_404") || !strings.Contains(term, "    │ line 40") {
		t.Errorf("the tail lacks the last lines:\n%s", term)
	}
	if strings.Contains(term, "line 01") || strings.Contains(term, "line 25") {
		t.Errorf("the tail is not a tail (%d lines max):\n%s", stepTailLines, term)
	}
	if n := strings.Count(term, "│"); n != stepTailLines {
		t.Errorf("tail has %d lines, want %d:\n%s", n, stepTailLines, term)
	}
	if !strings.Contains(term, "full output: "+displayPath(r.LogPath())) {
		t.Errorf("the failure does not say where the full output is (%s):\n%s", r.LogPath(), term)
	}
	if !strings.Contains(readLog(t, r), "line 01") {
		t.Error("the log lost the start of the output")
	}
}

// A warning forge printed (⚠️) is repeated under the step line, once, so
// capturing never hides one.
func TestStepRunnerRepeatsWarnings(t *testing.T) {
	r := NewStepRunner("forge project new", false)
	t.Cleanup(func() { _ = r.Close(); _ = os.Remove(r.path) })
	term := terminalOutput(t, func() {
		_ = r.Run(Step{Title: "generate code", Run: func() error {
			fmt.Println("🔨 Running buf generate...")
			fmt.Println("  ⚠️  observability: dashboards skipped")
			fmt.Fprintln(os.Stderr, "⚠️  observability: dashboards skipped")
			return nil
		}})
	})
	if n := strings.Count(term, "⚠️  observability: dashboards skipped"); n != 1 {
		t.Errorf("warning shown %d times, want 1:\n%s", n, term)
	}
	if strings.Contains(term, "buf generate") {
		t.Errorf("a routine line reached the terminal:\n%s", term)
	}
}

// Verbose streams the step's output as it always did, with the header, and
// captures nothing.
func TestStepRunnerVerboseStreams(t *testing.T) {
	r := NewStepRunner("forge project new", true)
	term := terminalOutput(t, func() {
		_ = r.Run(Step{Title: "go mod tidy", Header: "🔧 Running go mod tidy...", Run: func() error {
			fmt.Println("go: finding module for package x")
			return nil
		}})
	})
	for _, w := range []string{"🔧 Running go mod tidy...", "go: finding module for package x", "✓ go mod tidy ("} {
		if !strings.Contains(term, w) {
			t.Errorf("verbose output lacks %q:\n%s", w, term)
		}
	}
	if r.LogPath() != "" {
		t.Errorf("verbose captured to %s", r.LogPath())
	}
}

// The process's output is back where it was after a step, even one that
// panicked.
func TestStepRunnerRestoresOutputAfterAPanic(t *testing.T) {
	r := NewStepRunner("forge project new", false)
	t.Cleanup(func() { _ = r.Close(); _ = os.Remove(r.path) })
	prevOut, prevErr, prevLog := os.Stdout, os.Stderr, log.Writer()
	func() {
		defer func() { _ = recover() }()
		_ = r.Run(Step{Title: "boom", Run: func() error { panic("boom") }})
	}()
	if os.Stdout != prevOut || os.Stderr != prevErr || log.Writer() != prevLog {
		t.Fatal("a panicking step left the process's output redirected")
	}
}

// The log starts in the temp directory and moves into the project once there
// is one; later steps append to it there.
func TestStepRunnerMoveLogKeepsAppending(t *testing.T) {
	r := NewStepRunner("forge project new", false)
	t.Cleanup(func() { _ = r.Close() })
	terminalOutput(t, func() {
		_ = r.Run(Step{Title: "project files", Run: func() error { fmt.Println("first step"); return nil }})
	})
	tmp := r.LogPath()
	dst := filepath.Join(t.TempDir(), "shop", ".forge", "logs", "project-new.log")
	// Cleanups run last-registered first: close the moved log before
	// t.TempDir removes its directory (Windows will not remove an open file).
	t.Cleanup(func() { _ = r.Close() })
	if err := r.MoveLog(dst); err != nil {
		t.Fatal(err)
	}
	terminalOutput(t, func() {
		_ = r.Run(Step{Title: "go mod tidy", Run: func() error { fmt.Println("second step"); return nil }})
	})
	if r.LogPath() != dst {
		t.Fatalf("LogPath = %s, want %s", r.LogPath(), dst)
	}
	got := readLog(t, r)
	if !strings.Contains(got, "first step") || !strings.Contains(got, "second step") {
		t.Errorf("the moved log lacks a step:\n%s", got)
	}
	if _, err := os.Stat(tmp); !os.IsNotExist(err) {
		t.Errorf("the temp log %s is still there (stat: %v)", tmp, err)
	}
}

// TailLines leaves what a terminal would have shown: the last state of a
// progress bar, no colour codes, no blank lines.
func TestTailLines(t *testing.T) {
	out := "Progress: resolved 1\rProgress: resolved 631, done\n\n\x1b[33m[WARN]\x1b[39m peers\r\nlast\n"
	got := TailLines(out, 10)
	want := []string{"Progress: resolved 631, done", "[WARN] peers", "last"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("TailLines = %q, want %q", got, want)
	}
	if got := TailLines(out, 1); len(got) != 1 || got[0] != "last" {
		t.Fatalf("TailLines(n=1) = %q", got)
	}
}

// Off a terminal nothing is held: output streams as it always did.
func TestHoldOutputOffATerminalHoldsNothing(t *testing.T) {
	f, err := os.CreateTemp(t.TempDir(), "out")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.Close() }) // before t.TempDir's cleanup (Windows)
	prev := os.Stdout
	os.Stdout = f
	defer func() { os.Stdout = prev }()
	release := HoldOutputOnTerminal()
	if os.Stdout != f {
		t.Fatal("HoldOutputOnTerminal redirected output that is not a terminal")
	}
	release()
	_, _ = io.WriteString(f, "")
}
