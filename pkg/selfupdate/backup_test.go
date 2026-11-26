//go:build unix

package selfupdate

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// upgrade swaps the install at exe from version `from` to `to` (binary
// newBin) with a stand-in exec, using StateDir dir.
func upgrade(t *testing.T, exe, dir, from, to string, newBin []byte, mutate func(*Updater)) (*Updater, error) {
	t.Helper()
	files := releaseFiles(t, newBin)
	srv := newFakeRelease(t, "v"+to, files)
	u := newTestUpdater(t, exe, srv, func(c *Config) {
		c.StateDir, c.Current, c.Shutdown = dir, Build{Version: from}, func() {}
	})
	u.execFn = func(string, []string, []string) error { return nil }
	if mutate != nil {
		mutate(u)
	}
	s, err := u.Prepare(context.Background(), srv.release())
	if err != nil {
		t.Fatal(err)
	}
	if err := u.Request(context.Background(), s, Decision{Action: Apply}); err != nil {
		t.Fatal(err)
	}
	return u, u.ExecPending()
}

func backups(t *testing.T, dir string) map[string][]byte {
	t.Helper()
	out := map[string][]byte{}
	entries, _ := os.ReadDir(filepath.Join(dir, "previous"))
	for _, e := range entries {
		b, err := os.ReadFile(filepath.Join(dir, "previous", e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		out[filepath.Join(dir, "previous", e.Name())] = b
	}
	return out
}

func confirm(t *testing.T, u *Updater, exe string) {
	t.Helper()
	p := bootAs(u, exe, os.Getpid(), newExecRecorder(), allAlive)
	if !p.Swapped {
		t.Fatal("want a pending swap to confirm")
	}
	if err := p.Confirm(); err != nil {
		t.Fatal(err)
	}
}

func TestBackupBinaryNeverReusesAName(t *testing.T) {
	t.Parallel()
	src := installed(t, script("echo 1"))
	dir := filepath.Join(t.TempDir(), "previous")
	a, err := backupBinary(src, dir, "1.0.0", fixedNow)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(src, script("echo 2"), 0o755); err != nil {
		t.Fatal(err)
	}
	b, err := backupBinary(src, dir, "1.0.0", fixedNow) // same instant, same label
	if err != nil {
		t.Fatal(err)
	}
	if a == b {
		t.Fatalf("two backups share %s", a)
	}
	if got, _ := os.ReadFile(a); !bytes.Equal(got, script("echo 1")) {
		t.Fatal("the first backup was overwritten")
	}
}

func TestFailedBackupLeavesThePriorRollbackImage(t *testing.T) {
	t.Parallel()
	v1, v2, v3 := script("echo 1.0.0"), script("echo 2.0.0"), script("echo 3.0.0")
	exe := installed(t, v1)
	dir := filepath.Join(t.TempDir(), "state")
	u1, err := upgrade(t, exe, dir, "1.0.0", "2.0.0", v2, nil)
	if err != nil {
		t.Fatal(err)
	}
	confirm(t, u1, exe)
	before := readJSONState(t, dir)
	prior := before.Swap.Previous

	ro := filepath.Join(dir, "previous")
	cases := []struct {
		name    string
		prepare func(t *testing.T)
		mutate  func(*Updater)
	}{
		{name: "the backup copy fails", mutate: func(u *Updater) {
			u.backupFn = func(string, string, string, time.Time) (string, error) { return "", errors.New("disk full") }
		}},
		{name: "the backup directory is read-only", prepare: func(t *testing.T) {
			if os.Geteuid() == 0 {
				t.Skip("root ignores directory permissions")
			}
			if err := os.Chmod(ro, 0o555); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = os.Chmod(ro, 0o755) })
		}},
	}
	for _, tc := range cases {
		// Sequential: both share one install and state directory.
		t.Run(tc.name, func(t *testing.T) {
			if tc.prepare != nil {
				tc.prepare(t)
			}
			_, err := upgrade(t, exe, dir, "2.0.0", "3.0.0", v3, tc.mutate)
			if err == nil {
				t.Fatal("the swap must fail when the running binary cannot be backed up")
			}
			_ = os.Chmod(ro, 0o755)
			if got, err := os.ReadFile(prior); err != nil || !bytes.Equal(got, v1) {
				t.Fatalf("the prior rollback image is gone or changed: %v", err)
			}
			if got, _ := os.ReadFile(exe); !bytes.Equal(got, v2) {
				t.Fatal("the install changed although no backup was made")
			}
			if after := readJSONState(t, dir); after.Swap.Previous != prior || after.Swap.Phase != phaseConfirmed {
				t.Fatalf("state moved: %+v", after.Swap)
			}
		})
	}
}

func TestSuccessiveUnconfirmedUpgradesKeepEveryRollbackImage(t *testing.T) {
	t.Parallel()
	v1, v2, v3 := script("echo 1.0.0"), script("echo 2.0.0"), script("echo 3.0.0")
	exe := installed(t, v1)
	dir := filepath.Join(t.TempDir(), "state")
	if _, err := upgrade(t, exe, dir, "1.0.0", "2.0.0", v2, nil); err != nil {
		t.Fatal(err)
	}
	backupOfV1 := readJSONState(t, dir).Swap.Previous

	// 2.0.0 never confirmed, and upgrades again.
	u2, err := upgrade(t, exe, dir, "2.0.0", "3.0.0", v3, nil)
	if err != nil {
		t.Fatal(err)
	}
	st := readJSONState(t, dir)
	if got, _ := os.ReadFile(st.Swap.Previous); !bytes.Equal(got, v2) {
		t.Fatal("the rollback for 2 -> 3 does not hold v2")
	}
	if len(st.Swap.Retained) != 1 || st.Swap.Retained[0] != backupOfV1 {
		t.Fatalf("Retained = %v, want the unconfirmed step's backup %s", st.Swap.Retained, backupOfV1)
	}
	if got, _ := os.ReadFile(backupOfV1); !bytes.Equal(got, v1) {
		t.Fatal("v1's rollback image was pruned while 2.0.0 was unconfirmed")
	}

	// Confirming 3.0.0 retires everything but its own rollback image.
	confirm(t, u2, exe)
	got := backups(t, dir)
	if len(got) != 1 || !bytes.Equal(got[st.Swap.Previous], v2) {
		t.Fatalf("backups after confirming = %d files, want only v2's", len(got))
	}
}

func TestAConfirmedStepsBackupIsRetiredByTheNextCommit(t *testing.T) {
	t.Parallel()
	v1, v2, v3 := script("echo 1.0.0"), script("echo 2.0.0"), script("echo 3.0.0")
	exe := installed(t, v1)
	dir := filepath.Join(t.TempDir(), "state")
	u1, err := upgrade(t, exe, dir, "1.0.0", "2.0.0", v2, nil)
	if err != nil {
		t.Fatal(err)
	}
	confirm(t, u1, exe)
	if _, err := upgrade(t, exe, dir, "2.0.0", "3.0.0", v3, nil); err != nil {
		t.Fatal(err)
	}
	st := readJSONState(t, dir)
	got := backups(t, dir)
	if len(got) != 1 || !bytes.Equal(got[st.Swap.Previous], v2) || len(st.Swap.Retained) != 0 {
		t.Fatalf("after 2 -> 3: %d backups, retained %v; want only v2's", len(got), st.Swap.Retained)
	}
}
