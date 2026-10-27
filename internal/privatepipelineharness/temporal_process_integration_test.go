//go:build integration

// Copyright (c) 2025 Reliant Labs
package privatepipelineharness

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"go.temporal.io/sdk/client"
	"golang.org/x/sys/unix"
)

const pinnedTemporalExecutableSHA = "eb4f6ddaa41cadf58c7a77aa50c4bd932d6739fcef04ac76c7a679eaff2f5e6a"

func validateTemporalExecutable(path string) error {
	if runtime.GOOS != "linux" || runtime.GOARCH != "amd64" {
		return fmt.Errorf("pinned Temporal fixture supports linux/amd64 only")
	}
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open pinned Temporal executable: %w", err)
	}
	defer f.Close()
	stat, err := f.Stat()
	if err != nil || !stat.Mode().IsRegular() || stat.Mode()&0111 == 0 || stat.Size() > 300<<20 {
		return fmt.Errorf("invalid pinned Temporal executable")
	}
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil || fmt.Sprintf("%x", h.Sum(nil)) != pinnedTemporalExecutableSHA {
		return fmt.Errorf("Temporal executable does not match verified official v1.5.1 binary")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, path, "--version").Output()
	if err != nil || !strings.HasPrefix(string(out), "temporal version 1.5.1 ") {
		return fmt.Errorf("unexpected pinned Temporal executable version")
	}
	return nil
}

// ownedProcess is test infrastructure, not an authorization adapter. The
// command is always started in a fresh group; signals target only that group.
type ownedProcess struct {
	cmd         *exec.Cmd
	done        chan struct{}
	request     chan struct{}
	err         error
	identityErr error
	once        sync.Once
}

type processObservationHooks struct {
	beforeSignals       func()
	afterSignals        func()
	observeExit         func(int) (bool, error)
	reportIdentityError func(error)
}

func startOwnedProcess(t *testing.T, ctx context.Context, command string, args ...string) (*ownedProcess, error) {
	t.Helper()
	return startOwnedProcessObserved(t, ctx, processObservationHooks{}, command, args...)
}

