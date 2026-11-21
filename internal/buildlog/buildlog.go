// Package buildlog decides where a build subprocess's output goes: streamed
// to the terminal, or, inside a quiet session, into one log file with a
// single line per step on the terminal.
//
// WHY. `forge env deploy` runs docker builds, dependency installs and
// framework builds, and streamed their output: a deploy of a small app printed
// about 300 lines of Docker and Next.js logs. An agent pays for every line it
// reads, so it skimmed, and missed the lines that mattered (forge eval
// 2026-11-17, feedback #7). Inside a quiet session each step prints one line
// with its timing, the full output goes to a file, and a failing step inlines
// the tail of its own output and names the file, so a failure is as
// diagnosable as before.
//
// A step that runs outside a session streams exactly as it always has: the
// session is the opt-in, and only the commands that start one (`forge env
// deploy` without --verbose) change.
//
//forge:exclude-contract: process-wide output routing for build subprocesses (Begin/Step/Active), deliberately ambient so every exec site joins it without threading a dependency; the *Session methods are the session's own lifecycle (Failed/End/Path) over a local log file, and tests point it at a temp file, so there is nothing to substitute
package buildlog

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// TailLines is how many of a failing step's own lines are inlined.
const TailLines = 40

// Session is a quiet session: every step's output goes to one file.
type Session struct {
	path string
	out  func() io.Writer
	now  func() time.Time

	mu        sync.Mutex
	file      *os.File
	announced sync.Once
	failed    atomic.Bool
}

// Failed reports whether a step of the session failed. False on nil.
func (s *Session) Failed() bool { return s != nil && s.failed.Load() }

// announce names the log file once, above the first step's line, so a
// session that runs no step prints nothing.
func (s *Session) announce(w io.Writer) {
	s.announced.Do(func() {
		fmt.Fprintf(w, "Build steps (full output: %s; --verbose streams it):\n", s.path)
	})
}

var active atomic.Pointer[Session]

// Begin starts a quiet session writing every step's full output to path
// (created, with its directory; truncated). The file is the invoking user's
// alone (0600): build tools can print credentials, and the log outlives the
// deploy. out is where the one line per step goes, read at each write so a
// caller that redirects stdout later is followed. End stops it. A session
// already running is ended first.
func Begin(path string, out func() io.Writer) (*Session, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("build log: %w", err)
	}
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return nil, fmt.Errorf("build log: %w", err)
	}
	// OpenFile keeps an existing file's mode: tighten a log an earlier
	// release left readable by others.
	if err := f.Chmod(0o600); err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("build log: %w", err)
	}
	if out == nil {
		out = func() io.Writer { return os.Stdout }
	}
	s := &Session{path: path, out: out, now: time.Now, file: f}
	if prev := active.Swap(s); prev != nil {
		_ = prev.close()
	}
	return s, nil
}

// End stops the session; later steps stream again. Safe on nil.
func (s *Session) End() error {
	if s == nil {
		return nil
	}
	active.CompareAndSwap(s, nil)
	return s.close()
}

// Path is the session's log file.
func (s *Session) Path() string {
	if s == nil {
		return ""
	}
	return s.path
}

func (s *Session) close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.file == nil {
		return nil
	}
	err := s.file.Close()
	s.file = nil
	return err
}

// Active is the running quiet session, or nil.
func Active() *Session { return active.Load() }

// Step returns where one build step's stdout and stderr go, and end, which
// the caller runs with the step's error when it finishes.
//
// With no session they are os.Stdout and os.Stderr, read now, and end does
// nothing: the step streams as it always did. In a session both go to the log
// file, each line prefixed with the label so steps that run at once stay
// apart, and end prints one line — the label and its duration, or a failure
// with the step's last TailLines lines and the log's path.
func Step(label string) (stdout, stderr io.Writer, end func(error)) {
	s := Active()
	if s == nil {
		return os.Stdout, os.Stderr, func(error) {}
	}
	st := &step{s: s, label: label, start: s.now()}
	st.write([]byte(fmt.Sprintf("── %s\n", label)))
	return st, st, st.end
}

// step is one step's output inside a session.
type step struct {
	s     *Session
	label string
	start time.Time

	mu      sync.Mutex
	partial []byte
	tail    []string
	once    sync.Once
}

// Write splits p into lines, keeps the last TailLines, and appends each to
// the log with the label in front. A trailing partial line waits for the rest.
func (st *step) Write(p []byte) (int, error) {
	st.mu.Lock()
	defer st.mu.Unlock()
	st.partial = append(st.partial, p...)
	for {
		i := bytes.IndexByte(st.partial, '\n')
		if i < 0 {
			break
		}
		st.line(string(st.partial[:i]))
		st.partial = st.partial[i+1:]
	}
	return len(p), nil
}

func (st *step) line(l string) {
	l = strings.TrimRight(l, "\r")
	// A progress bar redraws one line with carriage returns: keep what it
	// last said.
	if i := strings.LastIndexByte(l, '\r'); i >= 0 {
		l = l[i+1:]
	}
	st.tail = append(st.tail, l)
	if len(st.tail) > TailLines {
		st.tail = st.tail[len(st.tail)-TailLines:]
	}
	st.write([]byte("[" + st.label + "] " + l + "\n"))
}

func (st *step) write(b []byte) {
	st.s.mu.Lock()
	defer st.s.mu.Unlock()
	if st.s.file != nil {
		_, _ = st.s.file.Write(b)
	}
}

func (st *step) end(err error) {
	st.once.Do(func() {
		st.mu.Lock()
		if len(st.partial) > 0 {
			st.line(string(st.partial))
			st.partial = nil
		}
		tail := append([]string(nil), st.tail...)
		st.mu.Unlock()

		took := st.s.now().Sub(st.start).Round(100 * time.Millisecond)
		w := st.s.out()
		st.s.announce(w)
		if err == nil {
			st.write([]byte(fmt.Sprintf("── %s: ok (%s)\n", st.label, took)))
			fmt.Fprintf(w, "  ✓ %s (%s)\n", st.label, took)
			return
		}
		st.s.failed.Store(true)
		st.write([]byte(fmt.Sprintf("── %s: FAILED (%s): %v\n", st.label, took, err)))
		fmt.Fprintf(w, "  ✗ %s failed after %s — its last %d lines:\n", st.label, took, len(tail))
		for _, l := range tail {
			fmt.Fprintf(w, "    %s\n", l)
		}
		fmt.Fprintf(w, "    full output: %s\n", st.s.path)
	})
}
