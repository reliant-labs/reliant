package vulnscan

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestEnsureStateMachine(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fresh := testNow.Add(-2 * time.Hour)
	old := testNow.Add(-48 * time.Hour)
	stale := testNow.Add(-6 * 24 * time.Hour)
	newer := testNow.Add(-1 * time.Hour)

	cases := []struct {
		name        string
		built       time.Time // zero: no DB
		listed      time.Time
		listingDown bool
		updateFails bool
		opts        EnsureOptions
		wantErr     error
		wantBuilt   time.Time
		wantReads   int32
		wantUpdates int32
		wantLog     string
	}{
		{name: "no db, no fetch: reports, never downloads", opts: EnsureOptions{}, listed: newer, wantErr: ErrNoDB},
		{name: "no db, fetch: downloads", opts: EnsureOptions{Fetch: true}, listed: newer, wantBuilt: newer, wantReads: 1, wantUpdates: 1, wantLog: "downloading the vulnerability database"},
		{name: "fresh db: no listing read", built: fresh, listed: newer, opts: EnsureOptions{Fetch: true}, wantBuilt: fresh},
		{name: "day-old db, newer listed: updates", built: old, listed: newer, opts: EnsureOptions{Fetch: true}, wantBuilt: newer, wantReads: 1, wantUpdates: 1},
		{name: "day-old db, nothing newer: no update", built: old, listed: old, opts: EnsureOptions{Fetch: true}, wantBuilt: old, wantReads: 1},
		{name: "day-old db without fetch: used as is", built: old, listed: newer, opts: EnsureOptions{}, wantBuilt: old},
		{name: "day-old db, listing down: used, said so", built: old, listingDown: true, opts: EnsureOptions{Fetch: true}, wantBuilt: old, wantReads: 1, wantLog: "could not check for a newer"},
		{name: "stale db, listing down: stale", built: stale, listingDown: true, opts: EnsureOptions{Fetch: true}, wantErr: ErrStale, wantReads: 1},
		{name: "stale db, no fetch: stale", built: stale, listed: newer, opts: EnsureOptions{}, wantErr: ErrStale},
		{name: "day-old db, download fails: used, said so", built: old, listed: newer, updateFails: true, opts: EnsureOptions{Fetch: true}, wantBuilt: old, wantReads: 1, wantUpdates: 1, wantLog: "download failed"},
		{name: "no db, download fails: no db", listed: newer, updateFails: true, opts: EnsureOptions{Fetch: true}, wantErr: ErrNoDB, wantReads: 1, wantUpdates: 1},
		{name: "force on a fresh db reads the listing", built: fresh, listed: newer, opts: EnsureOptions{Force: true}, wantBuilt: newer, wantReads: 1, wantUpdates: 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			db, g, l, log := newTestDB(t, tc.built, tc.listed)
			l.set(tc.listed, tc.listingDown)
			g.failUpdate = tc.updateFails
			st, err := db.Ensure(ctx, tc.opts)
			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("Ensure err = %v, want %v", err, tc.wantErr)
				}
			} else if err != nil {
				t.Fatalf("Ensure: %v", err)
			}
			if !tc.wantBuilt.IsZero() && !st.BuiltAt.Equal(tc.wantBuilt) {
				t.Errorf("built = %s, want %s", st.BuiltAt, tc.wantBuilt)
			}
			if got := l.reads.Load(); got != tc.wantReads {
				t.Errorf("listing reads = %d, want %d", got, tc.wantReads)
			}
			if got := g.updates.Load(); got != tc.wantUpdates {
				t.Errorf("grype db update runs = %d, want %d", got, tc.wantUpdates)
			}
			if tc.wantLog != "" && !strings.Contains(log.String(), tc.wantLog) {
				t.Errorf("log %q does not say %q", log.String(), tc.wantLog)
			}
		})
	}
}

