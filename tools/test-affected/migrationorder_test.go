// Copyright (c) 2025 Reliant Labs
package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// orderRepo builds a bare "origin", a clone of it, and returns the clone. main
// carries onMain; the clone then branches to "feature". Further commits to
// main go through advanceMain, which leaves the clone's origin/main stale until
// something fetches.
type orderRepo struct {
	t      *testing.T
	origin string
	clone  string
}

func (r *orderRepo) git(dir string, args ...string) string {
	r.t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1",
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.invalid",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.invalid")
	out, err := cmd.CombinedOutput()
	if err != nil {
		r.t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return string(out)
}

func (r *orderRepo) write(dir, name string) {
	r.t.Helper()
	p := filepath.Join(dir, "db", "migrations", name)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		r.t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte("SELECT 1;\n"), 0o644); err != nil {
		r.t.Fatal(err)
	}
}

// Each orderRepo is three git repositories of its own under t.TempDir(), and
// building and checking one is a few dozen git processes: on a loaded host
// that is tens of seconds a test. The tests that use one run in parallel, or
// the package's serial sum (240s at load 100) runs into the 5-minute package
// timeout of the race-enabled release gate.
func newOrderRepo(t *testing.T, onMain ...string) *orderRepo {
	t.Helper()
	base := t.TempDir()
	r := &orderRepo{t: t, origin: filepath.Join(base, "origin.git"), clone: filepath.Join(base, "clone")}
	r.git(base, "init", "-q", "--bare", "-b", "main", r.origin)
	seed := filepath.Join(base, "seed")
	r.git(base, "clone", "-q", r.origin, seed)
	r.git(seed, "checkout", "-q", "-b", "main")
	for _, n := range onMain {
		r.write(seed, n)
	}
	if len(onMain) == 0 {
		r.write(seed, "00001_baseline.up.sql")
	}
	r.git(seed, "add", "-A")
	r.git(seed, "commit", "-q", "-m", "main")
	r.git(seed, "push", "-q", "origin", "main")
	r.git(base, "clone", "-q", r.origin, r.clone)
	r.git(r.clone, "checkout", "-q", "-b", "feature")
	return r
}

// advanceMain lands one more migration on the remote main without touching the
// clone, exactly as another PR merging while this one is in review does.
func (r *orderRepo) advanceMain(name string) {
	r.t.Helper()
	other := filepath.Join(filepath.Dir(r.clone), "other")
	if _, err := os.Stat(other); err != nil {
		r.git(filepath.Dir(r.clone), "clone", "-q", r.origin, other)
	}
	r.git(other, "checkout", "-q", "main")
	r.git(other, "pull", "-q", "origin", "main")
	r.write(other, name)
	r.git(other, "add", "-A")
	r.git(other, "commit", "-q", "-m", "main moves: "+name)
	r.git(other, "push", "-q", "origin", "main")
}

func (r *orderRepo) addOnBranch(names ...string) {
	r.t.Helper()
	for _, n := range names {
		r.write(r.clone, n)
	}
	r.git(r.clone, "add", "-A")
	r.git(r.clone, "commit", "-q", "-m", "feature")
}

func (r *orderRepo) check(opts orderOptions) ([]orderViolation, error) {
	opts.Root = r.clone
	if opts.BaseRef == "" {
		opts.BaseRef = "origin/main"
	}
	if opts.Dir == "" {
		opts.Dir = "db/migrations"
	}
	return checkMigrationOrder(opts)
}

