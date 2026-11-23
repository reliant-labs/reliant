//go:build unix

package selfupdate

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fixedNow is the clock tests hand an Updater, so no assertion depends on
// when the test runs.
var fixedNow = time.Date(2026, 11, 19, 12, 0, 0, 0, time.UTC)

type fakeDrainer struct {
	err      error
	deadline time.Time
	called   bool
}

func (d *fakeDrainer) Drain(_ context.Context, deadline time.Time) (DrainReport, error) {
	d.called, d.deadline = true, deadline
	return DrainReport{Interrupted: 1}, d.err
}

// staged prepares v2 for an Updater whose running binary holds oldBin.
func stagedUpdater(t *testing.T, mutate func(*Config)) (u *Updater, s Staged, exe string, oldBin, newBin []byte) {
	t.Helper()
	oldBin, newBin = script("echo 1.0.0"), script("echo 2.0.0")
	srv := newFakeRelease(t, "v2.0.0", releaseFiles(t, newBin))
	exe = installed(t, oldBin)
	u = newTestUpdater(t, exe, srv, mutate)
	s, err := u.Prepare(context.Background(), srv.release())
	if err != nil {
		t.Fatal(err)
	}
	return u, s, exe, oldBin, newBin
}

func TestRequestDrainsThenShutsDown(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	t.Run("needs Apply and Shutdown", func(t *testing.T) {
		u, s, _, _, _ := stagedUpdater(t, nil)
		if err := u.Request(ctx, s, Decision{Action: Stage}); err == nil {
			t.Fatal("Request must refuse a Stage decision")
		}
		if err := u.Request(ctx, s, Decision{Action: Apply}); err == nil || !strings.Contains(err.Error(), "Shutdown") {
			t.Fatalf("err = %v, want Shutdown required", err)
		}
	})

	t.Run("drain declines", func(t *testing.T) {
		shut := false
		d := &fakeDrainer{err: errors.New("routine upgrade, work in flight")}
		u, s, _, _, _ := stagedUpdater(t, func(c *Config) { c.Drainer, c.Shutdown = d, func() { shut = true } })
		if err := u.Request(ctx, s, Decision{Action: Apply}); err == nil {
			t.Fatal("a drain error must abort the upgrade")
		}
		if shut || u.Armed() {
			t.Fatal("a declined drain must neither shut down nor arm a swap")
		}
		if err := u.ExecPending(); err != nil {
			t.Fatalf("ExecPending with nothing armed: %v", err)
		}
	})

	t.Run("drain, arm, shut down", func(t *testing.T) {
		shut := false
		d := &fakeDrainer{}
		u, s, _, _, _ := stagedUpdater(t, func(c *Config) { c.Drainer, c.Shutdown = d, func() { shut = true } })
		deadline := fixedNow.Add(time.Minute)
		if err := u.Request(ctx, s, Decision{Action: Apply, Deadline: deadline}); err != nil {
			t.Fatal(err)
		}
		if !d.called || !d.deadline.Equal(deadline) || !shut || !u.Armed() {
			t.Fatalf("drained=%v deadline=%v shut=%v pending=%v", d.called, d.deadline, shut, u.Armed())
		}
	})

	t.Run("default drain deadline", func(t *testing.T) {
		d := &fakeDrainer{}
		u, s, _, _, _ := stagedUpdater(t, func(c *Config) { c.Drainer, c.Shutdown = d, func() {} })
		u.now = func() time.Time { return fixedNow }
		if err := u.Request(ctx, s, Decision{Action: Apply}); err != nil {
			t.Fatal(err)
		}
		if want := fixedNow.Add(DefaultDrainTimeout); !d.deadline.Equal(want) {
			t.Fatalf("default deadline %v, want %v", d.deadline, want)
		}
	})

	t.Run("staged binary tampered", func(t *testing.T) {
		u, s, _, _, _ := stagedUpdater(t, func(c *Config) { c.Shutdown = func() {} })
		if err := os.WriteFile(s.Path, script("echo evil"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := u.Request(ctx, s, Decision{Action: Apply}); err == nil || !strings.Contains(err.Error(), "changed since") {
			t.Fatalf("err = %v", err)
		}
	})
}

func TestExecPendingSwapsAndExecs(t *testing.T) {
	t.Parallel()
	u, s, exe, oldBin, newBin := stagedUpdater(t, func(c *Config) { c.Shutdown = func() {}; c.ConfirmWithin = 7 * time.Second })
	if err := u.Request(context.Background(), s, Decision{Action: Apply}); err != nil {
		t.Fatal(err)
	}
	var execPath string
	u.execFn = func(path string, argv, env []string) error {
		execPath = path
		if len(argv) != len(os.Args) || len(env) == 0 {
			t.Errorf("exec argv=%v env len=%d: want this process's argv and environment", argv, len(env))
		}
		return nil // a real exec never returns
	}
	if err := u.ExecPending(); err != nil {
		t.Fatal(err)
	}
	resolved, _ := filepath.EvalSymlinks(exe)
	if execPath != resolved {
		t.Fatalf("exec'd %q, want the install %q", execPath, resolved)
	}
	if got, _ := os.ReadFile(exe); !bytes.Equal(got, newBin) {
		t.Fatal("the install does not hold the new binary")
	}
	st := readJSONState(t, u.cfg.StateDir)
	if st.Swap == nil || st.Swap.Phase != phaseSwapped || st.Swap.ToVersion != "2.0.0" || st.Swap.FromVersion != "1.0.0" || st.Swap.ConfirmWithinMS != 7000 {
		t.Fatalf("state = %+v", st.Swap)
	}
	if got, _ := os.ReadFile(st.Swap.Previous); !bytes.Equal(got, oldBin) {
		t.Fatal("previous/ does not hold the old binary")
	}
	if fi, err := os.Stat(exe); err != nil || fi.Mode().Perm()&0o111 == 0 {
		t.Fatalf("swapped binary is not executable: %v %v", fi, err)
	}
	if entries, _ := os.ReadDir(filepath.Dir(exe)); len(entries) != 1 {
		t.Fatalf("temp files left beside the install: %v", entries)
	}
}

func TestExecPendingFailureRestoresTheOldBinary(t *testing.T) {
	t.Parallel()
	u, s, exe, oldBin, _ := stagedUpdater(t, func(c *Config) { c.Shutdown = func() {} })
	if err := u.Request(context.Background(), s, Decision{Action: Apply}); err != nil {
		t.Fatal(err)
	}
	u.execFn = func(string, []string, []string) error { return errors.New("exec format error") }
	err := u.ExecPending()
	if err == nil || !strings.Contains(err.Error(), "exec format error") {
		t.Fatalf("err = %v", err)
	}
	if got, _ := os.ReadFile(exe); !bytes.Equal(got, oldBin) {
		t.Fatal("a failed exec must put the old binary back")
	}
	st := readJSONState(t, u.cfg.StateDir)
	if _, failed := st.failed("2.0.0"); !failed || st.Swap.Phase != phaseReverted {
		t.Fatalf("state = %+v, want 2.0.0 failed and the swap reverted", st)
	}
}

func TestExecPendingExitForSupervisor(t *testing.T) {
	t.Parallel()
	u, s, exe, _, newBin := stagedUpdater(t, func(c *Config) { c.Shutdown = func() {}; c.SwapMode = ExitForSupervisor })
	if err := u.Request(context.Background(), s, Decision{Action: Apply}); err != nil {
		t.Fatal(err)
	}
	code := -1
	u.exitFn = func(c int) { code = c }
	u.execFn = func(string, []string, []string) error { t.Fatal("ExitForSupervisor must not exec"); return nil }
	if err := u.ExecPending(); err != nil {
		t.Fatal(err)
	}
	if code != ExitCodeRestart {
		t.Fatalf("exit code %d, want %d", code, ExitCodeRestart)
	}
	if got, _ := os.ReadFile(exe); !bytes.Equal(got, newBin) {
		t.Fatal("the binary was not swapped before exiting")
	}
}

func TestExecPendingRefusesAnInstallThatBecameOwned(t *testing.T) {
	t.Parallel()
	u, s, exe, oldBin, _ := stagedUpdater(t, func(c *Config) { c.Shutdown = func() {} })
	if err := u.Request(context.Background(), s, Decision{Action: Apply}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(exe+".managed-by", []byte("dpkg"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := u.ExecPending(); !errors.Is(err, ErrNotSelfOwned) {
		t.Fatalf("err = %v, want ErrNotSelfOwned", err)
	}
	if got, _ := os.ReadFile(exe); !bytes.Equal(got, oldBin) {
		t.Fatal("an owned install was overwritten")
	}
}