// A listing that named nothing newer is not asked again within the hour:
// a late upstream does not cost every command a round-trip.
func TestEnsureDoesNotRecheckWithinTheHour(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	old := testNow.Add(-48 * time.Hour)
	db, _, l, _ := newTestDB(t, old, old)
	for i := 0; i < 3; i++ {
		if _, err := db.Ensure(ctx, EnsureOptions{Fetch: true}); err != nil {
			t.Fatal(err)
		}
	}
	if got := l.reads.Load(); got != 1 {
		t.Fatalf("listing reads = %d, want 1", got)
	}
	later := testNow.Add(2 * time.Hour)
	db.Now = func() time.Time { return later }
	if _, err := db.Ensure(ctx, EnsureOptions{Fetch: true}); err != nil {
		t.Fatal(err)
	}
	if got := l.reads.Load(); got != 2 {
		t.Fatalf("listing reads after the hour = %d, want 2", got)
	}
}

func TestStatusAndCacheKey(t *testing.T) {
	t.Parallel()
	built := time.Date(2026, 11, 19, 18, 41, 5, 0, time.UTC)
	db, g, _, _ := newTestDB(t, built, built)
	st, err := db.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !st.Present || st.Schema != "v6.1.10" || !st.BuiltAt.Equal(built) {
		t.Fatalf("status = %+v", st)
	}
	if st.CacheKey() != "forge-vulndb-v6-2026-11-19" {
		t.Errorf("cache key = %q", st.CacheKey())
	}
	if !st.Fresh(built.Add(23*time.Hour)) || st.Fresh(built.Add(25*time.Hour)) {
		t.Errorf("Fresh boundary wrong")
	}
	if st.Stale(built.Add(119*time.Hour)) || !st.Stale(built.Add(121*time.Hour)) {
		t.Errorf("Stale boundary wrong")
	}
	// Every grype invocation runs from the DB directory, with auto-update
	// off and the age check on.
	for _, want := range []string{"GRYPE_DB_AUTO_UPDATE=false", "GRYPE_DB_VALIDATE_AGE=true", "GRYPE_DB_CACHE_DIR=" + filepath.Join(db.Dir, "grype")} {
		if !hasEnv(g.lastEnv, want) {
			t.Errorf("grype env lacks %s", want)
		}
	}
	if g.lastDir != db.Dir {
		t.Errorf("grype ran in %q, want %q", g.lastDir, db.Dir)
	}
	cfg, err := os.ReadFile(filepath.Join(db.Dir, "grype.yaml"))
	if err != nil || string(cfg) != emptyConfig {
		t.Errorf("grype.yaml = %q, %v", cfg, err)
	}
}

func TestStatusWithNoDatabase(t *testing.T) {
	t.Parallel()
	db, _, _, _ := newTestDB(t, time.Time{}, testNow)
	st, err := db.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if st.Present || st.Problem != "database does not exist" || st.CacheKey() != "" {
		t.Fatalf("status = %+v", st)
	}
}

func TestMissingScannerIsErrScannerMissing(t *testing.T) {
	t.Parallel()
	db, _, _, _ := newTestDB(t, testNow, testNow)
	db.Grype = func(context.Context) (string, string, error) {
		return "", "", errors.New("no binary for windows/arm64")
	}
	if _, err := db.Status(context.Background()); !errors.Is(err, ErrScannerMissing) {
		t.Fatalf("Status err = %v, want ErrScannerMissing", err)
	}
	s := &Scanner{DB: db}
	if _, err := s.Scan(context.Background(), "sbom:/x"); !errors.Is(err, ErrScannerMissing) {
		t.Fatalf("Scan err = %v, want ErrScannerMissing", err)
	}
}

