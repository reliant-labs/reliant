package buildlog

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestStepStreamsOutsideASession(t *testing.T) {
	stdout, stderr, end := Step("docker build api")
	if stdout != os.Stdout || stderr != os.Stderr {
		t.Fatal("outside a session a step must stream to the terminal, as it always did")
	}
	end(errors.New("ignored")) // no session: nothing to print, and no panic
}

// The log can hold credentials a build tool printed: it is the invoking
// user's alone, including a log an earlier run left readable by others.
func TestBeginLogIsPrivate(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX file modes")
	}
	path := filepath.Join(t.TempDir(), "logs", "prod", "deploy.log")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	s, err := Begin(path, func() io.Writer { return io.Discard })
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.End() }()
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if mode := fi.Mode().Perm(); mode != 0o600 {
		t.Fatalf("deploy log mode = %o, want 600", mode)
	}
}

func TestQuietSession(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".forge", "logs", "prod", "deploy.log")
	var term bytes.Buffer
	s, err := Begin(path, func() io.Writer { return &term })
	if err != nil {
		t.Fatal(err)
	}
	clock := time.Unix(0, 0)
	s.now = func() time.Time { return clock }
	defer func() { _ = s.End() }()

	// A step that succeeds: one line on the terminal, everything in the file.
	out, errw, end := Step("web: npm run build")
	fmt.Fprint(out, "▲ Next.js 15\n  Creating an optimized production build ...\n")
	fmt.Fprint(errw, "progress 10%\rprogress 100%\n")
	clock = clock.Add(23400 * time.Millisecond)
	end(nil)

	// A step that fails: its own last lines and the file's path.
	out, _, end = Step("docker build api")
	for i := 1; i <= TailLines+5; i++ {
		fmt.Fprintf(out, "step %d\n", i)
	}
	fmt.Fprint(out, "ERROR: failed to solve: process \"/bin/sh -c go build\" did not complete") // no newline
	clock = clock.Add(2 * time.Second)
	end(errors.New("exit status 1"))
	end(errors.New("twice")) // idempotent

	got := term.String()
	for _, want := range []string{
		"Build steps (full output: " + path,
		"  ✓ web: npm run build (23.4s)\n",
		"  ✗ docker build api failed after 2s — its last 40 lines:\n",
		"    ERROR: failed to solve",
		"    full output: " + path + "\n",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("terminal lacks %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "Creating an optimized") || strings.Contains(got, "step 5\n") {
		t.Errorf("a step's own output reached the terminal beyond its failing tail:\n%s", got)
	}
	if strings.Count(got, "Build steps (") != 1 || strings.Count(got, "✗") != 1 {
		t.Errorf("the header or the failure printed more than once:\n%s", got)
	}
	if !s.Failed() {
		t.Error("the session does not report its failed step")
	}

	if err := s.End(); err != nil {
		t.Fatal(err)
	}
	log, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"[web: npm run build] ▲ Next.js 15\n",
		"[web: npm run build] progress 100%\n",
		"[docker build api] step 1\n",
		"── docker build api: FAILED (2s): exit status 1",
	} {
		if !strings.Contains(string(log), want) {
			t.Errorf("log lacks %q:\n%s", want, log)
		}
	}
	// After End, steps stream again.
	if o, _, _ := Step("x"); o != os.Stdout {
		t.Error("a step after End did not stream")
	}
}

// Steps that run at once stay apart in the log: every line carries its label.
func TestConcurrentStepsKeepTheirLabels(t *testing.T) {
	path := filepath.Join(t.TempDir(), "deploy.log")
	s, err := Begin(path, func() io.Writer { return io.Discard })
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for _, label := range []string{"a", "b", "c"} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			out, _, end := Step(label)
			for i := 0; i < 200; i++ {
				fmt.Fprintf(out, "%s line %d\n", label, i)
			}
			end(nil)
		}()
	}
	wg.Wait()
	_ = s.End()
	data, _ := os.ReadFile(path)
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		if strings.HasPrefix(line, "──") {
			continue
		}
		label := strings.TrimPrefix(strings.SplitN(line, "]", 2)[0], "[")
		if !strings.HasPrefix(strings.SplitN(line, "] ", 2)[1], label+" line ") {
			t.Fatalf("a line landed under another step's label: %q", line)
		}
	}
}

func TestSessionNoStepsPrintsNothing(t *testing.T) {
	var term bytes.Buffer
	s, err := Begin(filepath.Join(t.TempDir(), "deploy.log"), func() io.Writer { return &term })
	if err != nil {
		t.Fatal(err)
	}
	_ = s.End()
	if term.Len() != 0 || s.Failed() {
		t.Errorf("a session that ran no step printed %q (failed=%v)", term.String(), s.Failed())
	}
}
