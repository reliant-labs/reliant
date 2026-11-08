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

func TestHeartbeatSpeaksWhileAStepRuns(t *testing.T) {
	var out lockedBuf
	stop := HeartbeatEvery(&out, "lint: golangci", 10*time.Millisecond)
	time.Sleep(55 * time.Millisecond)
	stop()
	got := out.String()
	if n := strings.Count(got, "lint: golangci — still running"); n < 2 {
		t.Fatalf("want a repeating line, got %d:\n%s", n, got)
	}
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
	time.Sleep(20 * time.Millisecond)
	stop()
	stop()
	before := out.String()
	time.Sleep(30 * time.Millisecond)
	if after := out.String(); after != before {
		t.Fatalf("wrote after stop:\n%s", strings.TrimPrefix(after, before))
	}
}

func TestHeartbeatNilWriter(t *testing.T) {
	Heartbeat(nil, "x")()
}
