// Copyright (c) 2025 Reliant Labs
package daemonruntime

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// runningDaemon is a daemonClient.run that a test started, and that is
// stopped, and waited for, before the test's cleanup removes anything.
//
// Every test that starts a daemon must start it through startDaemon. The
// daemon writes into its data dir, a t.TempDir, for as long as it runs: it
// records each RegistrationAck it handles and each stream that ends in
// daemon-state.json, and persists the id the gateway assigns. A gateway
// counts a session when it SENDS the ack, so a test that sees the session it
// waited for, and returns, leaves the daemon still writing. Cancelling alone
// only asks it to stop; when its last write lands during TempDir's RemoveAll
// the cleanup fails with "directory not empty", and when it lands after, it
// recreates the directory and leaks it.
type runningDaemon struct {
	t      *testing.T
	cancel context.CancelFunc
	done   chan struct{}
	err    error
}

// daemonStopTimeout bounds how long a test waits for a cancelled daemon. A
// daemon that outlives it is a bug in shutdown, reported as a test failure
// rather than left to hang the test binary.
const daemonStopTimeout = 30 * time.Second

// startDaemon runs client until the test ends (or stop is called). Its
// t.Cleanup is registered after the client's data dir already exists, so it
// runs first: the daemon has exited before that directory is removed.
func startDaemon(t *testing.T, client *daemonClient) *runningDaemon {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	d := &runningDaemon{t: t, cancel: cancel, done: make(chan struct{})}
	go func() {
		defer close(d.done)
		d.err = client.run(ctx)
	}()
	t.Cleanup(d.stop)
	return d
}

// stop cancels the daemon and waits for run to return. It is idempotent.
func (d *runningDaemon) stop() {
	d.cancel()
	select {
	case <-d.done:
	case <-time.After(daemonStopTimeout):
		d.t.Errorf("the daemon did not stop within %v of its context being cancelled", daemonStopTimeout)
	}
}

// exited is closed when run has returned; err is valid after it.
func (d *runningDaemon) exited() <-chan struct{} { return d.done }

// Every test starts its daemon through startDaemon: a daemonClient.run called
// anywhere else is one nothing waits for.
func TestDaemonsAreStartedThroughStartDaemon(t *testing.T) {
	t.Parallel()
	files, err := filepath.Glob("*_test.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range files {
		if f == "running_daemon_test.go" {
			continue
		}
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for i, line := range strings.Split(string(src), "\n") {
			if strings.Contains(line, "client.run(") {
				t.Errorf("%s:%d starts a daemon with client.run; use startDaemon, which waits for it before the test's directories are removed", f, i+1)
			}
		}
	}
}
