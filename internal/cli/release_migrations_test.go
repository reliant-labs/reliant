package cli

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/buildtarget"
	"github.com/reliant-labs/forge/internal/ledgerfile"
	"github.com/reliant-labs/forge/pkg/release"
)

const reliantMigrations = "github.com/reliant-labs/reliant/db/migrations"

func migrationManifestOf(files ...string) *release.MigrationManifest {
	m := release.MigrationManifest{Sets: []release.MigrationSet{release.NewMigrationSet(reliantMigrations, "", files)}}
	return &m
}

// stubMigrationLedger is an env's ledger stated in memory: its promotion
// history, newest first, and the releases those promotions name.
type stubMigrationLedger struct {
	history  []release.Promotion
	releases map[string]release.Release
}

func (s *stubMigrationLedger) Current(context.Context, string) (release.Promotion, bool, error) {
	if len(s.history) == 0 {
		return release.Promotion{}, false, nil
	}
	return s.history[0], true, nil
}

func (s *stubMigrationLedger) Append(context.Context, release.Promotion, appendGuard) (release.Promotion, error) {
	return release.Promotion{}, errors.New("stub: read-only")
}

func (s *stubMigrationLedger) Location() string { return "stub-ledger" }

func (s *stubMigrationLedger) HistoryPage(_ context.Context, _ string, _ historyQuery) (historyPage, error) {
	return historyPage{Promotions: s.history}, nil
}

func (s *stubMigrationLedger) Cut(context.Context, release.Release) (bool, error) {
	return false, errors.New("stub: read-only")
}

func (s *stubMigrationLedger) Get(_ context.Context, version string) (*release.Release, error) {
	r, ok := s.releases[version]
	if !ok {
		return nil, nil
	}
	return &r, nil
}

func (s *stubMigrationLedger) List(context.Context) ([]release.Release, error) {
	var out []release.Release
	for _, r := range s.releases {
		out = append(out, r)
	}
	return out, nil
}

func (s *stubMigrationLedger) ledger() envLedger {
	return envLedger{Bindings: s, Releases: s, Hosted: true}
}

func relWithMigrations(version string, m *release.MigrationManifest) release.Release {
	r := ociRelease(version, map[string]string{"registry.test/acme/api": sha("1")})
	r.Migrations = m
	return r
}

// incidentLedger is prod as it stood on 2026-10-24: v1.7.52 applied, carrying
// ...035148; v1.7.54 promoted after it and REFUSED by prod's migrator (a
// failed apply); v1.7.40 applied long before, cut before manifests existed.
func incidentLedger() *stubMigrationLedger {
	failed := release.Gate{Name: applyGateName, Status: release.GateStatusFailed, Summary: "migrator refused 20261024024221"}
	return &stubMigrationLedger{
		history: []release.Promotion{
			{ID: "p3", Env: "prod", Release: "v1.7.54", RecordedGates: []release.Gate{failed}},
			{ID: "p2", Env: "prod", Release: "v1.7.52"},
			{ID: "p1", Env: "prod", Release: "v1.7.40"},
		},
		releases: map[string]release.Release{
			"v1.7.40": relWithMigrations("v1.7.40", nil),
			"v1.7.52": relWithMigrations("v1.7.52", migrationManifestOf(
				"20261023235358_access_tokens_obs_write_scope.up.sql",
				"20261024035148_hosted_http_action_tickets.up.sql")),
			"v1.7.54": relWithMigrations("v1.7.54", migrationManifestOf(
				"20261023235358_access_tokens_obs_write_scope.up.sql",
				"20261024024221_chats_worktree_binding_index.up.sql",
				"20261024035148_hosted_http_action_tickets.up.sql")),
		},
	}
}

