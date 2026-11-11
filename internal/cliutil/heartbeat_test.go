package cliutil

import (
	"strings"
	"sync"
	"testing"
	"time"
)

type lockedBuf struct {
	mu sync.Mutex
	sb strings.Builder
}

func (b *lockedBuf) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.sb.Write(p)
}

func (b *lockedBuf) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.sb.String()
}

// waitForDraws waits until out holds at least n occurrences of line, failing
// the test after 30s. It returns as soon as they are there, so a passing
// test pays only for the heartbeat itself. (testkit.Eventually will replace
// it.)
func waitForDraws(t *testing.T, out *lockedBuf, line string, n int) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for strings.Count(out.String(), line) < n {
		if time.Now().After(deadline) {
			t.Fatalf("want %d draw(s) of %q, got:\n%q", n, line, out.String())
		}
		time.Sleep(time.Millisecond)
	}
}

func TestHeartbeatSpeaksWhileAStepRuns(t *testing.T) {
	var out lockedBuf
	stop := HeartbeatEvery(&out, "lint: golangci", 10*time.Millisecond)
	defer stop()
	// Lines at 10, 40, 70, … ms: wait for the second, however long a loaded
	// machine takes to schedule it.
	waitForDraws(t, &out, "lint: golangci — still running", 2)
}

// A step inside the first interval prints nothing: a clean --quiet run stays
// one line, and a fast generate stays as quiet as it was.
func TestHeartbeatIsSilentForAFastStep(t *testing.T) {
	var out lockedBuf
	Heartbeat(&out, "x")()
	if out.String() != "" {
		t.Fatalf("a fast step printed %q", out.String())
	}
}

// The default interval is the one agents with a no-output cut-off rely on.
func TestHeartbeatDefaultIsUnderAMinute(t *testing.T) {
	if defaultHeartbeatInterval > 20*time.Second {
		t.Fatalf("default interval %s is too long to read as alive", defaultHeartbeatInterval)
	}
}

// After stop returns, nothing more is written, and a second stop is harmless.
func TestHeartbeatStopIsFinalAndIdempotent(t *testing.T) {
	var out lockedBuf
	stop := HeartbeatEvery(&out, "x", 5*time.Millisecond)
	waitForDraws(t, &out, "x", 1)
	stop()
	stop()
	before := out.String()
	// forge:lint-disable-next-line forge-test-fixed-sleep: a negative wait — gives a heartbeat that outlived stop the chance to write; a correct stop passes however long this takes
	time.Sleep(30 * time.Millisecond)
	if after := out.String(); after != before {
		t.Fatalf("wrote after stop:\n%s", strings.TrimPrefix(after, before))
	}
}

func TestHeartbeatNilWriter(t *testing.T) {
	Heartbeat(nil, "x")()
}

// Off a terminal the repeats are sparse — one every 45s after the first at
// 15s — but never a silent minute, which an agent's no-output cut-off reads
// as a hang.
func TestLineHeartbeatIsSparseButNeverSilentForAMinute(t *testing.T) {
	if got := lineBeatWait(0, defaultHeartbeatInterval); got != 15*time.Second {
		t.Errorf("first line after %s, want 15s", got)
	}
	for n := 1; n < 4; n++ {
		got := lineBeatWait(n, defaultHeartbeatInterval)
		if got < 30*time.Second || got >= time.Minute {
			t.Errorf("repeat %d after %s, want sparse (>=30s) and under a minute", n, got)
		}
	}
}

// On a terminal a heartbeat is ONE line redrawn in place: no newline is ever
// written, every draw starts at the line's start, and stop erases it.
func TestTerminalHeartbeatIsOneUpdatingLineErasedAtStop(t *testing.T) {
	var out lockedBuf
	stop := heartbeat(&out, "generate: buf generate", 5*time.Millisecond, true)
	waitForDraws(t, &out, "generate: buf generate", 1)
	termStatus.redraw() // a refresh, as the 1s ticker would
	stop()
	got := out.String()
	if strings.Contains(got, "\n") {
		t.Fatalf("a terminal heartbeat wrote a newline:\n%q", got)
	}
	if n := strings.Count(got, "generate: buf generate"); n < 2 {
		t.Fatalf("want the line drawn and redrawn, got %d draw(s): %q", n, got)
	}
	if !strings.HasPrefix(got, "\r") {
		t.Fatalf("a draw does not start at the line's start: %q", got)
	}
	// The erase is the last write: spaces over the line, cursor back.
	if !strings.HasSuffix(got, " \r") {
		t.Fatalf("stop did not erase the line: %q", got)
	}
}

// Nested heartbeats on a terminal share the one line: the inner one takes it
// over, the outer one is redrawn when the inner stops, and the line is gone
// when both have.
func TestNestedTerminalHeartbeatsShareOneLine(t *testing.T) {
	var out lockedBuf
	stopOuter := heartbeat(&out, "project new: generate code", time.Millisecond, true)
	waitForDraws(t, &out, "project new: generate code", 1)
	stopInner := heartbeat(&out, "generate: buf generate", time.Millisecond, true)
	waitForDraws(t, &out, "generate: buf generate", 1)
	before := len(out.String())
	stopInner()
	afterInner := out.String()[before:]
	if !strings.Contains(afterInner, "project new: generate code") {
		t.Errorf("the outer heartbeat was not redrawn when the inner stopped: %q", afterInner)
	}
	stopOuter()
	got := out.String()
	if strings.Contains(got, "\n") {
		t.Fatalf("nested terminal heartbeats wrote a newline:\n%q", got)
	}
	termStatus.mu.Lock()
	drawn, active := termStatus.drawn, len(termStatus.beats)
	termStatus.mu.Unlock()
	if drawn != 0 || active != 0 {
		t.Fatalf("after both stopped: %d cells still drawn, %d beats active", drawn, active)
	}
}

// A status line never wraps: a wrapped line cannot be redrawn with "\r".
func TestFitCellsCutsToTheTerminal(t *testing.T) {
	long := "⏳ " + strings.Repeat("x", 200)
	got := fitCells(long, 40)
	if cells(got) > 40 || !strings.HasSuffix(got, "…") {
		t.Fatalf("fitCells = %q (%d cells), want <= 40 ending in …", got, cells(got))
	}
	if short := "⏳ lint"; fitCells(short, 40) != short {
		t.Fatalf("fitCells changed a line that fits")
	}
}
