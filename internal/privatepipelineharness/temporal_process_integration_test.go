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
	cmd  *exec.Cmd
	done chan struct{}
	err  error
	once sync.Once
}

func startOwnedProcess(t *testing.T, ctx context.Context, command string, args ...string) (*ownedProcess, error) {
	t.Helper()
	cmd := exec.Command(command, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Stdout, cmd.Stderr = io.Discard, io.Discard
	p := &ownedProcess{cmd: cmd, done: make(chan struct{})}
	// Register before Start: readiness failure cannot strand the child.
	t.Cleanup(p.stop)
	if err := cmd.Start(); err != nil {
		p.err = err
		close(p.done)
		return p, err
	}
	go func() { p.err = cmd.Wait(); close(p.done) }()
	go func() {
		select {
		case <-ctx.Done():
			p.stop()
		case <-p.done:
		}
	}()
	return p, nil
}

func (p *ownedProcess) stop() {
	p.once.Do(func() {
		if p.cmd.Process == nil {
			return
		}
		select {
		case <-p.done:
			return
		default:
		}
		// A still-live direct child owns this process group; never name-match.
		_ = syscall.Kill(-p.cmd.Process.Pid, syscall.SIGTERM)
		select {
		case <-p.done:
		case <-time.After(3 * time.Second):
			_ = syscall.Kill(-p.cmd.Process.Pid, syscall.SIGKILL)
			<-p.done
		}
	})
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

func TestNeverReadyOwnedChild(t *testing.T) {
	// A no-op unless invoked by the exact dedicated parent test subprocess.
	if len(os.Args) < 2 || !bytes.Contains([]byte(strings.Join(os.Args, " ")), []byte("-test.run=^TestNeverReadyOwnedChild$")) {
		t.Skip("owned subprocess fixture only")
	}
	for {
		time.Sleep(time.Hour)
	}
}