func TestMigrationBelowMainsHeadFails(t *testing.T) {
	t.Parallel()
	r := newOrderRepo(t, "20261024035148_hosted_http_action_tickets.up.sql")
	r.addOnBranch("20261024024221_chats_worktree_binding_index.up.sql")

	vs, err := r.check(orderOptions{Fetch: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(vs) != 1 {
		t.Fatalf("violations = %+v, want exactly the branch's low migration", vs)
	}
	var out bytes.Buffer
	formatOrderViolations(&out, vs)
	msg := out.String()
	for _, want := range []string{
		"20261024024221_chats_worktree_binding_index.up.sql",
		"20261024024221",             // this migration's version
		"20261024035148",             // main's head
		"hosted_http_action_tickets", // main's head, by name
		"task db:migration NAME=chats_worktree_binding_index",
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("message does not contain %q:\n%s", want, msg)
		}
	}
}

func TestMigrationAboveMainsHeadPasses(t *testing.T) {
	t.Parallel()
	r := newOrderRepo(t, "20261024035148_hosted_http_action_tickets.up.sql")
	r.addOnBranch("20261024053145_daemon_attachment_build.up.sql")

	vs, err := r.check(orderOptions{Fetch: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(vs) != 0 {
		t.Fatalf("a migration above main's head must pass, got %+v", vs)
	}
}

// Merged history is below the head by definition and must never be flagged:
// only a file main does not carry (by name) is a candidate.
func TestMergedHistoryBelowTheHeadIsNeverFlagged(t *testing.T) {
	t.Parallel()
	r := newOrderRepo(t,
		"20261001000000_old.up.sql",
		"20261024035148_head.up.sql")
	vs, err := r.check(orderOptions{Fetch: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(vs) != 0 {
		t.Fatalf("migrations already on main were flagged: %+v", vs)
	}
}

// A version equal to main's head under a different name is a collision, and
// as unapplyable as a lower one.
func TestSameVersionAsMainsHeadUnderAnotherNameFails(t *testing.T) {
	t.Parallel()
	r := newOrderRepo(t, "20261024035148_head.up.sql")
	r.addOnBranch("20261024035148_other.up.sql")
	vs, err := r.check(orderOptions{Fetch: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(vs) != 1 {
		t.Fatalf("violations = %+v, want the colliding file", vs)
	}
}

// A branch that is merely BEHIND main still carries files main has since
// renamed (reliant#1450 re-versioned the very migration #1399 added). They were
// at the merge-base, so this branch did not add them and a squash merge will
// not bring them back: flagging them would fail every stale branch for
// someone else's migration.
func TestFileMainRenamedAfterTheBranchWasCutIsNotTheBranchsToFix(t *testing.T) {
	t.Parallel()
	r := newOrderRepo(t, "20261024024221_chats_worktree_binding_index.up.sql", "20261024035148_hosted_http_action_tickets.up.sql")
	r.advanceMain("20261024173535_other.up.sql")
	other := filepath.Join(filepath.Dir(r.clone), "other")
	r.git(other, "mv", "db/migrations/20261024024221_chats_worktree_binding_index.up.sql", "db/migrations/20261024180000_chats_worktree_binding_index.up.sql")
	r.git(other, "commit", "-q", "-am", "re-version the migration")
	r.git(other, "push", "-q", "origin", "main")

	r.addOnBranch("20261025000000_unrelated.up.sql")
	vs, err := r.check(orderOptions{Fetch: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(vs) != 0 {
		t.Fatalf("a file the branch did not add was flagged: %+v", vs)
	}
}

// THE REASON THE CHECK FETCHES. main moved after this branch last synced, so
// the clone's origin/main is behind: judged against the stale ref the low
// migration looks fine, which is how reliant#1399 merged.
func TestStaleOriginMainPassesWithoutAFetchAndFailsWithOne(t *testing.T) {
	t.Parallel()
	r := newOrderRepo(t, "20261001000000_old.up.sql")
	r.addOnBranch("20261024024221_chats_worktree_binding_index.up.sql")
	r.advanceMain("20261024035148_hosted_http_action_tickets.up.sql")

	stale, err := r.check(orderOptions{Fetch: false})
	if err != nil {
		t.Fatal(err)
	}
	if len(stale) != 0 {
		t.Fatalf("precondition: the stale ref should not know about the new head, got %+v", stale)
	}

	fresh, err := r.check(orderOptions{Fetch: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(fresh) != 1 {
		t.Fatalf("after a fetch the low migration must fail, got %+v", fresh)
	}
}

// A check that cannot reach main cannot say the order is right. It fails
// closed rather than passing over a ref it could not refresh.
func TestFetchFailureFailsClosed(t *testing.T) {
	t.Parallel()
	r := newOrderRepo(t, "20261001000000_old.up.sql")
	r.addOnBranch("20261024024221_x.up.sql")
	r.git(r.clone, "remote", "set-url", "origin", filepath.Join(t.TempDir(), "gone.git"))

	_, err := r.check(orderOptions{Fetch: true})
	if err == nil {
		t.Fatal("an unreachable origin must be an error, not a pass")
	}
	if !strings.Contains(err.Error(), "MIGRATION_ORDER_OFFLINE") {
		t.Errorf("the error must name the offline override: %v", err)
	}
}

func TestNoBaseRefFailsClosed(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	cmd := exec.Command("git", "init", "-q", "-b", "main")
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	_, err := checkMigrationOrder(orderOptions{Root: dir, BaseRef: "origin/main", Dir: "db/migrations"})
	if err == nil {
		t.Fatal("no origin/main to compare against must be an error, not a pass")
	}
}

func TestNoMigrationsDirectoryPasses(t *testing.T) {
	t.Parallel()
	r := newOrderRepo(t)
	if err := os.RemoveAll(filepath.Join(r.clone, "db")); err != nil {
		t.Fatal(err)
	}
	vs, err := r.check(orderOptions{Fetch: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(vs) != 0 {
		t.Fatalf("got %+v", vs)
	}
}

// An uncommitted migration is judged too: the check runs from a working tree
// before anything is committed.
func TestUncommittedMigrationIsJudged(t *testing.T) {
	t.Parallel()
	r := newOrderRepo(t, "20261024035148_head.up.sql")
	r.write(r.clone, "20261024024221_low.up.sql")
	vs, err := r.check(orderOptions{Fetch: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(vs) != 1 {
		t.Fatalf("got %+v", vs)
	}
}

func TestMigrationOrderCommandExitCodes(t *testing.T) {
	t.Parallel()
	r := newOrderRepo(t, "20261024035148_head.up.sql")
	r.addOnBranch("20261024024221_low.up.sql")
	var out, errOut bytes.Buffer
	if code := runMigrationOrder([]string{"-root", r.clone}, &out, &errOut); code != 1 {
		t.Fatalf("exit = %d, want 1 for a violation\nstdout: %s\nstderr: %s", code, out.String(), errOut.String())
	}
	if !strings.Contains(errOut.String(), "20261024035148") {
		t.Errorf("the failure must be printed on stderr: %q", errOut.String())
	}
	r.git(r.clone, "rm", "-q", "-f", "db/migrations/20261024024221_low.up.sql")
	r.addOnBranch("20261025000000_ok.up.sql")
	out.Reset()
	errOut.Reset()
	if code := runMigrationOrder([]string{"-root", r.clone}, &out, &errOut); code != 0 {
		t.Fatalf("exit = %d, want 0\nstdout: %s\nstderr: %s", code, out.String(), errOut.String())
	}
}

// The lane runs when, and only when, a migration is added or changed.
func TestMigrationOrderLaneSelection(t *testing.T) {
	for _, p := range []string{"db/migrations/20261024024221_x.up.sql", "db/migrations/README.md"} {
		if pl := classify(p); !pl.MigOrder.Full {
			t.Errorf("%s: migration-order lane must run", p)
		}
	}
	for _, p := range []string{"internal/leaf/leaf.go", "docs/x.md", "db/seeds/x.sql", "frontends/web/src/lib/leaf.ts"} {
		if pl := classify(p); pl.MigOrder.Full {
			t.Errorf("%s: migration-order lane must not run (%v)", p, pl.MigOrder.Reasons)
		}
	}
	// A deletion cannot put a file below the head.
	if pl := ClassifyReach([]Change{{Path: "db/migrations/20261024024221_x.up.sql", Deleted: true}}, testGraph(), ReachAll); pl.MigOrder.Full {
		t.Errorf("a deleted migration must not run the lane: %v", pl.MigOrder.Reasons)
	}
}

func TestRunnerRunsMigrationOrderFirstAndOnlyWhenEnabled(t *testing.T) {
	pl := classify("db/migrations/20261024024221_x.up.sql")
	steps := (&runner{root: "/repo"}).steps(pl, laneSet("go,web,electron,forge-generate,docs-generate,migration-order"))
	if len(steps) == 0 || !strings.Contains(strings.Join(steps[0].argv, " "), "migration-order") || !steps[0].prerequisite || steps[0].dir != "/repo" {
		t.Fatalf("migration-order must be the first prerequisite, from the repo root: %+v", steps)
	}
	for _, s := range (&runner{root: "/repo"}).steps(pl, laneSet("go,web")) {
		if strings.Contains(strings.Join(s.argv, " "), "migration-order") {
			t.Fatal("-lanes without migration-order must not run it")
		}
	}
}

// The default lane list is what `task test:affected` runs: it must include the
// migration-order lane, or the gate silently stops covering migrations.
func TestDefaultLanesIncludeMigrationOrder(t *testing.T) {
	if !strings.Contains(defaultLanes, string(laneMigOrder)) {
		t.Fatalf("defaultLanes %q omits %s", defaultLanes, laneMigOrder)
	}
	for _, l := range allLanes {
		if !strings.Contains(defaultLanes, string(l)) {
			t.Errorf("defaultLanes %q omits lane %s", defaultLanes, l)
		}
	}
}