func startOwnedProcessObserved(t *testing.T, ctx context.Context, hooks processObservationHooks, command string, args ...string) (*ownedProcess, error) {
	t.Helper()
	cmd := exec.Command(command, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Stdout, cmd.Stderr = io.Discard, io.Discard
	p := &ownedProcess{cmd: cmd, done: make(chan struct{}), request: make(chan struct{})}
	// Register before Start: readiness failure cannot strand the child.
	t.Cleanup(p.stop)
	if err := cmd.Start(); err != nil {
		p.err = err
		close(p.done)
		return p, err
	}
	go p.ownLifecycle(ctx, hooks)
	t.Cleanup(func() {
		p.stop()
		if p.identityErr != nil {
			if hooks.reportIdentityError != nil {
				hooks.reportIdentityError(p.identityErr)
			} else {
				t.Errorf("owned child identity observation failed; group was NOT signaled and descendant cleanup is unverified: %v", p.identityErr)
			}
		}
	})
	return p, nil
}

// childExited observes without reaping. The direct child reserves its PID and
// process-group identity (including as a zombie) until the SINGLE owner Waits.
func childExited(pid int) (bool, error) {
	var info unix.Siginfo
	err := unix.Waitid(unix.P_PID, pid, &info, unix.WEXITED|unix.WNOHANG|unix.WNOWAIT, nil)
	return info.Signo != 0, err
}

func (p *ownedProcess) ownLifecycle(ctx context.Context, hooks processObservationHooks) {
	defer close(p.done)
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	observe := childExited
	if hooks.observeExit != nil {
		observe = hooks.observeExit
	}
	for {
		exited, err := observe(p.cmd.Process.Pid)
		if err != nil {
			// No owner Wait has happened, so this direct child cannot have had
			// its identity recycled. Kill ONLY the retained os.Process handle
			// (pidfd on supported Go/Linux), never an unverified integer PGID.
			p.identityErr = err
			if killErr := p.cmd.Process.Kill(); killErr != nil && killErr != os.ErrProcessDone {
				p.identityErr = fmt.Errorf("observation failed and owned leader kill failed: %w", killErr)
			}
			reaped := make(chan error, 1)
			go func() { reaped <- p.cmd.Wait() }()
			select {
			case p.err = <-reaped:
			case <-time.After(3 * time.Second):
				p.identityErr = fmt.Errorf("observation failed; owned leader reap deadline exceeded; descendants unverified")
			}
			return
		}
		if exited {
			break
		}
		select {
		case <-ctx.Done():
			goto stopping
		case <-p.request:
			goto stopping
		case <-ticker.C:
		}
	}
stopping:
	if hooks.beforeSignals != nil {
		hooks.beforeSignals()
	}
	// No Wait has occurred, so PID/PGID cannot have been recycled. Retain
	// that identity through BOTH signals; even a simultaneous leader exit is
	// only a zombie until the final Wait below. KILL also closes surviving
	// descendants before the leader can be reaped.
	_ = unix.Kill(-p.cmd.Process.Pid, unix.SIGTERM)
	grace := time.NewTimer(100 * time.Millisecond)
	<-grace.C
	_ = unix.Kill(-p.cmd.Process.Pid, unix.SIGKILL)
	if hooks.afterSignals != nil {
		hooks.afterSignals()
	}
	// This is the ONLY reap and occurs after the last possible group signal.
	p.err = p.cmd.Wait()
}

func (p *ownedProcess) stop() {
	p.once.Do(func() { close(p.request) })
	<-p.done
}

func startTemporalProcess(t *testing.T, binary, namespace, database string) (client.Client, error) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	address := listener.Addr().String()
	_, port, _ := net.SplitHostPort(address)
	_ = listener.Close()
	// Startup timeout and entire lifetime have separate ownership. Startup
	// failure explicitly stops the child; successful server lasts to cleanup.
	lifetime, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	p, err := startOwnedProcess(t, lifetime, binary, "server", "start-dev", "--ip", "127.0.0.1", "--port", port, "--namespace", namespace, "--db-filename", database, "--headless", "--log-level", "never", "--dynamic-config-value", "frontend.enableServerVersionCheck=false")
	if err != nil {
		return nil, err
	}
	deadline, done := context.WithTimeout(t.Context(), 30*time.Second)
	defer done()
	for {
		attempt, cancelAttempt := context.WithTimeout(deadline, time.Second)
		c, dialErr := client.DialContext(attempt, client.Options{HostPort: address, Namespace: namespace})
		cancelAttempt()
		if dialErr == nil {
			t.Cleanup(c.Close)
			return c, nil
		}
		select {
		case <-p.done:
			return nil, fmt.Errorf("owned Temporal exited before readiness")
		case <-deadline.Done():
			p.stop()
			return nil, fmt.Errorf("owned Temporal readiness deadline exceeded")
		case <-time.After(25 * time.Millisecond):
		}
	}
}

func TestTemporalExecutableMismatchRejected(t *testing.T) {
	path := t.TempDir() + "/not-temporal"
	if err := os.WriteFile(path, []byte("not the pinned binary"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := validateTemporalExecutable(path); err == nil {
		t.Fatal("unverified executable accepted before SQL allocation")
	}
}

func TestOwnedProcessObservationDeniedReapsLeaderWithoutGroupSignal(t *testing.T) {
	if testing.Short() {
		t.Skip("starts an owned test subprocess; run scoped without -short")
	}
	groupSignaled := false
	reported := false
	t.Cleanup(func() {
		if !reported {
			t.Error("identity observation error was silently suppressed during cleanup")
		}
	})
	p, err := startOwnedProcessObserved(t, context.Background(), processObservationHooks{
		observeExit:   func(int) (bool, error) { return false, unix.EACCES },
		beforeSignals: func() { groupSignaled = true },
		reportIdentityError: func(err error) {
			reported = true
			if err != unix.EACCES {
				t.Errorf("unexpected observation error: %v", err)
			}
		},
	}, os.Args[0], "-test.run=^TestNeverReadyOwnedChild$")
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-p.done:
	case <-time.After(5 * time.Second):
		t.Fatal("Waitid EACCES left live child cleanup blocked")
	}
	if groupSignaled || p.identityErr != unix.EACCES || p.err == nil {
		t.Fatal("observation failure was not safely surfaced without group signals")
	}
	if _, err := childExited(p.cmd.Process.Pid); err != unix.ECHILD {
		t.Fatalf("owned leader was not reaped after observation failure: %v", err)
	}
	// Explicitly verify the error-report callback; normal cleanup repeats it
	// rather than suppressing an observation failure as a successful teardown.
	if reported {
		t.Fatal("identity reporting unexpectedly ran before cleanup")
	}
}

func TestOwnedProcessStartFailureHasNoSignalTarget(t *testing.T) {
	p, err := startOwnedProcess(t, context.Background(), t.TempDir()+"/missing-executable")
	if err == nil || p.cmd.Process != nil {
		t.Fatal("failed start unexpectedly acquired a signal target")
	}
	p.stop()
	select {
	case <-p.done:
	default:
		t.Fatal("failed start cleanup did not finish")
	}
}

func TestOwnedProcessStopsWhenReadinessNeverArrives(t *testing.T) {
	if testing.Short() {
		t.Skip("starts an owned test subprocess; run scoped without -short")
	}
	// Own the existing test binary in helper mode, not a public binary-hash
	// override. No SQL, CLI download or Temporal server is involved here.
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	p, err := startOwnedProcess(t, ctx, os.Args[0], "-test.run=^TestNeverReadyOwnedChild$")
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-p.done:
	case <-time.After(5 * time.Second):
		t.Fatal("owned readiness-timeout child leaked")
	}
	if p.err == nil {
		t.Fatal("never-ready child exited successfully instead of being terminated")
	}
}

