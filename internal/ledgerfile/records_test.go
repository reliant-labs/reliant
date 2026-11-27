package ledgerfile

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/reliant-labs/forge/pkg/release"
)

func readFileT(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func writeRaw(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestOpenRefusesAnEmptyHomeOrProject(t *testing.T) {
	t.Parallel()
	for name, args := range map[string][2]string{
		"empty home":       {"", "proj"},
		"blank home":       {"   ", "proj"},
		"empty project id": {t.TempDir(), ""},
		"blank project id": {t.TempDir(), "\t"},
	} {
		if s, err := Open(args[0], args[1]); err == nil || s != nil {
			t.Errorf("%s: Open = %v, %v; want a refusal", name, s, err)
		}
	}
}

func TestOpenFailsWhenTheLedgerDirectoryCannotBeCreated(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	// A file where the home directory should be.
	blocker := filepath.Join(home, "file")
	writeRaw(t, blocker, "x")
	_, err := Open(blocker, "proj")
	if err == nil || !strings.Contains(err.Error(), "create ledger") {
		t.Errorf("err = %v, want a create-ledger failure", err)
	}
}

// Home is the one place the environment is read: the variable wins, and its
// value is trimmed; without it the ledger lives under the user's home.
func TestHomeResolution(t *testing.T) {
	t.Setenv(DefaultHomeEnv, "  "+filepath.Join("custom", "ledger")+"  ")
	got, err := Home()
	if err != nil || got != filepath.Join("custom", "ledger") {
		t.Errorf("Home with %s set = %q, %v", DefaultHomeEnv, got, err)
	}

	t.Setenv(DefaultHomeEnv, "")
	user := t.TempDir()
	t.Setenv("HOME", user)
	t.Setenv("USERPROFILE", user) // os.UserHomeDir on Windows
	got, err = Home()
	if err != nil || got != filepath.Join(user, ".forge", "ledger") {
		t.Errorf("Home without the variable = %q, %v; want ~/.forge/ledger", got, err)
	}
}

func TestEnvDeclarationsKeepTheNewestPerEnvInFirstSeenOrder(t *testing.T) {
	t.Parallel()
	s := openTest(t)
	if got, err := s.Envs(); err != nil || len(got) != 0 {
		t.Fatalf("Envs on an empty ledger = %v, %v", got, err)
	}
	if got, err := s.Env("prod"); err != nil || got != nil {
		t.Fatalf("Env of an undeclared env = %v, %v; want nil, nil", got, err)
	}
	if err := s.PutEnv(release.EnvRecord{Name: "", Kind: release.EnvPersistent}); !errors.Is(err, release.ErrInvalid) {
		t.Errorf("PutEnv with no name = %v, want ErrInvalid", err)
	}
	if err := s.PutEnv(release.EnvRecord{Name: "prod", Kind: "nonsense"}); !errors.Is(err, release.ErrInvalid) {
		t.Errorf("PutEnv with an unknown kind = %v, want ErrInvalid", err)
	}

	for _, r := range []release.EnvRecord{
		{Name: "prod", Kind: release.EnvPersistent},
		{Name: "staging", Kind: release.EnvPersistent},
		{Name: "prod", Kind: release.EnvSelfManaged},
	} {
		if err := s.PutEnv(r); err != nil {
			t.Fatal(err)
		}
	}
	all, err := s.Envs()
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 2 || all[0].Name != "prod" || all[0].Kind != release.EnvSelfManaged || all[1].Name != "staging" {
		t.Errorf("Envs = %+v, want prod (re-declared self_managed) then staging", all)
	}
	prod, err := s.Env("prod")
	if err != nil || prod == nil || prod.Kind != release.EnvSelfManaged {
		t.Errorf("Env(prod) = %+v, %v; want the newest declaration", prod, err)
	}
	// The log is append-only: the superseded declaration is still on disk.
	lines, err := readLines(s.path(envsFile))
	if err != nil || len(lines) != 3 {
		t.Errorf("envs.jsonl has %d lines, %v; want all 3 declarations kept", len(lines), err)
	}
}

// shapeFixture is a shape that survives a round trip: the ledger refuses to
// decode a record whose environment kind is empty.
var shapeFixture = release.Shape{Kind: release.EnvPersistent}

func TestBundlesAreIdempotentOnEnvAndDigest(t *testing.T) {
	t.Parallel()
	s := openTest(t)
	digest := "sha256:" + strings.Repeat("a", 64)

	for name, b := range map[string]release.BundleRecord{
		"no env":    {Digest: digest},
		"no digest": {Env: "prod"},
	} {
		if _, _, err := s.RecordBundle(b); err == nil {
			t.Errorf("%s: RecordBundle accepted an incomplete bundle", name)
		}
	}
	bad := release.BundleRecord{Env: "prod", Digest: digest, Shape: release.Shape{Kind: "bogus"}}
	if _, _, err := s.RecordBundle(bad); !errors.Is(err, release.ErrInvalid) {
		t.Errorf("RecordBundle with an invalid shape = %v, want ErrInvalid", err)
	}

	first, created, err := s.RecordBundle(release.BundleRecord{Env: "prod", Digest: digest, Release: "v1", Shape: shapeFixture})
	if err != nil || !created || first.ID == "" || first.CreatedAt.IsZero() {
		t.Fatalf("first RecordBundle = %+v, created %v, %v; want a new record with an id and a time", first, created, err)
	}
	again, created, err := s.RecordBundle(release.BundleRecord{Env: "prod", Digest: digest, Release: "v2-ignored", Shape: shapeFixture})
	if err != nil || created || again.ID != first.ID || again.Release != "v1" {
		t.Errorf("a retry = %+v, created %v, %v; want the record already held", again, created, err)
	}
	// The same content in another env is another bundle.
	other, created, err := s.RecordBundle(release.BundleRecord{Env: "staging", Digest: digest, Shape: shapeFixture})
	if err != nil || !created || other.ID == first.ID {
		t.Errorf("same digest in another env = %+v, created %v, %v", other, created, err)
	}
	// An explicit id and time are kept.
	at := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	kept, _, err := s.RecordBundle(release.BundleRecord{Env: "prod", Digest: "sha256:" + strings.Repeat("b", 64), ID: "bundle-fixed", CreatedAt: at, Shape: shapeFixture})
	if err != nil || kept.ID != "bundle-fixed" || !kept.CreatedAt.Equal(at) {
		t.Errorf("explicit id/time = %+v, %v", kept, err)
	}

	if got, err := s.Bundle(first.ID); err != nil || got == nil || got.Digest != digest {
		t.Errorf("Bundle(id) = %+v, %v", got, err)
	}
	if got, err := s.Bundle("no-such"); err != nil || got != nil {
		t.Errorf("Bundle(unknown) = %+v, %v; want nil, nil", got, err)
	}
	prodBundles, err := s.Bundles("prod")
	if err != nil || len(prodBundles) != 2 || prodBundles[0].ID != first.ID || prodBundles[1].ID != "bundle-fixed" {
		t.Errorf("Bundles(prod) = %+v, %v; want the two prod bundles oldest first", prodBundles, err)
	}
	if got, err := s.Bundles("nowhere"); err != nil || len(got) != 0 {
		t.Errorf("Bundles of an unknown env = %v, %v", got, err)
	}
	if got, want := s.OCIDir(), filepath.Join(s.Dir(), "oci"); got != want {
		t.Errorf("OCIDir = %q, want %q", got, want)
	}
	if got, want := s.VerificationsPath(), filepath.Join(s.Dir(), "verifications.jsonl"); got != want {
		t.Errorf("VerificationsPath = %q, want %q", got, want)
	}
}

func TestReadersRefuseACorruptRecordFile(t *testing.T) {
	t.Parallel()
	s := openTest(t)
	for name, file := range map[string]string{"envs": envsFile, "bundles": bundlesFile, "sessions": sessionsFile} {
		writeRaw(t, s.path(file), "{not json\n")
		var err error
		switch name {
		case "envs":
			_, err = s.Envs()
		case "bundles":
			_, err = s.Bundles("prod")
		case "sessions":
			_, err = s.Sessions()
		}
		if err == nil || !strings.Contains(err.Error(), "line 1") {
			t.Errorf("%s: err = %v, want the corrupt line named", name, err)
		}
	}
}

func applyFixture(env string, started time.Time, deadline time.Duration) release.Apply {
	return release.Apply{
		Env:        env,
		BundleID:   "bundle-1",
		CreatedAt:  started,
		DeadlineAt: started.Add(deadline),
	}
}

func TestBeginApplyRefusesASecondInFlightApplyUnlessSuperseded(t *testing.T) {
	t.Parallel()
	s := openTest(t)
	now := time.Now().UTC().Truncate(time.Second)

	first, err := s.BeginApply(applyFixture("prod", now, time.Hour), false)
	if err != nil || first.ID == "" {
		t.Fatalf("BeginApply = %+v, %v; want an id assigned", first, err)
	}
	_, err = s.BeginApply(applyFixture("prod", now, time.Hour), false)
	if err == nil || !strings.Contains(err.Error(), "has not finished") || !strings.Contains(err.Error(), first.ID) || !strings.Contains(err.Error(), "--supersede") {
		t.Errorf("a second in-flight apply: err = %v, want a refusal naming the first and the override", err)
	}
	// Another env is not blocked.
	if _, err := s.BeginApply(applyFixture("staging", now, time.Hour), false); err != nil {
		t.Errorf("another env's apply was refused: %v", err)
	}
	over, err := s.BeginApply(applyFixture("prod", now, time.Hour), true)
	if err != nil || !over.SupersededInFlight {
		t.Errorf("superseding apply = %+v, %v; want SupersededInFlight recorded", over, err)
	}
	all, err := s.Applies("prod")
	if err != nil || len(all) != 2 || all[0].Apply.ID != first.ID || all[1].Apply.ID != over.ID {
		t.Errorf("Applies = %+v, %v; want both, oldest first", all, err)
	}
}

func TestBeginApplyDefaultsAndValidates(t *testing.T) {
	t.Parallel()
	s := openTest(t)
	if _, err := s.BeginApply(release.Apply{Env: "prod", BundleID: "b"}, false); !errors.Is(err, release.ErrInvalid) {
		t.Errorf("an apply with no deadline = %v, want ErrInvalid", err)
	}
	// With CreatedAt left zero the store stamps it; a deadline before that is refused.
	past := release.Apply{Env: "prod", BundleID: "b", DeadlineAt: time.Now().Add(-time.Hour)}
	if _, err := s.BeginApply(past, false); !errors.Is(err, release.ErrInvalid) {
		t.Errorf("a deadline already past = %v, want ErrInvalid", err)
	}
	ok := release.Apply{Env: "prod", BundleID: "b", ID: "fixed-id", DeadlineAt: time.Now().Add(time.Hour)}
	got, err := s.BeginApply(ok, false)
	if err != nil || got.ID != "fixed-id" || got.CreatedAt.IsZero() {
		t.Errorf("BeginApply = %+v, %v; want the given id and a stamped start", got, err)
	}
}

// An abandoned apply (past its deadline, never reported) must not wedge the
// env: "we could not look" is not "still running".
func TestAnAbandonedApplyDoesNotBlockTheNext(t *testing.T) {
	t.Parallel()
	s := openTest(t)
	started := time.Now().UTC().Add(-2 * time.Hour).Truncate(time.Second)
	if _, err := s.BeginApply(applyFixture("prod", started, time.Hour), false); err != nil {
		t.Fatal(err)
	}
	if _, err := s.BeginApply(applyFixture("prod", time.Now().UTC().Truncate(time.Second), time.Hour), false); err != nil {
		t.Errorf("an apply past its deadline blocked the next one: %v", err)
	}
}

func TestFinishApplyIsIdempotentAndRefusesAContradiction(t *testing.T) {
	t.Parallel()
	s := openTest(t)
	now := time.Now().UTC().Truncate(time.Second)
	a, err := s.BeginApply(applyFixture("prod", now, time.Hour), false)
	if err != nil {
		t.Fatal(err)
	}
	done := release.ApplyOutcome{ApplyID: a.ID, Status: release.ApplySucceeded, Summary: "rolled out", FinishedAt: now.Add(time.Minute)}

	for name, o := range map[string]release.ApplyOutcome{
		"no apply id":    {Status: release.ApplySucceeded, FinishedAt: now},
		"unknown status": {ApplyID: a.ID, Status: "great", FinishedAt: now},
		"no finish time": {ApplyID: a.ID, Status: release.ApplySucceeded},
	} {
		if err := s.FinishApply("prod", o); !errors.Is(err, release.ErrInvalid) {
			t.Errorf("%s: FinishApply = %v, want ErrInvalid", name, err)
		}
	}
	if err := s.FinishApply("prod", release.ApplyOutcome{ApplyID: "nope", Status: release.ApplyFailed, FinishedAt: now}); err == nil || !strings.Contains(err.Error(), "is not recorded") {
		t.Errorf("an outcome for an unknown apply = %v", err)
	}

	if err := s.FinishApply("prod", done); err != nil {
		t.Fatal(err)
	}
	// Reporting the same outcome again is a retry, not a second line.
	if err := s.FinishApply("prod", done); err != nil {
		t.Errorf("re-reporting the same outcome = %v, want an idempotent retry", err)
	}
	lines, _ := readLines(s.envPath("applies", "prod"))
	if len(lines) != 2 {
		t.Errorf("applies file has %d lines, want the apply and one outcome", len(lines))
	}
	conflict := done
	conflict.Status = release.ApplyFailed
	if err := s.FinishApply("prod", conflict); err == nil || !strings.Contains(err.Error(), "already reported") {
		t.Errorf("a different outcome = %v, want a refusal naming the first", err)
	}

	all, err := s.Applies("prod")
	if err != nil || len(all) != 1 || all[0].Outcome == nil || all[0].Outcome.Status != release.ApplySucceeded {
		t.Fatalf("Applies = %+v, %v; want the outcome joined to its apply", all, err)
	}
	// With the apply finished, the next one is not blocked.
	if _, err := s.BeginApply(applyFixture("prod", now, time.Hour), false); err != nil {
		t.Errorf("an apply after a finished one: %v", err)
	}
}

func TestAppliesRefuseMalformedLines(t *testing.T) {
	t.Parallel()
	s := openTest(t)
	path := s.envPath("applies", "prod")
	writeRaw(t, path, `{"outcome":{"apply_id":"ghost","status":"failed","finished_at":"2026-01-01T00:00:00Z"},"apply_id":"ghost"}`+"\n")
	if _, err := s.Applies("prod"); err == nil || !strings.Contains(err.Error(), "which the file does not record") {
		t.Errorf("an outcome with no apply: err = %v", err)
	}
	writeRaw(t, path, `{}`+"\n")
	if _, err := s.Applies("prod"); err == nil || !strings.Contains(err.Error(), "neither an apply nor an outcome") {
		t.Errorf("an empty line object: err = %v", err)
	}
}

func sessionFixture(env, host, key string, seen time.Time) release.LocalSession {
	return release.LocalSession{
		ID:         "sess-" + key,
		Env:        env,
		Worktree:   release.Worktree{Host: host, Key: key},
		StartedAt:  seen.Add(-time.Hour),
		LastSeenAt: seen,
	}
}

// A heartbeat replaces the row for its (env, host, worktree) identity rather
// than appending, so the file stays one row per live stack.
func TestSessionHeartbeatsReplaceTheirOwnRow(t *testing.T) {
	t.Parallel()
	s := openTest(t)
	now := time.Now().UTC().Truncate(time.Second)

	bad := sessionFixture("dev", "", "wt", now)
	if err := s.ReportSession(bad); !errors.Is(err, release.ErrInvalid) {
		t.Errorf("a session with no host = %v, want ErrInvalid", err)
	}

	if err := s.ReportSession(sessionFixture("dev", "h1", "wt-a", now)); err != nil {
		t.Fatal(err)
	}
	if err := s.ReportSession(sessionFixture("dev", "h1", "wt-b", now)); err != nil {
		t.Fatal(err)
	}
	if err := s.ReportSession(sessionFixture("prod", "h1", "wt-a", now)); err != nil {
		t.Fatal(err)
	}
	beat := sessionFixture("dev", "h1", "wt-a", now.Add(time.Minute))
	if err := s.ReportSession(beat); err != nil {
		t.Fatal(err)
	}

	all, err := s.Sessions()
	if err != nil || len(all) != 3 {
		t.Fatalf("Sessions = %+v, %v; want three rows (the heartbeat replaced, not appended)", all, err)
	}
	if !all[0].LastSeenAt.Equal(beat.LastSeenAt) || all[0].Worktree.Key != "wt-a" || all[0].Env != "dev" {
		t.Errorf("the heartbeat row = %+v, want it replaced in place", all[0])
	}
	dev, err := s.SessionsFor("dev")
	if err != nil || len(dev) != 2 {
		t.Errorf("SessionsFor(dev) = %+v, %v; want two", dev, err)
	}
	if none, err := s.SessionsFor("nowhere"); err != nil || len(none) != 0 {
		t.Errorf("SessionsFor(unknown) = %v, %v", none, err)
	}
}

func TestSessionsOnAnEmptyLedger(t *testing.T) {
	t.Parallel()
	s := openTest(t)
	if got, err := s.Sessions(); err != nil || len(got) != 0 {
		t.Errorf("Sessions = %v, %v", got, err)
	}
}

func TestImportPromotionIsVerbatimAndIdempotentOnTheSourceID(t *testing.T) {
	t.Parallel()
	s := openTest(t)
	at := time.Date(2025, 6, 7, 8, 9, 10, 0, time.UTC)
	p := promotion("prod", "v1.0.0")
	p.ID, p.PromotedAt = "source-id-1", at

	if err := s.ImportPromotion(release.Promotion{Env: "prod", Release: "v1", Kind: release.KindPromote}); err == nil || !strings.Contains(err.Error(), "source id is required") {
		t.Errorf("an import with no id = %v", err)
	}
	if err := s.ImportPromotion(release.Promotion{ID: "x", Release: "v1", Kind: release.KindPromote}); !errors.Is(err, release.ErrInvalid) {
		t.Errorf("an import with no env = %v, want ErrInvalid", err)
	}
	for i := 0; i < 3; i++ {
		if err := s.ImportPromotion(p); err != nil {
			t.Fatal(err)
		}
	}
	// Consecutive promotions of one release stay (a promote would collapse them).
	p2 := p
	p2.ID = "source-id-2"
	if err := s.ImportPromotion(p2); err != nil {
		t.Fatal(err)
	}
	got, err := s.Promotions("prod")
	if err != nil || len(got) != 2 || got[0].ID != "source-id-1" || !got[0].PromotedAt.Equal(at) || got[1].ID != "source-id-2" {
		t.Errorf("Promotions = %+v, %v; want both imports with their own ids and the original time", got, err)
	}
}

func TestAppendAndReadGates(t *testing.T) {
	t.Parallel()
	s := openTest(t)
	pass := release.Gate{Name: "smoke", Status: release.GateStatusPassed, Summary: "ok"}
	fail := release.Gate{Name: "lint", Status: release.GateStatusFailed}

	if err := s.AppendGates("prod", "", []release.Gate{pass}); err == nil || !strings.Contains(err.Error(), "promotion id is required") {
		t.Errorf("gates with no promotion id = %v", err)
	}
	if err := s.AppendGates("prod", "p1", []release.Gate{pass, {Status: release.GateStatusPassed}}); !errors.Is(err, release.ErrInvalid) {
		t.Errorf("a nameless gate = %v, want ErrInvalid", err)
	}
	if got, _ := s.Gates("prod", "p1"); len(got) != 0 {
		t.Errorf("an invalid batch was partly recorded: %+v", got)
	}

	if err := s.AppendGates("prod", "p1", []release.Gate{pass, fail}); err != nil {
		t.Fatal(err)
	}
	if err := s.AppendGates("prod", "p2", []release.Gate{pass}); err != nil {
		t.Fatal(err)
	}
	p1, err := s.Gates("prod", "p1")
	if err != nil || len(p1) != 2 || p1[0].Name != "smoke" || p1[1].Name != "lint" || p1[1].Status != release.GateStatusFailed {
		t.Errorf("Gates(p1) = %+v, %v; want only p1's two gates in order", p1, err)
	}
	if none, err := s.Gates("prod", "unknown"); err != nil || len(none) != 0 {
		t.Errorf("Gates(unknown) = %v, %v", none, err)
	}
	// The line is the gate's own JSON with promotion_id added: a gate decoder
	// (ImportLedger) can read it and ignore the join key.
	lines, _ := readLines(s.envPath("gates", "prod"))
	if !strings.Contains(string(lines[0]), `"promotion_id":"p1"`) || !strings.Contains(string(lines[0]), `"name":"smoke"`) {
		t.Errorf("gate line = %s", lines[0])
	}
}

func TestGateLineRefusesGarbage(t *testing.T) {
	t.Parallel()
	var l gateLine
	if err := l.UnmarshalJSON([]byte(`[1]`)); err == nil || !strings.Contains(err.Error(), "decode gate line") {
		t.Errorf("UnmarshalJSON of a non-object = %v", err)
	}
}

func TestRecordReleaseMigrations(t *testing.T) {
	t.Parallel()
	s := openTest(t)
	m := release.MigrationManifest{Sets: []release.MigrationSet{{
		Source: "github.com/acme/app/db/migrations",
		Files:  []string{"20260102000000_b.up.sql", "20260101000000_a.up.sql"},
	}}}

	if _, err := s.RecordReleaseMigrations("v1", m); !errors.Is(err, ErrReleaseNotFound) {
		t.Errorf("a release never cut = %v, want ErrReleaseNotFound", err)
	}
	if _, err := s.RecordReleaseMigrations("v1", release.MigrationManifest{Sets: []release.MigrationSet{{}}}); !errors.Is(err, release.ErrInvalid) {
		t.Errorf("an invalid manifest = %v, want ErrInvalid", err)
	}
	if _, err := s.CutRelease(ociRelease("v1")); err != nil {
		t.Fatal(err)
	}
	recorded, err := s.RecordReleaseMigrations("v1", m)
	if err != nil || !recorded {
		t.Fatalf("first record = %v, %v", recorded, err)
	}
	got, err := s.Release("v1")
	if err != nil || got == nil || got.Migrations == nil {
		t.Fatalf("Release = %+v, %v", got, err)
	}
	// The manifest was normalized: files sorted by version.
	if files := got.Migrations.Sets[0].Files; !slices.Equal(files, []string{"20260101000000_a.up.sql", "20260102000000_b.up.sql"}) {
		t.Errorf("files = %v, want them sorted by version", files)
	}
	// An equal manifest is a no-op; a different one is a conflict.
	recorded, err = s.RecordReleaseMigrations("v1", m)
	if err != nil || recorded {
		t.Errorf("an equal manifest = %v, %v; want a no-op", recorded, err)
	}
	different := release.MigrationManifest{Sets: []release.MigrationSet{{Source: "github.com/acme/app/db/migrations", Files: []string{"20260301000000_c.up.sql"}}}}
	if _, err := s.RecordReleaseMigrations("v1", different); !errors.Is(err, release.ErrReleaseConflict) {
		t.Errorf("a different manifest = %v, want ErrReleaseConflict", err)
	}
	// The release is still one release: the log re-appended it, readers see one.
	all, err := s.Releases()
	if err != nil || len(all) != 1 {
		t.Errorf("Releases = %d, %v; want the re-appended release collapsed to one", len(all), err)
	}
}

func TestReleasesAndReleaseOnAnEmptyLedger(t *testing.T) {
	t.Parallel()
	s := openTest(t)
	if got, err := s.Releases(); err != nil || len(got) != 0 {
		t.Errorf("Releases = %v, %v", got, err)
	}
	if got, err := s.Release("v9"); err != nil || got != nil {
		t.Errorf("Release(unknown) = %v, %v; want nil, nil", got, err)
	}
	if _, err := s.CutRelease(release.Release{}); !errors.Is(err, release.ErrInvalid) {
		t.Errorf("CutRelease with no version = %v, want ErrInvalid", err)
	}
}

func TestPromotionsRefuseALineOfAnotherEnv(t *testing.T) {
	t.Parallel()
	s := openTest(t)
	if _, err := s.AppendPromotion(promotion("staging", "v1"), decideOnly); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(s.envPath("promotions", "staging"))
	if err != nil {
		t.Fatal(err)
	}
	writeRaw(t, s.envPath("promotions", "prod"), string(data))
	if _, err := s.Promotions("prod"); err == nil || !strings.Contains(err.Error(), `records env "staging", not "prod"`) {
		t.Errorf("a misfiled promotion = %v", err)
	}
}

func TestAppendPromotionRefusesWhenAdmitDoes(t *testing.T) {
	t.Parallel()
	s := openTest(t)
	boom := errors.New("stale")
	_, err := s.AppendPromotion(promotion("prod", "v1"), func([]release.Promotion, release.Promotion) (*release.Promotion, error) { return nil, boom })
	if !errors.Is(err, boom) {
		t.Errorf("err = %v, want the admit error", err)
	}
	if got, _ := s.Promotions("prod"); len(got) != 0 {
		t.Errorf("a refused promotion was recorded: %+v", got)
	}
	// With no admit rule, the promotion is simply appended.
	p, err := s.AppendPromotion(promotion("prod", "v1"), nil)
	if err != nil || p.ID == "" || p.PromotedAt.IsZero() {
		t.Errorf("AppendPromotion(nil admit) = %+v, %v", p, err)
	}
	if _, err := s.AppendPromotion(release.Promotion{}, nil); !errors.Is(err, release.ErrInvalid) {
		t.Errorf("an invalid promotion = %v, want ErrInvalid", err)
	}
}

func TestCurrentPromotion(t *testing.T) {
	t.Parallel()
	s := openTest(t)
	if _, ok, err := s.CurrentPromotion("prod"); ok || err != nil {
		t.Fatalf("never promoted = ok %v, err %v", ok, err)
	}
	if _, err := s.AppendPromotion(promotion("prod", "v1"), decideOnly); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AppendPromotion(promotion("prod", "v2"), decideOnly); err != nil {
		t.Fatal(err)
	}
	cur, ok, err := s.CurrentPromotion("prod")
	if err != nil || !ok || cur.Release != "v2" {
		t.Errorf("CurrentPromotion = %+v, %v, %v; want v2", cur, ok, err)
	}
}

func TestReadLinesSkipsBlankLinesAndMissingFiles(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if got, err := readLines(filepath.Join(dir, "missing")); err != nil || got != nil {
		t.Errorf("readLines of a missing file = %v, %v", got, err)
	}
	p := filepath.Join(dir, "f.jsonl")
	writeRaw(t, p, "\n  {\"a\":1}  \n\n\t\n{\"b\":2}")
	got, err := readLines(p)
	if err != nil || len(got) != 2 || string(got[0]) != `{"a":1}` || string(got[1]) != `{"b":2}` {
		t.Errorf("readLines = %q, %v", got, err)
	}
	if _, err := readLines(dir); err == nil {
		t.Error("readLines of a directory must be an error")
	}
	// A line longer than the scanner's 8 MiB cap is an error, not truncation.
	huge := filepath.Join(dir, "huge.jsonl")
	writeRaw(t, huge, strings.Repeat("x", 9<<20)+"\n")
	if _, err := readLines(huge); err == nil {
		t.Error("an over-long line must be an error, not silently cut")
	}
}

func TestCanonicalJSONDoesNotEscapeHTML(t *testing.T) {
	t.Parallel()
	got, err := canonicalJSON(map[string]string{"note": "a < b && c > d"})
	if err != nil || string(got) != `{"note":"a < b && c > d"}` {
		t.Errorf("canonicalJSON = %s, %v; want the characters kept as they are", got, err)
	}
	if _, err := canonicalJSON(make(chan int)); err == nil || !strings.Contains(err.Error(), "encode ledger record") {
		t.Errorf("an unencodable record = %v", err)
	}
}

func TestAppendRecordReportsUnwritablePaths(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	blocker := filepath.Join(dir, "file")
	writeRaw(t, blocker, "x")
	if err := appendRecord(filepath.Join(blocker, "sub", "f.jsonl"), map[string]int{"a": 1}); err == nil || !strings.Contains(err.Error(), "create") {
		t.Errorf("appendRecord under a file = %v, want a create failure", err)
	}
	// The target is a directory: opening it for append fails.
	asDir := filepath.Join(dir, "adir")
	if err := os.Mkdir(asDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := appendRecord(asDir, map[string]int{"a": 1}); err == nil || !strings.Contains(err.Error(), "open") {
		t.Errorf("appendRecord onto a directory = %v, want an open failure", err)
	}
	if err := appendRecord(filepath.Join(dir, "ok.jsonl"), make(chan int)); err == nil {
		t.Error("appendRecord of an unencodable value must fail before writing")
	}
	if _, err := os.Stat(filepath.Join(dir, "ok.jsonl")); err == nil {
		t.Error("a failed encode must not leave a file behind")
	}
}

func TestWriteFileAtomicReportsUnwritablePaths(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	blocker := filepath.Join(dir, "file")
	writeRaw(t, blocker, "x")
	if err := writeFileAtomic(filepath.Join(blocker, "sub", "f"), []byte("x")); err == nil {
		t.Error("writeFileAtomic under a file must fail")
	}
	// Renaming a file over a non-empty directory fails, and the temp file is cleaned up.
	target := filepath.Join(dir, "target")
	if err := os.MkdirAll(filepath.Join(target, "child"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := writeFileAtomic(target, []byte("x")); err == nil {
		t.Error("writeFileAtomic over a non-empty directory must fail")
	}
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".tmp-") {
			t.Errorf("a failed write left %s behind", e.Name())
		}
	}
}

func TestRewriteLinesReportsEncodingErrors(t *testing.T) {
	t.Parallel()
	if err := rewriteLines(filepath.Join(t.TempDir(), "f"), []any{make(chan int)}); err == nil {
		t.Error("rewriteLines of an unencodable record must fail")
	}
}
