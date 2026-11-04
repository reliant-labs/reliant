// Copyright (c) 2025 Reliant Labs

package stopcause

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// fakeProc writes a /proc tree: btime, and per process a stat line and a
// NUL-separated cmdline.
type fakeProc struct {
	root string
	boot time.Time
}

func newFakeProc(t *testing.T, boot time.Time) fakeProc {
	t.Helper()
	root := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(root, "stat"),
		[]byte(fmt.Sprintf("cpu  1 2 3\nbtime %d\nprocesses 9\n", boot.Unix())), 0o600))
	return fakeProc{root: root, boot: boot}
}

func (f fakeProc) add(t *testing.T, pid, ppid int, comm string, started time.Time, argv ...string) {
	t.Helper()
	dir := filepath.Join(f.root, fmt.Sprint(pid))
	require.NoError(t, os.MkdirAll(dir, 0o755))
	ticks := int64(started.Sub(f.boot) / (time.Second / clockTicks))
	// Fields 3..22: state, ppid, then 17 filler fields, then starttime.
	stat := fmt.Sprintf("%d (%s) S %d %s %d 0 0\n", pid, comm, ppid, strings.Repeat("0 ", 17), ticks)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "stat"), []byte(stat), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "cmdline"), []byte(strings.Join(argv, "\x00")+"\x00"), 0o600))
}

// The agent command that sent the signal is still running under the daemon
// when the signal lands, so it shows up both as an in-flight command and as a
// process that could have sent it; unrelated processes are not suspects.
func TestCaptureNamesTheInFlightKiller(t *testing.T) {
	boot := time.Unix(1_700_000_000, 0)
	now := boot.Add(time.Hour)
	started := now.Add(-90 * time.Minute)
	fp := newFakeProc(t, boot)
	fp.add(t, 1, 0, "reliant-init", boot, "/usr/local/bin/reliant", "daemon", "start")
	fp.add(t, 7, 1, "reliant", boot.Add(time.Second), "/usr/local/bin/reliant", "daemon", "start")
	fp.add(t, 40, 7, "bash", now.Add(-3*time.Second), "bash", "-c", "go test ./internal/x/...")
	fp.add(t, 41, 7, "bash", now.Add(-2*time.Second), "bash", "-c", "pkill -f 'reliant daemon'")
	fp.add(t, 42, 41, "pkill", now.Add(-time.Second), "pkill", "-f", "reliant daemon")
	fp.add(t, 90, 1, "sleep (x)", now.Add(-time.Minute), "sleep", "600")

	r := Capture(syscall.SIGTERM, fp.root, 7, started, now)

	require.Equal(t, "terminated", r.Signal)
	require.Equal(t, 90*time.Minute, r.Uptime)
	require.Equal(t, 1, r.Parent.PID)
	require.Contains(t, r.Parent.Cmdline, "reliant daemon start")
	require.Equal(t, []int{40, 41, 42}, pids(r.Descendants), "the daemon's whole tree, breadth-first")
	require.Equal(t, []int{41, 42}, pids(r.Suspects))
	require.Equal(t, time.Second, r.Descendants[2].Age)

	out := r.String()
	require.Contains(t, out, "Stop signal terminated after 1h30m0s up")
	require.Contains(t, out, `cmd="pkill -f reliant daemon"`)
	require.Contains(t, out, "in-flight commands (3):")
}

func TestCaptureBoundsTheDescendantList(t *testing.T) {
	boot := time.Unix(1_700_000_000, 0)
	now := boot.Add(time.Hour)
	fp := newFakeProc(t, boot)
	fp.add(t, 7, 1, "reliant", boot, "reliant", "daemon", "start")
	for i := 0; i < maxDescendants+5; i++ {
		fp.add(t, 100+i, 7, "sleep", now.Add(-time.Second), "sleep", "1")
	}

	r := Capture(syscall.SIGINT, fp.root, 7, boot, now)

	require.Len(t, r.Descendants, maxDescendants)
	require.Equal(t, 5, r.Truncated)
	require.Contains(t, r.String(), "… and 5 more")
}

// Without /proc (macOS, a sandbox) the report still says what it can.
func TestCaptureWithoutProc(t *testing.T) {
	now := time.Now()
	r := Capture(syscall.SIGTERM, filepath.Join(t.TempDir(), "missing"), os.Getpid(), now.Add(-time.Minute), now)

	require.Equal(t, os.Getppid(), r.Parent.PID)
	require.Empty(t, r.Descendants)
	require.Contains(t, r.String(), "processes that could have sent it: none still running")
}

func TestParseStatHandlesParenthesesInTheName(t *testing.T) {
	ppid, start, ok := parseStat([]byte("5 (a) b (c)) S 3 " + strings.Repeat("0 ", 17) + "1234 0\n"))
	require.True(t, ok)
	require.Equal(t, 3, ppid)
	require.Equal(t, int64(1234), start)
}

func pids(ps []Proc) []int {
	out := make([]int, 0, len(ps))
	for _, p := range ps {
		out = append(out, p.PID)
	}
	return out
}