func TestOwnedProcessRetainsZombieUntilSignalsFinish(t *testing.T) {
	if testing.Short() {
		t.Skip("starts an owned test subprocess; run scoped without -short")
	}
	beforeSignals := make(chan struct{})
	allowSignals := make(chan struct{})
	afterSignals := make(chan struct{})
	allowReap := make(chan struct{})
	// Always release barriers before cleanup waits for the lifecycle owner.
	var releaseSignals, releaseReap sync.Once
	defer releaseSignals.Do(func() { close(allowSignals) })
	defer releaseReap.Do(func() { close(allowReap) })
	p, err := startOwnedProcessObserved(t, context.Background(), processObservationHooks{
		beforeSignals: func() { close(beforeSignals); <-allowSignals },
		afterSignals:  func() { close(afterSignals); <-allowReap },
	}, os.Args[0], "-test.run=^TestOwnedImmediateExitChild$")
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-beforeSignals:
	case <-time.After(5 * time.Second):
		t.Fatal("owned immediate-exit child did not reach signal barrier")
	}
	exited, err := childExited(p.cmd.Process.Pid)
	if err != nil || !exited {
		t.Fatal("leader was reaped before first group signal; identity not reserved")
	}
	stopped := make(chan struct{})
	go func() { p.stop(); close(stopped) }()
	releaseSignals.Do(func() { close(allowSignals) })
	select {
	case <-afterSignals:
	case <-time.After(5 * time.Second):
		t.Fatal("owned process did not finish signal phase")
	}
	exited, err = childExited(p.cmd.Process.Pid)
	if err != nil || !exited {
		t.Fatal("leader identity was reaped during concurrent stop/signal phase")
	}
	select {
	case <-stopped:
		t.Fatal("stop returned before final reap")
	default:
	}
	releaseReap.Do(func() { close(allowReap) })
	select {
	case <-stopped:
	case <-time.After(5 * time.Second):
		t.Fatal("owned process did not reap after all signals")
	}
	if _, err := childExited(p.cmd.Process.Pid); err != unix.ECHILD {
		t.Fatalf("expected exactly one final reap, got %v", err)
	}
}

func TestOwnedImmediateExitChild(t *testing.T) {
	if len(os.Args) < 2 || !bytes.Contains([]byte(strings.Join(os.Args, " ")), []byte("-test.run=^TestOwnedImmediateExitChild$")) {
		t.Skip("owned subprocess fixture only")
	}
}

func TestNeverReadyOwnedChild(t *testing.T) {
	// A no-op unless invoked by the exact dedicated parent test subprocess.
	if len(os.Args) < 2 || !bytes.Contains([]byte(strings.Join(os.Args, " ")), []byte("-test.run=^TestNeverReadyOwnedChild$")) {
		t.Skip("owned subprocess fixture only")
	}
	for {
		time.Sleep(time.Hour)
	}
}
