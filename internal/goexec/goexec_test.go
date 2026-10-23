package goexec

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

// trapChild starts `sh` holding a marker file it removes from an INT trap
// — a stand-in for `go build` unlinking its go-link-* scratch directory.
// Whether the marker survives cancellation IS the leak: present means the
// child never got to clean up, absent means it did.
func trapChild(ctx context.Context, t *testing.T) (cmd *exec.Cmd, marker string) {
	t.Helper()
	marker = filepath.Join(t.TempDir(), "scratch")
	if err := os.WriteFile(marker, []byte("pretend this is 120MB of linker input\n"), 0o644); err != nil {
		t.Fatalf("seed marker: %v", err)
	}
	// `exec sleep` would replace the shell and lose the trap, so the sleep
	// runs in the background and the shell waits on it — the shape that
	// keeps a signal handler installed.
	script := `trap 'rm -f "$MARK"; exit 0' INT TERM; sleep 30 & wait`
	cmd = exec.CommandContext(ctx, "sh", "-c", script)
	cmd.Env = append(os.Environ(), "MARK="+marker)
	return cmd, marker
}

// waitForExit runs cmd, cancels after it is up, and reports whether the
// marker survived. It returns rather than asserts so both the control and
// the treatment can go through one path.
func runAndCancel(t *testing.T, cmd *exec.Cmd, cancel context.CancelFunc, marker string) (survived bool) {
	t.Helper()
	if err := cmd.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	// Give sh time to install the trap. Cancelling before the handler is
	// registered would make the control case pass for the wrong reason.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if out, err := exec.Command("sh", "-c", "pgrep -P "+itoa(cmd.Process.Pid)).Output(); err == nil && len(out) > 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	cancel()
	_ = cmd.Wait()

	// The trap's rm races the Wait return by microseconds; allow for it
	// rather than reading the filesystem at the exact instant of exit.
	for i := 0; i < 100; i++ {
		if _, err := os.Stat(marker); os.IsNotExist(err) {
			return false
		}
		time.Sleep(20 * time.Millisecond)
	}
	_, err := os.Stat(marker)
	return err == nil
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}

// TestPlainCommandContextSIGKILLsAndOrphansScratch is the control: it
// pins the DEFECT. exec.CommandContext's default cancellation kills the
// child outright, so its cleanup never runs and its scratch is orphaned —
// which is how 15 GB of go-link-* accumulated in one TMPDIR.
//
// If this test ever starts failing, the Go runtime changed its default
// cancellation and Graceful's call sites deserve a second look.
func TestPlainCommandContextSIGKILLsAndOrphansScratch(t *testing.T) {
	if testing.Short() {
		t.Skip("waits out real process/probe timeouts; runs in task test")
	}
	if runtime.GOOS == "windows" {
		t.Skip("POSIX signals and sh are not available on Windows")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cmd, marker := trapChild(ctx, t)

	if survived := runAndCancel(t, cmd, cancel, marker); !survived {
		t.Fatal("the plain CommandContext child cleaned up, so SIGKILL is no longer the default — this control no longer pins the defect Graceful fixes")
	}
}

// TestGracefulLetsTheChildCleanUp is the fix: the same child, the same
// cancellation, but the signal is one `sh` (and `go`) handles, so the
// scratch is gone by the time Wait returns.
func TestGracefulLetsTheChildCleanUp(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX signals and sh are not available on Windows")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cmd, marker := trapChild(ctx, t)
	Graceful(cmd)

	if survived := runAndCancel(t, cmd, cancel, marker); survived {
		t.Fatalf("%s survived cancellation: the child was killed before its cleanup ran", marker)
	}
}

// TestGracefulBoundsTheWaitOnAnUnresponsiveChild — a child that ignores
// SIGINT must not turn a cancelled build into a hang. WaitDelay is what
// guarantees that, so assert it is set rather than burning GraceDelay of
// wall clock proving it.
func TestGracefulBoundsTheWaitOnAnUnresponsiveChild(t *testing.T) {
	cmd := exec.Command("sh", "-c", "true")
	Graceful(cmd)
	if cmd.WaitDelay != GraceDelay {
		t.Fatalf("WaitDelay = %v, want %v; without it a child that ignores SIGINT hangs the cancel forever", cmd.WaitDelay, GraceDelay)
	}
	if cmd.Cancel == nil {
		t.Fatal("Cancel is nil; cancellation would still SIGKILL")
	}
}

// TestGracefulCancelBeforeStartIsNotAnError — Cancel can fire on a cmd
// whose process never came up (a context already done at Start). It must
// report nothing to signal rather than dereferencing a nil Process.
func TestGracefulCancelBeforeStartIsNotAnError(t *testing.T) {
	cmd := exec.Command("sh", "-c", "true")
	Graceful(cmd)
	if err := cmd.Cancel(); err != nil {
		t.Fatalf("Cancel before Start: %v", err)
	}
}

// TestGracefulToleratesNilCmd keeps the helper usable at a call site that
// builds its command conditionally.
func TestGracefulToleratesNilCmd(t *testing.T) {
	if got := Graceful(nil); got != nil {
		t.Fatalf("Graceful(nil) = %v, want nil", got)
	}
}