// The incident, at the cut of the next release: it still carries 024221, and
// prod's schema (head 035148, applied with v1.7.52) has passed it. The cut is
// refused, naming the file, the head and the release that applied it — and
// v1.7.54's own files do NOT count as applied, because its deploy failed.
func TestCheckPassedMigrationsRefusesTheIncident(t *testing.T) {
	l := incidentLedger()
	target := migrationManifestOf(
		"20261024024221_chats_worktree_binding_index.up.sql",
		"20261024035148_hosted_http_action_tickets.up.sql",
		"20261024052601_daemon_attachment_build.up.sql")

	var out bytes.Buffer
	err := checkPassedMigrations(context.Background(), &out, "[build]", releaseRef{t.TempDir(), "prod", "v1.7.55"}, target, l.ledger(), false)
	var coded exitCodeError
	if !errors.As(err, &coded) || coded.code != exitRefused {
		t.Fatalf("err = %v; want an exitRefused refusal", err)
	}
	for _, want := range []string{
		"20261024024221_chats_worktree_binding_index.up.sql",
		"20261024035148_hosted_http_action_tickets.up.sql",
		"v1.7.52",
		"forge db migration rebase",
		"forge db migration new",
		"--" + allowPassedMigrationsFlag,
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("refusal does not name %q:\n%v", want, err)
		}
	}
	if strings.Contains(err.Error(), "052601") {
		t.Errorf("refusal names 052601, which is above the head and applies normally:\n%v", err)
	}
	// The release cut before manifests existed is SAID, not silently trusted.
	if !strings.Contains(out.String(), "v1.7.40") || !strings.Contains(out.String(), "backfill-migrations") {
		t.Errorf("no note naming the unrecorded release and the backfill; got:\n%s", out.String())
	}
}

// The waiver turns the refusal into a loud warning; nothing else changes.
func TestCheckPassedMigrationsWaiverWarns(t *testing.T) {
	l := incidentLedger()
	target := migrationManifestOf("20261024024221_chats_worktree_binding_index.up.sql")
	var out bytes.Buffer
	if err := checkPassedMigrations(context.Background(), &out, "[deploy]", releaseRef{t.TempDir(), "prod", "v1.7.55"}, target, l.ledger(), true); err != nil {
		t.Fatalf("waived check returned %v", err)
	}
	if !strings.Contains(out.String(), "WARNING (--"+allowPassedMigrationsFlag+")") || !strings.Contains(out.String(), "024221") {
		t.Errorf("waiver did not warn with the file named; got:\n%s", out.String())
	}
}

// The fixed release passes: 024221 replaced by a re-runnable copy above the head.
func TestCheckPassedMigrationsAdmitsTheReplacement(t *testing.T) {
	l := incidentLedger()
	target := migrationManifestOf(
		"20261024035148_hosted_http_action_tickets.up.sql",
		"20261024061500_chats_worktree_binding_index.up.sql")
	if err := checkPassedMigrations(context.Background(), &bytes.Buffer{}, "[build]", releaseRef{t.TempDir(), "prod", "v1.7.55"}, target, l.ledger(), false); err != nil {
		t.Fatalf("a release whose migrations all sort above the head was refused: %v", err)
	}
}

// A deploy reads the release's recorded manifest; a release cut before
// manifests existed is noted, never refused.
func TestCheckDeployMigrations(t *testing.T) {
	l := incidentLedger()
	if err := checkDeployMigrations(context.Background(), &bytes.Buffer{}, t.TempDir(), "prod", "v1.7.54", l.ledger(), false); err == nil {
		t.Fatal("deploying v1.7.54 again was admitted, but prod's schema has passed its 024221")
	}
	var out bytes.Buffer
	if err := checkDeployMigrations(context.Background(), &out, t.TempDir(), "prod", "v1.7.40", l.ledger(), false); err != nil {
		t.Fatalf("a release with no manifest was refused: %v", err)
	}
	if !strings.Contains(out.String(), "records no migrations") {
		t.Errorf("no note for a release with no manifest; got:\n%s", out.String())
	}
}

