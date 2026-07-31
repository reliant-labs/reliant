// Copyright (c) 2025 Reliant Labs
package daemonruntime

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/toolexec/bootstrap"
	"github.com/reliant-labs/reliant/internal/toolexec/daemonstate"
)

// testBootstrapConfig is a client-mode config that passes Validate and binds
// no port, so Start reaches the data-dir guard and the run loop without
// touching the machine's real daemon ports.
func testBootstrapConfig(dataDir string) bootstrap.DaemonBootstrapConfig {
	return bootstrap.DaemonBootstrapConfig{
		AuthToken: "rlnt_pat_test",
		GRPCURL:   "http://127.0.0.1:1",
		TLSMode:   bootstrap.TLSModeH2C,
		DataDir:   dataDir,
	}
}

type senderFunc func(*reliantv1.DaemonMessage) error

func (f senderFunc) Send(msg *reliantv1.DaemonMessage) error { return f(msg) }

// A gateway that has stopped reading blocks stream.Send indefinitely — the
// write goes into an io.Pipe feeding the request body, and nothing drains it.
// runSender is then stuck inside Send and can never reach its shutdown case, so
// an unbounded drain in session teardown wedges the process: SIGTERM lands, the
// daemon prints "Shutting down", and it never exits. Teardown must give up.
func TestDrainSenderIsBoundedWhenTheStreamWriteBlocks(t *testing.T) {
	d := &daemonClient{
		sendCh:      make(chan *reliantv1.DaemonMessage, 1),
		sendDone:    make(chan struct{}),
		sessionDone: make(chan struct{}),
	}

	inSend := make(chan struct{})
	release := make(chan struct{})
	defer close(release)

	go d.runSender(senderFunc(func(*reliantv1.DaemonMessage) error {
		close(inSend)
		<-release // a peer that never reads
		return nil
	}))

	d.sendCh <- &reliantv1.DaemonMessage{}
	<-inSend // runSender is now inside Send and cannot observe sessionDone

	start := time.Now()
	stopped := d.drainSender(200 * time.Millisecond)
	elapsed := time.Since(start)

	require.False(t, stopped, "the sender is wedged — teardown must report that, not wait it out")
	require.Less(t, elapsed, 5*time.Second, "teardown must not outlive its own deadline")
}

// The ordinary case still waits for the sender, so a session that ends cleanly
// does not abandon queued writes.
func TestDrainSenderWaitsForACooperativeSender(t *testing.T) {
	d := &daemonClient{
		sendCh:      make(chan *reliantv1.DaemonMessage, 1),
		sendDone:    make(chan struct{}),
		sessionDone: make(chan struct{}),
	}
	go d.runSender(senderFunc(func(*reliantv1.DaemonMessage) error { return nil }))

	require.True(t, d.drainSender(5*time.Second))
	select {
	case <-d.sendDone:
	default:
		t.Fatal("sender must have exited")
	}
}

// A second daemon runtime against a data directory already held by a live one
// must refuse. Two daemons on one host resolve to the same gateway daemonID and
// evict each other on every registration; the cheapest place to stop that is
// before the second one dials.
func TestStartRefusesWhenAnotherDaemonHoldsTheDataDir(t *testing.T) {
	dir := t.TempDir()
	incumbent, err := daemonstate.Acquire(dir)
	require.NoError(t, err)
	t.Cleanup(func() { _ = incumbent.Release() })
	require.NoError(t, daemonstate.Init(dir, "http://localhost:29190", "h2c", false))

	// The guard must fire before anything dials. The deadline is only so this
	// test reports a failed assertion rather than wedging the binary when the
	// guard is absent and Start falls through into its reconnect loop.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	err = Start(ctx, StartOptions{
		BootstrapConfig: testBootstrapConfig(dir),
	})

	require.Error(t, err)
	require.ErrorIs(t, err, daemonstate.ErrLocked)
	require.Contains(t, err.Error(), "reliant daemon stop", "the refusal must say how to resolve it")
	require.Contains(t, err.Error(), "already running")
}

// The claim is released on exit, so restarting is never blocked by the daemon
// that just stopped.
func TestStartReleasesTheClaimWhenItReturns(t *testing.T) {
	dir := t.TempDir()

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Start returns as soon as it reaches the run loop

	_ = Start(ctx, StartOptions{
		BootstrapConfig: testBootstrapConfig(dir),
	})

	lock, err := daemonstate.Acquire(dir)
	require.NoError(t, err, "the claim must not outlive the runtime that took it")
	require.NoError(t, lock.Release())
}