func TestImport(t *testing.T) {
	t.Parallel()
	db, g, l, log := newTestDB(t, time.Time{}, testNow)
	archive := filepath.Join(t.TempDir(), "vulnerability-db.tar.zst")
	built := testNow.Add(-3 * time.Hour)
	if err := os.WriteFile(archive, []byte(built.Format(time.RFC3339)), 0o644); err != nil {
		t.Fatal(err)
	}
	st, err := db.Import(context.Background(), archive)
	if err != nil {
		t.Fatal(err)
	}
	if !st.BuiltAt.Equal(built) || g.imports.Load() != 1 || l.reads.Load() != 0 {
		t.Fatalf("import: status %+v, imports %d, listing reads %d", st, g.imports.Load(), l.reads.Load())
	}
	if !strings.Contains(log.String(), "importing the vulnerability database") {
		t.Errorf("import said nothing: %q", log.String())
	}
	if _, err := db.Import(context.Background(), t.TempDir()); err == nil {
		t.Errorf("importing a directory succeeded")
	}
}

// A GRYPE_* variable left in the caller's shell never reaches grype: one
// such as GRYPE_ONLY_FIXED would silently change what a scan finds.
func TestGrypeEnvironmentDropsCallerGrypeVariables(t *testing.T) {
	t.Setenv("GRYPE_ONLY_FIXED", "true")
	t.Setenv("GRYPE_DB_AUTO_UPDATE", "true")
	db := &DB{Dir: t.TempDir(), URL: "https://mirror.example/grype"}
	env := db.env()
	if hasEnv(env, "GRYPE_ONLY_FIXED=true") || hasEnv(env, "GRYPE_DB_AUTO_UPDATE=true") {
		t.Fatalf("caller's GRYPE_* leaked: %v", env)
	}
	if !hasEnv(env, "GRYPE_DB_UPDATE_URL=https://mirror.example/grype") {
		t.Fatalf("mirror not passed to grype")
	}
}

func TestBaseURLFromEnvironment(t *testing.T) {
	t.Setenv(EnvURL, "https://mirror.example/db/")
	db := &DB{}
	if got := db.ListingURL(); got != "https://mirror.example/db/v6/latest.json" {
		t.Fatalf("listing URL = %q", got)
	}
	t.Setenv(EnvDir, "/ci/cache/vulndb")
	if d, _ := DefaultDir(); d != "/ci/cache/vulndb" {
		t.Fatalf("DefaultDir = %q", d)
	}
}

func TestScanRefusesStaleAndMissingDatabases(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db, g, _, _ := newTestDB(t, testNow.Add(-6*24*time.Hour), testNow)
	s := &Scanner{DB: db}
	if _, err := s.Scan(ctx, "sbom:/x"); !errors.Is(err, ErrStale) {
		t.Fatalf("stale scan err = %v", err)
	}
	g.setBuilt(time.Time{})
	if _, err := s.Scan(ctx, "sbom:/x"); !errors.Is(err, ErrNoDB) {
		t.Fatalf("missing-db scan err = %v", err)
	}
	if g.scans.Load() != 0 {
		t.Fatalf("grype scanned against an unusable database")
	}
}

func TestScanSBOM(t *testing.T) {
	t.Parallel()
	built := testNow.Add(-5 * time.Hour)
	db, _, _, _ := newTestDB(t, built, built)
	s := &Scanner{DB: db}
	scan, err := s.ScanSBOM(context.Background(), []byte(`{"bomFormat":"CycloneDX"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !scan.DBBuiltAt.Equal(built) || len(scan.Findings) != 7 || scan.ScannerVersion != "v0.122.1" {
		t.Fatalf("scan = built %s, %d findings, version %s", scan.DBBuiltAt, len(scan.Findings), scan.ScannerVersion)
	}
}

// Two processes refreshing at once: one downloads, the other waits for it
// and then uses its result.
func TestConcurrentEnsureDownloadsOnce(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	newer := testNow.Add(-time.Hour)
	db, g, _, _ := newTestDB(t, time.Time{}, newer)
	other := *db
	errs := make(chan error, 2)
	for _, d := range []*DB{db, &other} {
		go func(d *DB) {
			_, err := d.Ensure(ctx, EnsureOptions{Fetch: true})
			errs <- err
		}(d)
	}
	for i := 0; i < 2; i++ {
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
	}
	if got := g.updates.Load(); got != 1 {
		t.Fatalf("grype db update ran %d times, want 1", got)
	}
}