// A ledger that cannot list history judges nothing and says so.
func TestCheckPassedMigrationsWithoutHistoryNotes(t *testing.T) {
	l := incidentLedger()
	ledger := envLedger{Bindings: noHistoryBindings{l}, Releases: l}
	var out bytes.Buffer
	target := migrationManifestOf("20261024024221_chats_worktree_binding_index.up.sql")
	if err := checkPassedMigrations(context.Background(), &out, "[build]", releaseRef{t.TempDir(), "prod", "v9"}, target, ledger, false); err != nil {
		t.Fatalf("err = %v; want a note, not a refusal", err)
	}
	if !strings.Contains(out.String(), "cannot list") {
		t.Errorf("no note; got:\n%s", out.String())
	}
}

// noHistoryBindings hides the history capability.
type noHistoryBindings struct{ s *stubMigrationLedger }

func (n noHistoryBindings) Current(ctx context.Context, env string) (release.Promotion, bool, error) {
	return n.s.Current(ctx, env)
}

func (n noHistoryBindings) Append(ctx context.Context, p release.Promotion, g appendGuard) (release.Promotion, error) {
	return n.s.Append(ctx, p, g)
}
func (n noHistoryBindings) Location() string { return n.s.Location() }

// The cut records the project's own directory from disk — including a file
// no commit holds yet, because go:embed embeds the working tree.
func TestCaptureReleaseMigrationsReadsTheProjectFromDisk(t *testing.T) {
	dir := t.TempDir()
	mig := filepath.Join(dir, "db", "migrations")
	if err := os.MkdirAll(mig, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"20260101000000_a.up.sql", "20260102000000_b.up.sql", "README.md"} {
		if err := os.WriteFile(filepath.Join(mig, f), []byte("SELECT 1;"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	m, notes, err := captureReleaseMigrations(dir, release.Provenance{Repo: "github.com/acme/app", Commit: strings.Repeat("a", 40), Dirty: true}, nil)
	if err != nil || len(notes) != 0 {
		t.Fatalf("capture: %v, notes %v", err, notes)
	}
	if m == nil || len(m.Sets) != 1 {
		t.Fatalf("manifest = %+v; want one set", m)
	}
	s := m.Sets[0]
	if s.Source != "github.com/acme/app/db/migrations" {
		t.Errorf("source = %q", s.Source)
	}
	if s.Commit != "" {
		t.Errorf("commit = %q; a dirty cut's bytes are no commit's", s.Commit)
	}
	if strings.Join(s.Files, ",") != "20260101000000_a.up.sql,20260102000000_b.up.sql" {
		t.Errorf("files = %v", s.Files)
	}
	// A project with no migrations directory records nothing.
	if m, _, err := captureReleaseMigrations(t.TempDir(), release.Provenance{}, nil); err != nil || m != nil {
		t.Errorf("no-migrations project: manifest %+v, err %v; want nil, nil", m, err)
	}
}

// The machine ledger fills a manifest once, accepts an equal one as a
// no-op, and refuses a different one.
func TestMachineLedgerRecordsMigrationsOnce(t *testing.T) {
	store, err := ledgerfile.Open(t.TempDir(), "proj")
	if err != nil {
		t.Fatal(err)
	}
	ledger := machineReleaseLedger{store: store}
	ctx := context.Background()
	if _, err := ledger.Cut(ctx, relWithMigrations("v1", nil)); err != nil {
		t.Fatal(err)
	}
	m := migrationManifestOf("00001_a.up.sql")
	if rec, err := ledger.RecordMigrations(ctx, "v1", *m); err != nil || !rec {
		t.Fatalf("first record = %v, %v; want recorded", rec, err)
	}
	got, err := ledger.Get(ctx, "v1")
	if err != nil || got == nil || got.Migrations == nil || !got.Migrations.Equal(*m) {
		t.Fatalf("read back %+v, %v", got, err)
	}
	if rec, err := ledger.RecordMigrations(ctx, "v1", *m); err != nil || rec {
		t.Fatalf("equal record = %v, %v; want a no-op", rec, err)
	}
	if _, err := ledger.RecordMigrations(ctx, "v1", *migrationManifestOf("00002_b.up.sql")); !errors.Is(err, release.ErrReleaseConflict) {
		t.Fatalf("different record = %v; want ErrReleaseConflict", err)
	}
	if _, err := ledger.RecordMigrations(ctx, "v9", *m); !errors.Is(err, ledgerfile.ErrReleaseNotFound) {
		t.Fatalf("unknown release = %v; want ErrReleaseNotFound", err)
	}
	// A re-cut of the same artifacts stays an idempotent retry.
	if created, err := ledger.Cut(ctx, relWithMigrations("v1", nil)); err != nil || created {
		t.Fatalf("re-cut = %v, %v; want an idempotent no-op", created, err)
	}
}

// The manifest crosses the wire both ways: a cut sends it and reads it back,
// and the backfill rpc fills one on a release cut without.
func TestHostedStore_MigrationsRoundTripAndRecord(t *testing.T) {
	fake := newFakeDeployService(map[string]string{"prod": "env-prod-uuid"})
	store, _ := newHostedTestStore(t, fake)
	ctx := context.Background()
	m := migrationManifestOf("20261024035148_hosted_http_action_tickets.up.sql")

	if _, err := store.Cut(ctx, relWithMigrations("v2", m)); err != nil {
		t.Fatalf("cut: %v", err)
	}
	got, err := store.Get(ctx, "v2")
	if err != nil || got == nil || got.Migrations == nil || !got.Migrations.Equal(*m) {
		t.Fatalf("read back %+v, %v; want the manifest", got, err)
	}

	if _, err := store.Cut(ctx, relWithMigrations("v1", nil)); err != nil {
		t.Fatalf("cut: %v", err)
	}
	if rec, err := store.RecordMigrations(ctx, "v1", *m); err != nil || !rec {
		t.Fatalf("record = %v, %v; want recorded", rec, err)
	}
	if rec, err := store.RecordMigrations(ctx, "v1", *m); err != nil || rec {
		t.Fatalf("equal record = %v, %v; want a no-op", rec, err)
	}
	if _, err := store.RecordMigrations(ctx, "v1", *migrationManifestOf("00001_x.up.sql")); !errors.Is(err, release.ErrReleaseConflict) {
		t.Fatalf("different record = %v; want ErrReleaseConflict", err)
	}
}

// --- git-backed: (A) and the backfill. Each execs git over a temp repository. ---

func migGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func writeMigFile(t *testing.T, dir, name string) {
	t.Helper()
	mig := filepath.Join(dir, "db", "migrations")
	if err := os.MkdirAll(mig, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(mig, name), []byte("SELECT 1;"), 0o644); err != nil {
		t.Fatal(err)
	}
}

// (A): a release for a shared env carrying the project's migration that main
// does not have is refused; once it is on main, it passes.
func TestCheckUnmergedMigrations(t *testing.T) {
	if testing.Short() {
		t.Skip("execs git over a temp repository; runs in task test")
	}
	dir := t.TempDir()
	migGit(t, dir, "init", "-q", "-b", "main")
	writeMigFile(t, dir, "20260101000000_a.up.sql")
	migGit(t, dir, "add", "-A")
	migGit(t, dir, "commit", "-q", "-m", "a")
	migGit(t, dir, "checkout", "-q", "-b", "hotfix")
	writeMigFile(t, dir, "20260102000000_b.up.sql")
	migGit(t, dir, "add", "-A")
	migGit(t, dir, "commit", "-q", "-m", "b")

	err := checkUnmergedMigrations(&bytes.Buffer{}, "[build]", dir, "v2", false)
	var coded exitCodeError
	if !errors.As(err, &coded) || coded.code != exitRefused || !strings.Contains(err.Error(), "20260102000000_b.up.sql") {
		t.Fatalf("err = %v; want a refusal naming the unmerged file", err)
	}
	if strings.Contains(err.Error(), "20260101000000_a") {
		t.Errorf("refusal names a migration main already has:\n%v", err)
	}
	var out bytes.Buffer
	if err := checkUnmergedMigrations(&out, "[build]", dir, "v2", true); err != nil || !strings.Contains(out.String(), "WARNING") {
		t.Fatalf("waived: err %v, out %q; want a warning", err, out.String())
	}
	migGit(t, dir, "checkout", "-q", "main")
	migGit(t, dir, "merge", "-q", "hotfix")
	if err := checkUnmergedMigrations(&bytes.Buffer{}, "[build]", dir, "v2", false); err != nil {
		t.Fatalf("after merging, err = %v; want nil", err)
	}
}

// The backfill reconstructs a release's manifest from git: the project's own
// directory at the release's commit, and a sibling's at the commit the
// project's go.mod pinned THEN — not the sibling's current HEAD.
func TestReconstructReleaseMigrationsFollowsTheHistoricalPin(t *testing.T) {
	if testing.Short() {
		t.Skip("execs git over temp repositories; runs in task test")
	}
	root := t.TempDir()
	sib := filepath.Join(root, "reliant")
	proj := filepath.Join(root, "cp")
	for _, d := range []string{sib, proj} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
		migGit(t, d, "init", "-q", "-b", "main")
	}
	// The sibling: a forge project, two commits.
	if err := os.WriteFile(filepath.Join(sib, "forge.yaml"), []byte("name: reliant\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	writeMigFile(t, sib, "20261024035148_hosted.up.sql")
	migGit(t, sib, "add", "-A")
	migGit(t, sib, "commit", "-q", "-m", "one")
	pinned := migGit(t, sib, "rev-parse", "HEAD")
	writeMigFile(t, sib, "20261024024221_index.up.sql")
	migGit(t, sib, "add", "-A")
	migGit(t, sib, "commit", "-q", "-m", "two")

	// The project pins the sibling's FIRST commit.
	gomod := "module example.com/cp\n\ngo 1.22\n\nrequire example.com/reliant v0.0.0-20261024000000-" + pinned[:12] + "\n"
	if err := os.WriteFile(filepath.Join(proj, "go.mod"), []byte(gomod), 0o644); err != nil {
		t.Fatal(err)
	}
	writeMigFile(t, proj, "00001_cp.up.sql")
	migGit(t, proj, "add", "-A")
	migGit(t, proj, "commit", "-q", "-m", "cp")
	cpCommit := migGit(t, proj, "rev-parse", "HEAD")

	rel := relWithMigrations("v1.7.52", nil)
	rel.SetProvenance(release.Provenance{Repo: "github.com/acme/cp", Commit: cpCommit})
	src := &buildtarget.Source{Dir: sib, Repo: "github.com/acme/reliant", Module: "example.com/reliant", ModuleDir: "."}
	m, _, err := reconstructReleaseMigrations(context.Background(), proj, rel, []*buildtarget.Source{src})
	if err != nil {
		t.Fatalf("reconstruct: %v", err)
	}
	bySource := map[string]release.MigrationSet{}
	for _, s := range m.Sets {
		bySource[s.Source] = s
	}
	if s := bySource["github.com/acme/cp/db/migrations"]; strings.Join(s.Files, ",") != "00001_cp.up.sql" || s.Commit != cpCommit {
		t.Errorf("project set = %+v", s)
	}
	s := bySource["github.com/acme/reliant/db/migrations"]
	if strings.Join(s.Files, ",") != "20261024035148_hosted.up.sql" || s.Commit != pinned {
		t.Errorf("sibling set = %+v; want only the file at the PINNED commit %s", s, pinned)
	}
}
