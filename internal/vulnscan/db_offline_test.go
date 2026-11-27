package vulnscan

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"testing"
	"time"
)

// tree lists every path under dir with its size and mode, so a test can say
// nothing in it changed.
func tree(t *testing.T, dir string) []string {
	t.Helper()
	var out []string
	_ = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		fi, ierr := d.Info()
		if ierr != nil {
			return nil
		}
		rel, _ := filepath.Rel(dir, p)
		out = append(out, rel+" "+fi.Mode().String()+" "+time.Duration(fi.Size()).String())
		return nil
	})
	sort.Strings(out)
	return out
}

func TestOfflineEnsureNeverReadsTheNetwork(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	cases := []struct {
		name    string
		built   time.Time
		wantErr error
	}{
		{name: "day-old db is used as it is", built: testNow.Add(-48 * time.Hour)},
		{name: "no db is no db", wantErr: ErrNoDB},
		{name: "a stale db is stale", built: testNow.Add(-6 * 24 * time.Hour), wantErr: ErrStale},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			db, g, l, _ := newTestDB(t, tc.built, testNow.Add(-time.Hour))
			db.Offline = true
			_, err := db.Ensure(ctx, EnsureOptions{Fetch: true, Force: true})
			if tc.wantErr == nil && err != nil {
				t.Fatalf("Ensure: %v", err)
			}
			if tc.wantErr != nil && !errors.Is(err, tc.wantErr) {
				t.Fatalf("Ensure err = %v, want %v", err, tc.wantErr)
			}
			if n := l.reads.Load(); n != 0 {
				t.Errorf("listing read %d times offline", n)
			}
			if n := g.updates.Load(); n != 0 {
				t.Errorf("grype db update ran %d times offline", n)
			}
			if _, err := db.Latest(ctx); !errors.Is(err, ErrOffline) {
				t.Errorf("Latest offline = %v, want ErrOffline", err)
			}
		})
	}
}

func TestReadOnlyDatabaseWritesNothingIntoItsDirectory(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db, g, l, _ := newTestDB(t, testNow.Add(-48*time.Hour), testNow.Add(-time.Hour))
	// The keeper's directory, as `forge vuln db pull` leaves it.
	if err := os.MkdirAll(filepath.Join(db.Dir, "grype", "6"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(db.Dir, "grype", "6", "vulnerability.db"), []byte("db"), 0o644); err != nil {
		t.Fatal(err)
	}
	before := tree(t, db.Dir)
	db.ReadOnly, db.ConfigDir = true, t.TempDir()

	st, err := db.Ensure(ctx, EnsureOptions{Fetch: true})
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	if !st.Present {
		t.Fatalf("status = %+v", st)
	}
	if _, err := (&Scanner{DB: db}).ScanSBOM(ctx, []byte(`{"bomFormat":"CycloneDX","specVersion":"1.6"}`)); err != nil {
		t.Fatalf("ScanSBOM: %v", err)
	}
	if after := tree(t, db.Dir); !equalStrings(before, after) {
		t.Errorf("read-only database directory changed:\nbefore %v\nafter  %v", before, after)
	}
	if n := l.reads.Load() + g.updates.Load(); n != 0 {
		t.Errorf("a read-only database refreshed (%d listing reads + updates)", n)
	}
	if g.lastDir != db.ConfigDir {
		t.Errorf("grype ran in %q, want the private config dir %q", g.lastDir, db.ConfigDir)
	}
	if !hasEnv(g.lastEnv, "GRYPE_DB_CACHE_DIR="+filepath.Join(db.Dir, "grype")) {
		t.Errorf("grype not pointed at the read-only database: %v", g.lastEnv)
	}
	if _, err := os.Stat(filepath.Join(db.ConfigDir, "grype.yaml")); err != nil {
		t.Errorf("grype's config not in ConfigDir: %v", err)
	}
	if _, err := db.Import(ctx, filepath.Join(db.ConfigDir, "grype.yaml")); err == nil {
		t.Error("Import into a read-only database succeeded")
	}
}

func TestReadOnlyDatabaseSharesAnExistingLock(t *testing.T) {
	t.Parallel()
	db, _, _, _ := newTestDB(t, testNow.Add(-2*time.Hour), testNow)
	if err := os.WriteFile(db.lockPath(), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	before := tree(t, db.Dir)
	db.ReadOnly, db.ConfigDir = true, t.TempDir()
	if _, err := db.Ensure(context.Background(), EnsureOptions{}); err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	if after := tree(t, db.Dir); !equalStrings(before, after) {
		t.Errorf("directory changed: %v -> %v", before, after)
	}
}

func TestReadOnlyDatabaseNeedsAConfigDir(t *testing.T) {
	t.Parallel()
	db, _, _, _ := newTestDB(t, testNow.Add(-2*time.Hour), testNow)
	db.ReadOnly = true
	if _, err := db.Status(context.Background()); err == nil {
		t.Fatal("a read-only database with no ConfigDir ran grype")
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// Review B1 on #3001: a read-only database whose lock cannot be checked is
// not read unlocked: the keeper may be swapping it, and a scan of a
// half-replaced database must be "not scanned", never clean.
func TestReadOnlyDatabaseRefusesAnUncheckableLock(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("Windows chmod does not remove directory search permission")
	}
	if os.Geteuid() == 0 {
		t.Skip("root reads through directory permissions")
	}
	if runtime.GOOS == "windows" {
		// os.Chmod only toggles the read-only attribute on Windows: a 0o000
		// directory stays searchable, so the stat this test needs to fail
		// succeeds.
		t.Skip("Windows has no POSIX directory permissions to make the lock uncheckable")
	}
	db, _, _, _ := newTestDB(t, testNow.Add(-2*time.Hour), testNow)
	// The lock lives in a directory this process cannot search, so the stat
	// fails with a permission error rather than "not there".
	locked := filepath.Join(db.Dir, "sealed")
	if err := os.Mkdir(locked, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(locked, "lock"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(locked, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o755) })
	ro := *db
	ro.Dir, ro.ReadOnly, ro.ConfigDir = locked, true, t.TempDir()
	if release, err := ro.lock(context.Background(), false); err == nil {
		release()
		t.Fatal("lock() read a database whose lock it could not check")
	}
}
