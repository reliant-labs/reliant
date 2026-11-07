package storage

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/reliant-labs/forge/internal/openfiles"
)

// gitRepoWithWorktree builds a real repository whose only commit is 60 days
// old, plus a linked worktree checked out at that same commit — merged into
// main by construction, so it passes every age and ancestry test Worktrees
// applies. Whatever the worktree holds is the only thing left to decide on.
func gitRepoWithWorktree(t *testing.T) (repo, worktree string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	// Isolate from the developer's git config: a global hook or signing
	// setting must not decide whether this fixture can be built.
	empty := filepath.Join(t.TempDir(), "gitconfig")
	if err := os.WriteFile(empty, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GIT_CONFIG_GLOBAL", empty)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	old := time.Now().Add(-60 * 24 * time.Hour).Format(time.RFC3339)
	root := t.TempDir()
	repo = filepath.Join(root, "repo")
	worktree = filepath.Join(root, "feature")
	git := func(dir string, args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir, "-c", "user.name=t", "-c", "user.email=t@example.com", "-c", "commit.gpgsign=false"}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_DATE="+old, "GIT_COMMITTER_DATE="+old)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	if err := os.MkdirAll(repo, 0o700); err != nil {
		t.Fatal(err)
	}
	git(repo, "init", "-q", "-b", "main")
	if err := os.WriteFile(filepath.Join(repo, ".gitignore"), []byte("data/\n.env\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	git(repo, "add", ".gitignore")
	git(repo, "commit", "-q", "-m", "init")
	git(repo, "worktree", "add", "-q", "-b", "feature", worktree)
	return repo, worktree
}

// ageDir backdates a worktree — every entry in it, since anything modified
// recently is activity — so its mtimes are not what keeps it.
func ageDir(t *testing.T, dir string) {
	t.Helper()
	stamp := time.Now().Add(-60 * 24 * time.Hour)
	err := filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.Type()&os.ModeSymlink != 0 {
			return nil
		}
		return os.Chtimes(p, stamp, stamp)
	})
	if err != nil {
		t.Fatal(err)
	}
	// Activity includes the worktree's git admin files, fresh from `worktree add`.
	if admin := worktreeAdminDir(dir); admin != "" {
		for _, name := range []string{"index", "HEAD", "logs/HEAD"} {
			_ = os.Chtimes(filepath.Join(admin, name), stamp, stamp)
		}
	}
}

// TestWorktreesRetainIgnoredAndUntrackedData is the B2 regression. `git
// status --porcelain` never lists IGNORED files, and `git worktree remove`
// without --force deletes them — so a worktree holding a gitignored database
// (reliant's ./data/, forge hostinfra's .forge/hostinfra/<name> postgres dir)
// or a gitignored .env looked clean and was deleted along with them.
//
// Real git throughout: what is being pinned is git's behaviour, which a faked
// `git status` would only restate.
func TestWorktreesRetainIgnoredAndUntrackedData(t *testing.T) {
	if testing.Short() {
		t.Skip("builds real git repositories and worktrees; runs in task test")
	}
	for _, tc := range []struct {
		name  string
		files []string
	}{
		{"ignored data directory", []string{"data/db.sqlite"}},
		{"ignored file", []string{".env"}},
		{"untracked file", []string{"notes.txt"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo, worktree := gitRepoWithWorktree(t)
			for _, rel := range tc.files {
				path := filepath.Join(worktree, rel)
				if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte("irreplaceable"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			ageDir(t, worktree)
			fakeLsof(t, unrelatedOpenFile, "exit 0")

			var out strings.Builder
			r := Runner{Out: &out}
			if err := r.Worktrees(context.Background(), repo, "main", 24*time.Hour, true); err != nil {
				t.Fatalf("Worktrees: %v\n%s", err, out.String())
			}
			for _, rel := range tc.files {
				if _, err := os.Stat(filepath.Join(worktree, rel)); err != nil {
					t.Fatalf("worktrees --apply deleted %s (%v)\n%s", rel, err, out.String())
				}
			}
		})
	}
}

// TestWorktreesRemoveATrulyCleanWorktree is the positive control for the test
// above: without it, a Worktrees that never removed anything would pass.
func TestWorktreesRemoveATrulyCleanWorktree(t *testing.T) {
	if testing.Short() {
		t.Skip("builds real git repositories and worktrees; runs in task test")
	}
	repo, worktree := gitRepoWithWorktree(t)
	ageDir(t, worktree)
	fakeLsof(t, unrelatedOpenFile, "exit 0")
	var out strings.Builder
	if err := (Runner{Out: &out}).Worktrees(context.Background(), repo, "main", 24*time.Hour, true); err != nil {
		t.Fatalf("Worktrees: %v\n%s", err, out.String())
	}
	if _, err := os.Stat(worktree); !os.IsNotExist(err) {
		t.Fatalf("a clean, merged, idle worktree was not removed (%v)\n%s", err, out.String())
	}
	// The canonical target is named BEFORE anything is removed.
	announce := strings.Index(out.String(), "removing worktree "+resolved(t, filepath.Dir(worktree))+string(filepath.Separator)+filepath.Base(worktree))
	done := strings.Index(out.String(), "removed worktree")
	if announce < 0 || done < announce {
		t.Fatalf("the canonical target was not printed before removal:\n%s", out.String())
	}
}

// TestWorktreesRetainAWorktreeAProcessIsUsing: the only in-use test used to
// be "is it forge's OWN cwd". A dev stack, a shell or an editor sitting in
// the worktree passed that and lost its directory. lsof reports the RESOLVED
// path (macOS: /private/var/... for /var/...), so this also pins that the
// comparison is made on canonical paths.
func TestWorktreesRetainAWorktreeAProcessIsUsing(t *testing.T) {
	if testing.Short() {
		t.Skip("builds real git repositories and worktrees; runs in task test")
	}
	for _, tc := range []struct{ name, record string }{
		{"process cwd inside the worktree", "fcwd\nn%s/sub\n"},
		{"process cwd is the worktree", "fcwd\nn%s\n"},
		{"file open inside the worktree", "f7\nn%s/README\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo, worktree := gitRepoWithWorktree(t)
			ageDir(t, worktree)
			fakeLsof(t, "p4242\n"+strings.ReplaceAll(tc.record, "%s", resolved(t, worktree)), "exit 0")
			var out strings.Builder
			if err := (Runner{Out: &out}).Worktrees(context.Background(), repo, "main", 24*time.Hour, true); err != nil {
				t.Fatalf("Worktrees: %v\n%s", err, out.String())
			}
			if _, err := os.Stat(worktree); err != nil {
				t.Fatalf("removed a worktree a live process is using (%v)\n%s", err, out.String())
			}
		})
	}
}

// TestWorktreesRefuseWhenUseCannotBeDetermined: with no evidence about which
// worktrees are in use, removal must not proceed on age alone.
func TestWorktreesRefuseWhenUseCannotBeDetermined(t *testing.T) {
	if testing.Short() {
		t.Skip("builds real git repositories and worktrees; runs in task test")
	}
	repo, worktree := gitRepoWithWorktree(t)
	ageDir(t, worktree)
	fakeLsof(t, "", "exit 2")
	var out strings.Builder
	err := (Runner{Out: &out}).Worktrees(context.Background(), repo, "main", 24*time.Hour, true)
	if _, statErr := os.Stat(worktree); statErr != nil {
		t.Fatalf("removed a worktree with no in-use evidence (%v)\n%s", statErr, out.String())
	}
	if err == nil {
		t.Fatalf("an unknowable in-use set must be reported, not silently passed:\n%s", out.String())
	}
}

func runGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	// No automatic maintenance. After a commit git may start `maintenance run
	// --auto` DETACHED, which creates and deletes .git/objects/maintenance.lock
	// behind the test's back: ageDir walked the tree, the lock vanished before
	// its chtimes, and TestWorktreeOtherNestedRepositoriesStillHold failed 2
	// of main's last 40 runs with "chtimes .../maintenance.lock: no such file
	// or directory".
	cmd := exec.Command("git", append([]string{"-C", dir, "-c", "user.name=t", "-c", "user.email=t@example.com", "-c", "commit.gpgsign=false",
		"-c", "maintenance.auto=false", "-c", "gc.auto=0"}, args...)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return string(out)
}

func reapOnce(t *testing.T, r Runner, repo string, apply bool) WorktreeReport {
	t.Helper()
	report, err := r.reapWorktrees(context.Background(), reapOptions{repos: []string{repo}, base: "main", idle: 24 * time.Hour, apply: apply})
	if err != nil {
		t.Fatal(err)
	}
	return report
}

func heldReason(report WorktreeReport, path string) string {
	path, _ = filepath.EvalSymlinks(path)
	for _, h := range report.Held {
		if h.Path == path {
			return h.Reason
		}
	}
	return ""
}

func TestWorktreeAllowsRebuildableIgnoredAndHoldsTheRest(t *testing.T) {
	if testing.Short() {
		t.Skip("builds real git repositories and worktrees; runs in task test")
	}
	for _, tc := range []struct {
		name, file, want string
	}{
		{"node_modules", "web/node_modules/x/index.js", ""},
		{"build output", "dist/app.js", ""},
		{"app data", "data/db.sqlite", HoldData},
		{"env file", ".env", HoldData},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo, worktree := gitRepoWithWorktree(t)
			ignore := filepath.Join(repo, ".git", "info", "exclude")
			if err := os.WriteFile(ignore, []byte("node_modules/\ndist/\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(worktree, tc.file)
			if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
				t.Fatal(err)
			}
			ageDir(t, worktree)
			fakeLsof(t, unrelatedOpenFile, "exit 0")
			report := reapOnce(t, Runner{}, repo, false)
			if got := heldReason(report, worktree); got != tc.want {
				t.Fatalf("held reason = %q, want %q (removable %v, held %+v)", got, tc.want, report.Removable, report.Held)
			}
		})
	}
}

func TestWorktreeHoldsLockedRecentAndUnpushed(t *testing.T) {
	if testing.Short() {
		t.Skip("builds real git repositories and worktrees; runs in task test")
	}
	t.Run("locked", func(t *testing.T) {
		repo, worktree := gitRepoWithWorktree(t)
		runGit(t, repo, "worktree", "lock", worktree)
		ageDir(t, worktree)
		fakeLsof(t, unrelatedOpenFile, "exit 0")
		if got := heldReason(reapOnce(t, Runner{}, repo, true), worktree); got != HoldLocked {
			t.Fatalf("got %q", got)
		}
		if _, err := os.Stat(worktree); err != nil {
			t.Fatal("locked worktree removed")
		}
	})
	t.Run("recently active", func(t *testing.T) {
		repo, worktree := gitRepoWithWorktree(t)
		ageDir(t, worktree)
		// A fresh index (git add, checkout) is activity even if the dir mtime is old.
		now := time.Now()
		if err := os.Chtimes(filepath.Join(worktreeAdminDir(worktree), "index"), now, now); err != nil {
			t.Fatal(err)
		}
		fakeLsof(t, unrelatedOpenFile, "exit 0")
		if got := heldReason(reapOnce(t, Runner{}, repo, false), worktree); got != HoldActive {
			t.Fatalf("got %q", got)
		}
	})
	t.Run("unpushed", func(t *testing.T) {
		repo, worktree := gitRepoWithWorktree(t)
		if err := os.WriteFile(filepath.Join(worktree, "new.txt"), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		runGit(t, worktree, "add", "new.txt")
		runGit(t, worktree, "commit", "-q", "-m", "work")
		ageDir(t, worktree)
		fakeLsof(t, unrelatedOpenFile, "exit 0")
		if got := heldReason(reapOnce(t, Runner{}, repo, true), worktree); got != HoldUnpushed {
			t.Fatalf("got %q", got)
		}
		if _, err := os.Stat(worktree); err != nil {
			t.Fatal("unpushed worktree removed")
		}
	})
}

func TestWorktreePushedToRemoteBranchIsRemovable(t *testing.T) {
	if testing.Short() {
		t.Skip("builds real git repositories and worktrees; runs in task test")
	}
	repo, worktree := gitRepoWithWorktree(t)
	if err := os.WriteFile(filepath.Join(worktree, "new.txt"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGit(t, worktree, "add", "new.txt")
	runGit(t, worktree, "commit", "-q", "-m", "work")
	head := strings.TrimSpace(runGit(t, worktree, "rev-parse", "HEAD"))
	// A remote-tracking ref containing HEAD, as `git push` would leave.
	runGit(t, repo, "update-ref", "refs/remotes/origin/feature", head)
	ageDir(t, worktree)
	fakeLsof(t, unrelatedOpenFile, "exit 0")
	report := reapOnce(t, Runner{}, repo, true)
	if len(report.Removed) != 1 {
		t.Fatalf("pushed idle worktree not removed: held %v", report.Held)
	}
	if _, err := os.Stat(worktree); !os.IsNotExist(err) {
		t.Fatal("worktree still exists")
	}
	if !strings.Contains(runGit(t, repo, "branch", "--list", "feature"), "feature") {
		t.Fatal("branch was deleted")
	}
}

// The GC layer removes only when BOTH hold: the policy opts in (worktree_reap)
// and the pass is an explicit `forge storage gc --apply` (Explicit). The
// installed schedule and `storage daemon` run the same layer with
// Explicit false.
func TestWorktreeLayerRemovesOnlyInAnExplicitOptedInPass(t *testing.T) {
	if testing.Short() {
		t.Skip("builds real git repositories and worktrees; runs in task test")
	}
	for _, tc := range []struct{ policy, explicit, wantGone bool }{
		{false, false, false},
		{false, true, false},
		{true, false, false},
		{true, true, true},
	} {
		repo, worktree := gitRepoWithWorktree(t)
		runGit(t, repo, "update-ref", "refs/remotes/origin/main", "main")
		ageDir(t, worktree)
		fakeLsof(t, unrelatedOpenFile, "exit 0")
		var out strings.Builder
		r := Runner{Policy: Policy{Repos: []string{repo}, WorktreeReap: tc.policy}, Explicit: tc.explicit, Out: &out}
		r.Ctx = context.Background()
		if err := r.worktreeLayer(true); err != nil {
			t.Fatal(err)
		}
		_, err := os.Stat(worktree)
		if gone := os.IsNotExist(err); gone != tc.wantGone {
			t.Fatalf("worktree_reap=%v explicit=%v: removed=%v, want %v\n%s", tc.policy, tc.explicit, gone, tc.wantGone, out.String())
		}
	}
}

// TestAutomaticGCNeverReapsAWorktree is the 2026-10-09 regression. The
// non-disruptive pass — started in the background by `forge env up` and
// hourly by the installed schedule — ran the worktree layer, so on a machine
// whose policy opted in to worktree_reap it removed any worktree that looked
// idle, clean and pushed. An agent between two commands holds no process in
// its worktree and need not have written to it for a day, so that is exactly
// how a worktree in use looks. No automatic pass may remove one.
func TestAutomaticGCNeverReapsAWorktree(t *testing.T) {
	if testing.Short() {
		t.Skip("builds real git repositories and worktrees; runs in task test")
	}
	repo, worktree := gitRepoWithWorktree(t)
	runGit(t, repo, "update-ref", "refs/remotes/origin/main", "main") // pushed
	ageDir(t, worktree)                                               // stale-looking
	fakeLsof(t, unrelatedOpenFile, "exit 0")                          // nothing holds it open
	p := DefaultPolicy()
	p.Builders = nil
	p.Repos = []string{repo}
	p.WorktreeReap = true // even where the policy opts in to reaping
	var out strings.Builder
	r := Runner{
		Policy: p, Out: &out,
		OpenPaths: func(context.Context) (openfiles.Snapshot, error) {
			return openfiles.FromPaths([]string{"/nonexistent/forge-storage-test/unrelated"}), nil
		},
		Command: func(ctx context.Context, name string, args ...string) ([]byte, error) {
			if name == "docker" {
				return nil, errors.New("no docker in this test")
			}
			return Exec(ctx, name, args...)
		},
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	_ = r.NonDisruptiveGC(ctx, true)
	if _, err := os.Stat(worktree); err != nil {
		t.Fatalf("the automatic pass removed a clean, pushed, idle-looking worktree (%v)\n%s", err, out.String())
	}
}

// Explicit reaping refuses anything that is not plainly a linked worktree of
// the repository, whatever else is true of it.
func TestWorktreeRulesRefuseCheckoutsAndHome(t *testing.T) {
	root := resolved(t, t.TempDir())
	main := filepath.Join(root, "repo")
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home directory")
	}
	rules := worktreeRules{main: main, common: filepath.Join(main, ".git")}
	for _, tc := range []struct{ name, path string }{
		{"the main checkout", main},
		{"a directory containing the main checkout", root},
		{"the home directory", home},
		{"a directory containing the home directory", filepath.Dir(canonicalPath(home))},
		{"a path with no .git file", filepath.Join(root, "elsewhere")},
	} {
		if reason, _ := rules.refuse(tc.path); reason != HoldNotLinked {
			t.Errorf("%s (%s): reason %q, want %q", tc.name, tc.path, reason, HoldNotLinked)
		}
	}
}

// A registration whose directory now holds a checkout of its own (its .git is
// a directory) is held, not judged by that other repository's status.
func TestWorktreeHoldsADirectoryThatIsItsOwnCheckout(t *testing.T) {
	if testing.Short() {
		t.Skip("builds real git repositories and worktrees; runs in task test")
	}
	repo, worktree := gitRepoWithWorktree(t)
	runGit(t, repo, "update-ref", "refs/remotes/origin/main", "main")
	if err := os.Remove(filepath.Join(worktree, ".git")); err != nil {
		t.Fatal(err)
	}
	runGit(t, worktree, "init", "-q", "-b", "main")
	ageDir(t, worktree)
	fakeLsof(t, unrelatedOpenFile, "exit 0")
	report := reapOnce(t, Runner{}, repo, true)
	if got := heldReason(report, worktree); got != HoldNotLinked {
		t.Fatalf("held reason = %q, want %q (held %+v)", got, HoldNotLinked, report.Held)
	}
	if _, err := os.Stat(filepath.Join(worktree, ".gitignore")); err != nil {
		t.Fatal("a directory holding its own checkout was removed")
	}
}

// Reliant reclaims the worktrees it locked as its own, wherever they live; an
// unlocked worktree under its root (a plain `git worktree add` there) is
// judged like any other, or nothing would ever reclaim it.
func TestWorktreeHeldAsReliantsOnlyByReliantsLock(t *testing.T) {
	if testing.Short() {
		t.Skip("builds real git repositories and worktrees; runs in task test")
	}
	for _, locked := range []bool{false, true} {
		repo, worktree := gitRepoWithWorktree(t)
		runGit(t, repo, "update-ref", "refs/remotes/origin/main", "main")
		config := t.TempDir()
		t.Setenv("RELIANT_USER_CONFIG_DIR", config)
		under := filepath.Join(config, "worktrees", "agent-made")
		if err := os.MkdirAll(filepath.Dir(under), 0o700); err != nil {
			t.Fatal(err)
		}
		runGit(t, repo, "worktree", "move", worktree, under)
		if locked {
			runGit(t, repo, "worktree", "lock", "--reason", "reliant: worktree 0b6c", under)
		}
		ageDir(t, under)
		fakeLsof(t, unrelatedOpenFile, "exit 0")
		report, err := (Runner{}).reapWorktrees(context.Background(), reapOptions{repos: []string{repo}, base: "main", idle: 24 * time.Hour, apply: true})
		if err != nil {
			t.Fatal(err)
		}
		_, statErr := os.Stat(under)
		if gone := os.IsNotExist(statErr); gone == locked {
			t.Fatalf("locked=%v: removed=%v (held %+v)", locked, gone, report.Held)
		}
		if locked {
			if got := heldReason(report, under); got != HoldManaged {
				t.Fatalf("held reason = %q, want %q", got, HoldManaged)
			}
		}
	}
}

func TestParseWorktreesKeepsTheLockReason(t *testing.T) {
	got := parseWorktrees("worktree /m\nHEAD a\n\nworktree /w\nHEAD b\nlocked reliant: worktree 1\n\nworktree /x\nHEAD c\nlocked\n")
	if len(got) != 3 || got[1].lockReason != "reliant: worktree 1" || !got[2].locked || got[2].lockReason != "" {
		t.Fatalf("parsed %+v", got)
	}
}

// Activity anywhere in the tree holds the worktree: a build writing an
// ignored, rebuildable dir changes neither the git admin files nor status.
func TestWorktreeHeldWhenAnythingInsideWasModifiedRecently(t *testing.T) {
	if testing.Short() {
		t.Skip("builds real git repositories and worktrees; runs in task test")
	}
	repo, worktree := gitRepoWithWorktree(t)
	runGit(t, repo, "update-ref", "refs/remotes/origin/main", "main")
	if err := os.WriteFile(filepath.Join(repo, ".git", "info", "exclude"), []byte("node_modules/\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	built := filepath.Join(worktree, "web", "node_modules", "pkg", "index.js")
	if err := os.MkdirAll(filepath.Dir(built), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(built, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	ageDir(t, worktree)
	now := time.Now()
	if err := os.Chtimes(built, now, now); err != nil { // the build just wrote it
		t.Fatal(err)
	}
	fakeLsof(t, unrelatedOpenFile, "exit 0")
	report := reapOnce(t, Runner{}, repo, true)
	if got := heldReason(report, worktree); got != HoldActive {
		t.Fatalf("held reason = %q, want %q (held %+v)", got, HoldActive, report.Held)
	}
	if _, err := os.Stat(built); err != nil {
		t.Fatal("a worktree written to moments ago was removed")
	}
}

// No lsof at all is the cloud-workspace case (the 2026-10-09 machine had
// none): the in-use check is unavailable, so nothing is removed.
func TestWorktreesRetainWhenLsofIsMissing(t *testing.T) {
	if testing.Short() {
		t.Skip("builds real git repositories and worktrees; runs in task test")
	}
	gitPath, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git not on PATH")
	}
	repo, worktree := gitRepoWithWorktree(t)
	runGit(t, repo, "update-ref", "refs/remotes/origin/main", "main")
	ageDir(t, worktree)
	bin := t.TempDir()
	if err := os.Symlink(gitPath, filepath.Join(bin, "git")); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)                      // git, and no lsof
	t.Setenv("FORGE_OPENFILES_SOURCE", "lsof") // Linux reads /proc, which needs none; force the lsof source
	var out strings.Builder
	err = (Runner{Out: &out}).Worktrees(context.Background(), repo, "main", 24*time.Hour, true)
	if _, statErr := os.Stat(worktree); statErr != nil {
		t.Fatalf("removed a worktree with no lsof to check it (%v)\n%s", statErr, out.String())
	}
	if err == nil || !strings.Contains(err.Error(), "lsof") {
		t.Fatalf("the missing in-use check must be reported: %v\n%s", err, out.String())
	}
}

// Inside a pass, git's unlinks count against the delete budget: a worktree
// that would overrun what is left waits for a later pass.
func TestWorktreeRemovalIsChargedToThePassBudget(t *testing.T) {
	if testing.Short() {
		t.Skip("builds real git repositories and worktrees; runs in task test")
	}
	repo, worktree := gitRepoWithWorktree(t)
	runGit(t, repo, "update-ref", "refs/remotes/origin/main", "main")
	ageDir(t, worktree)
	fakeLsof(t, unrelatedOpenFile, "exit 0")
	r := Runner{Policy: Policy{MaxDeletesPerPass: 1}}
	r = r.beginPass(context.Background())
	report := reapOnce(t, r, repo, true)
	if _, err := os.Stat(worktree); err != nil {
		t.Fatal("a worktree larger than the pass's remaining delete budget was removed")
	}
	if got := heldReason(report, worktree); got != HoldDeferred {
		t.Fatalf("held reason = %q, want %q (held %+v)", got, HoldDeferred, report.Held)
	}
}

func TestRebuildablePathNeverMatchesData(t *testing.T) {
	allow := append(DefaultWorktreeRebuildable(), "data", ".env")
	for _, p := range []string{"data/", ".env", ".env.local", ".forge/hostinfra/pg/", "x/secrets/", "a.db"} {
		if rebuildablePath(p, allow) {
			t.Errorf("%s must never be rebuildable", p)
		}
	}
	for _, p := range []string{"node_modules/", "web/node_modules/", "frontends/a/tsconfig.tsbuildinfo", ".forge/logs/"} {
		if !rebuildablePath(p, DefaultWorktreeRebuildable()) {
			t.Errorf("%s should be rebuildable", p)
		}
	}
}

func TestConvergeRecordsRepos(t *testing.T) {
	repo := t.TempDir()
	if _, changed := upsertFacts(Policy{}, Facts{Repos: []string{repo}}); changed {
		t.Fatal("a repo under the temp dir must not be recorded")
	}
	p, changed := upsertFacts(Policy{}, Facts{Repos: []string{"/Users/x/src/reliant", "/Users/x/src/reliant"}})
	if !changed || len(p.Repos) != 1 {
		t.Fatalf("repos = %v", p.Repos)
	}
}

// The scan must not reset the idle clock it measures: a stale stat cache makes
// plain `git status` rewrite the index.
func TestWorktreeScanDoesNotTouchIndex(t *testing.T) {
	if testing.Short() {
		t.Skip("builds real git repositories and worktrees; runs in task test")
	}
	repo, worktree := gitRepoWithWorktree(t)
	readme := filepath.Join(worktree, ".gitignore")
	content, err := os.ReadFile(readme)
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(1100 * time.Millisecond)
	if err := os.WriteFile(readme, content, 0o600); err != nil { // same content, new mtime
		t.Fatal(err)
	}
	ageDir(t, worktree)
	index := filepath.Join(worktreeAdminDir(worktree), "index")
	before, err := os.Stat(index)
	if err != nil {
		t.Fatal(err)
	}
	fakeLsof(t, unrelatedOpenFile, "exit 0")
	reapOnce(t, Runner{}, repo, false)
	after, err := os.Stat(index)
	if err != nil {
		t.Fatal(err)
	}
	if !after.ModTime().Equal(before.ModTime()) {
		t.Fatalf("scan rewrote the worktree index: %v -> %v", before.ModTime(), after.ModTime())
	}
}

func TestWorktreeHoldsHiddenChanges(t *testing.T) {
	if testing.Short() {
		t.Skip("builds real git repositories and worktrees; runs in task test")
	}
	for _, flag := range []string{"--skip-worktree", "--assume-unchanged"} {
		t.Run(flag, func(t *testing.T) {
			repo, worktree := gitRepoWithWorktree(t)
			runGit(t, worktree, "update-index", flag, ".gitignore")
			if err := os.WriteFile(filepath.Join(worktree, ".gitignore"), []byte("data/\n.env\nmy secret edit\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			ageDir(t, worktree)
			fakeLsof(t, unrelatedOpenFile, "exit 0")
			report := reapOnce(t, Runner{}, repo, true)
			if got := heldReason(report, worktree); got != HoldHidden {
				t.Fatalf("held reason = %q, want %q (held %+v)", got, HoldHidden, report.Held)
			}
			if _, err := os.Stat(worktree); err != nil {
				t.Fatal("worktree with hidden edits was removed")
			}
		})
	}
}

func TestWorktreeHoldsNestedRepositories(t *testing.T) {
	if testing.Short() {
		t.Skip("builds real git repositories and worktrees; runs in task test")
	}
	for _, tc := range []struct{ name, dir string }{
		{"inside allowlisted node_modules", "node_modules/pkg/.git"},
		{"inside allowlisted bin, deep", "bin/a/b/c/.git"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo, worktree := gitRepoWithWorktree(t)
			if err := os.WriteFile(filepath.Join(repo, ".git", "info", "exclude"), []byte("node_modules/\nbin/\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.MkdirAll(filepath.Join(worktree, tc.dir), 0o700); err != nil {
				t.Fatal(err)
			}
			ageDir(t, worktree)
			fakeLsof(t, unrelatedOpenFile, "exit 0")
			report := reapOnce(t, Runner{}, repo, true)
			if got := heldReason(report, worktree); got != HoldNested {
				t.Fatalf("held reason = %q, want %q (held %+v)", got, HoldNested, report.Held)
			}
			if _, err := os.Stat(worktree); err != nil {
				t.Fatal("worktree holding a nested repository was removed")
			}
		})
	}
}

// A process that starts using the worktree AFTER the batch snapshot but before
// removal must stop the removal. The fake lsof reports nothing on its first
// run and the worktree on every later run.
func TestWorktreeRecheckCatchesUseAfterBatchClassification(t *testing.T) {
	if testing.Short() {
		t.Skip("builds real git repositories and worktrees; runs in task test")
	}
	repo, worktree := gitRepoWithWorktree(t)
	ageDir(t, worktree)
	dir := t.TempDir()
	counter := filepath.Join(dir, "count")
	// After the batch snapshot a process "starts using" the worktree, which by
	// then may sit at its quarantine path, so report every sibling of either name.
	parent := filepath.Dir(resolved(t, worktree))
	script := "#!/bin/sh\nif [ -e '" + counter + "' ]; then printf 'p4242\\n'; for d in '" + parent + "'/feature '" + parent + "'/.forge-reclaim-*; do [ -d \"$d\" ] && printf 'fcwd\\nn%s\\n' \"$d\"; done; else : > '" + counter + "'; printf 'p1\\nf3\\nn/nonexistent/x\\n'; fi\n"
	if err := os.WriteFile(filepath.Join(dir, "lsof"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("FORGE_OPENFILES_SOURCE", "lsof")
	report := reapOnce(t, Runner{}, repo, true)
	if _, err := os.Stat(worktree); err != nil {
		t.Fatalf("worktree removed although a process started using it after the batch snapshot (held %+v)", report.Held)
	}
	if got := heldReason(report, worktree); got != HoldInUse {
		t.Fatalf("held reason = %q, want %q", got, HoldInUse)
	}
	if len(report.Removed) != 0 {
		t.Fatalf("removed %v", report.Removed)
	}
}

// Commits made on a detached HEAD, or only in a worktree's reflog, are
// unreachable once the worktree is removed.
func TestWorktreeHoldsUnreachableCommits(t *testing.T) {
	t.Run("detached HEAD commit", func(t *testing.T) {
		repo, worktree := gitRepoWithWorktree(t)
		runGit(t, worktree, "checkout", "-q", "--detach")
		if err := os.WriteFile(filepath.Join(worktree, "w.txt"), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		runGit(t, worktree, "add", "w.txt")
		runGit(t, worktree, "commit", "-q", "-m", "detached work")
		runGit(t, repo, "update-ref", "refs/remotes/origin/main", "main")
		ageDir(t, worktree)
		fakeLsof(t, unrelatedOpenFile, "exit 0")
		report := reapOnce(t, Runner{}, repo, true)
		if got := heldReason(report, worktree); got != HoldOrphans {
			t.Fatalf("held reason = %q, want %q (held %+v)", got, HoldOrphans, report.Held)
		}
		if _, err := os.Stat(worktree); err != nil {
			t.Fatal("worktree with orphaned commits was removed")
		}
	})
	t.Run("commit only in the reflog", func(t *testing.T) {
		repo, worktree := gitRepoWithWorktree(t)
		if err := os.WriteFile(filepath.Join(worktree, "w.txt"), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		runGit(t, worktree, "add", "w.txt")
		runGit(t, worktree, "commit", "-q", "-m", "work")
		runGit(t, worktree, "reset", "-q", "--hard", "HEAD~1") // the commit survives only in the reflog
		runGit(t, repo, "update-ref", "refs/remotes/origin/main", "main")
		ageDir(t, worktree)
		fakeLsof(t, unrelatedOpenFile, "exit 0")
		report := reapOnce(t, Runner{}, repo, true)
		if got := heldReason(report, worktree); got != HoldOrphans {
			t.Fatalf("held reason = %q, want %q (held %+v)", got, HoldOrphans, report.Held)
		}
	})
	t.Run("per-worktree ref", func(t *testing.T) {
		repo, worktree := gitRepoWithWorktree(t)
		if err := os.WriteFile(filepath.Join(worktree, "w.txt"), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		runGit(t, worktree, "add", "w.txt")
		runGit(t, worktree, "commit", "-q", "-m", "work")
		runGit(t, worktree, "update-ref", "refs/bisect/bad", "HEAD")
		runGit(t, worktree, "reset", "-q", "--hard", "HEAD~1")
		runGit(t, repo, "update-ref", "refs/remotes/origin/main", "main")
		ageDir(t, worktree)
		fakeLsof(t, unrelatedOpenFile, "exit 0")
		report := reapOnce(t, Runner{}, repo, false)
		if got := heldReason(report, worktree); got != HoldOrphans {
			t.Fatalf("held reason = %q, want %q (held %+v)", got, HoldOrphans, report.Held)
		}
	})
}

// A file written into the worktree after the batch classification, while the
// worktree is quarantined, must keep the worktree.
func TestWorktreeQuarantineCatchesWriteDuringWindow(t *testing.T) {
	repo, worktree := gitRepoWithWorktree(t)
	runGit(t, repo, "update-ref", "refs/remotes/origin/main", "main")
	ageDir(t, worktree)
	fakeLsof(t, unrelatedOpenFile, "exit 0")
	r := Runner{}
	r.afterQuarantine = func(original, quarantined string) {
		if _, err := os.Stat(original); err == nil {
			t.Errorf("the original path still exists during quarantine")
		}
		// The late writer: path-based writers of the original path get ENOENT,
		// so the only way to land is writing into the quarantined dir.
		if err := os.WriteFile(filepath.Join(quarantined, ".env"), []byte("secret"), 0o600); err != nil {
			t.Error(err)
		}
	}
	report := reapOnce(t, r, repo, true)
	got, err := os.ReadFile(filepath.Join(worktree, ".env"))
	if err != nil || string(got) != "secret" {
		t.Fatalf("late-written .env was lost or the worktree not restored (err %v, held %+v)", err, report.Held)
	}
	if h := heldReason(report, worktree); h != HoldData {
		t.Fatalf("held reason = %q, want %q", h, HoldData)
	}
	if len(report.Removed) != 0 {
		t.Fatalf("removed %v", report.Removed)
	}
}

func TestWorktreeQuarantineLeftoverIsReportedAndNeverDeleted(t *testing.T) {
	repo, worktree := gitRepoWithWorktree(t)
	runGit(t, repo, "update-ref", "refs/remotes/origin/main", "main")
	ageDir(t, worktree)
	fakeLsof(t, unrelatedOpenFile, "exit 0")
	r := Runner{}
	r.afterQuarantine = func(original, quarantined string) {
		_ = os.WriteFile(filepath.Join(quarantined, ".env"), []byte("secret"), 0o600)
		// Something now occupies the original path, so the move back fails.
		_ = os.MkdirAll(original, 0o700)
		_ = os.WriteFile(filepath.Join(original, "squatter"), nil, 0o600)
	}
	report := reapOnce(t, r, repo, true)
	var held *WorktreeHold
	for i := range report.Held {
		if report.Held[i].Reason == HoldQuarantined {
			held = &report.Held[i]
		}
	}
	if held == nil {
		t.Fatalf("expected a quarantined hold, got %+v", report.Held)
	}
	if !strings.Contains(held.Detail, quarantinePrefix) {
		t.Fatalf("detail does not name the quarantine path: %q", held.Detail)
	}
	entries, _ := filepath.Glob(filepath.Join(filepath.Dir(worktree), quarantinePrefix+"*", ".env"))
	if len(entries) != 1 {
		t.Fatal("the quarantined worktree's data was deleted")
	}
	// A later pass lists the leftover rather than ignoring it.
	report = reapOnce(t, Runner{}, repo, false)
	found := false
	for _, h := range report.Held {
		if h.Reason == HoldQuarantined {
			found = true
		}
	}
	if !found {
		t.Fatalf("a later pass does not report the quarantine leftover: %+v", report.Held)
	}
}

func TestRebuildableDefaultsIncludeLanguageCaches(t *testing.T) {
	for _, p := range []string{"target/", ".venv/", "venv/", ".pytest_cache/", ".mypy_cache/", ".ruff_cache/", ".gradle/", "svc/target/"} {
		if !rebuildablePath(p, DefaultWorktreeRebuildable()) {
			t.Errorf("%s should be rebuildable by default", p)
		}
	}
}

func excludeInRepo(t *testing.T, repo, patterns string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(repo, ".git", "info", "exclude"), []byte(patterns), 0o600); err != nil {
		t.Fatal(err)
	}
}

func writeIn(t *testing.T, root, rel string) {
	t.Helper()
	p := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestRebuildablePathRegenerableEntries(t *testing.T) {
	for p, want := range map[string]bool{
		".forge/tmp/":                  true,
		".forge/tmp/render-1/main.k":   true,
		".forge/generate-cache.json":   true,
		".forge/state/":                true,
		".forge/state/build-dev.json":  true,
		".forge/.forge-tmp-state-1":    true,
		".tools/":                      true,
		".tools/stamps/buf":            true,
		".tools/pre-commit/repoabc/":   true,
		"tmp/":                         true,
		"tmp/ci/summary.txt":           true,
		"web/tmp/x":                    false, // tmp is anchored at the worktree root
		"sub/.tools/state.json":        false, // so is .tools
		"tmp/data/x":                   false, // the never-list wins inside tmp
		"tmp/.env":                     false,
		"tmp/a.db":                     false,
		".tools/.env":                  false,
		".tools/secrets/k":             false,
		".forge/hostinfra/pg/data":     false,
		".forge/generate-cache.json.x": false,
		".forge/tmpx/":                 false,
		".forge/statex/":               false,
		"tmpfile":                      false,
	} {
		if got := rebuildablePath(p, DefaultWorktreeRebuildable()); got != want {
			t.Errorf("rebuildablePath(%q) = %v, want %v", p, got, want)
		}
	}
}

func TestWorktreeRegenerableToolOutputDoesNotHold(t *testing.T) {
	if testing.Short() {
		t.Skip("builds real git repositories and worktrees; runs in task test")
	}
	for name, rel := range map[string]string{
		"forge-tmp":            ".forge/tmp/render-123/main.k",
		"forge-generate-cache": ".forge/generate-cache.json",
		"tools-bin":            ".tools/bin/golangci-lint",
		"tools-stamps":         ".tools/stamps/buf-v1.50.0",
		"tmp":                  "tmp/notes.md",
		"tmp-ci":               "tmp/ci/summary.txt",
	} {
		t.Run(name, func(t *testing.T) {
			repo, worktree := gitRepoWithWorktree(t)
			excludeInRepo(t, repo, ".forge/*\n.tools/\ntmp/\n")
			writeIn(t, worktree, rel)
			ageDir(t, worktree)
			fakeLsof(t, unrelatedOpenFile, "exit 0")
			report := reapOnce(t, Runner{}, repo, true)
			if _, err := os.Stat(worktree); err == nil || len(report.Removed) != 1 {
				t.Fatalf("%s: want removed, got removed=%v held=%+v", rel, report.Removed, report.Held)
			}
		})
	}
}

func TestWorktreeRegenerableEntriesDoNotCoverData(t *testing.T) {
	if testing.Short() {
		t.Skip("builds real git repositories and worktrees; runs in task test")
	}
	for name, rel := range map[string]string{
		"data-in-tmp":     "tmp/data/rows.csv",
		"env-in-tmp":      "tmp/.env",
		"db-in-tools":     ".tools/state.db",
		"tmp-not-at-root": "weird/tmp/thing.bin",
		"nested-tools":    "weird/.tools/thing.bin",
	} {
		t.Run(name, func(t *testing.T) {
			repo, worktree := gitRepoWithWorktree(t)
			// A tracked file in tmp/ and .tools/ keeps git from collapsing
			// each into one "!! dir/" entry that would hide what is inside.
			excludeInRepo(t, repo, ".tools/*\n!.tools/.keep\ntmp/*\n!tmp/.keep\nweird/\n")
			writeIn(t, worktree, "tmp/.keep")
			writeIn(t, worktree, ".tools/.keep")
			runGit(t, worktree, "add", "tmp/.keep", ".tools/.keep")
			runGit(t, worktree, "commit", "-q", "-m", "keep dirs")
			writeIn(t, worktree, rel)
			ageDir(t, worktree)
			fakeLsof(t, unrelatedOpenFile, "exit 0")
			report := reapOnce(t, Runner{}, repo, true)
			if got := heldReason(report, worktree); got != HoldData {
				t.Fatalf("%s: held reason = %q, want %q (held %+v)", rel, got, HoldData, report.Held)
			}
			if _, err := os.Stat(filepath.Join(worktree, filepath.FromSlash(rel))); err != nil {
				t.Fatalf("%s was deleted: %v", rel, err)
			}
		})
	}
}

// preCommitClone makes a hook clone (a full repository with history) at rel.
func preCommitClone(t *testing.T, checkout, rel string) string {
	t.Helper()
	clone := filepath.Join(checkout, filepath.FromSlash(rel))
	writeIn(t, clone, ".pre-commit-hooks.yaml")
	runGit(t, clone, "init", "-q", "-b", "main")
	runGit(t, clone, "add", ".")
	runGit(t, clone, "commit", "-q", "-m", "hooks")
	return clone
}

func TestWorktreePreCommitClonesAreRegenerable(t *testing.T) {
	if testing.Short() {
		t.Skip("builds real git repositories and worktrees; runs in task test")
	}
	repo, worktree := gitRepoWithWorktree(t)
	excludeInRepo(t, repo, ".tools/\n")
	preCommitClone(t, worktree, ".tools/pre-commit/repo1a2b3c")
	preCommitClone(t, worktree, ".tools/pre-commit/repo4d5e6f")
	writeIn(t, worktree, ".tools/pre-commit/db.db")
	ageDir(t, worktree)
	fakeLsof(t, unrelatedOpenFile, "exit 0")
	report := reapOnce(t, Runner{}, repo, true)
	if _, err := os.Stat(worktree); err == nil || len(report.Removed) != 1 {
		t.Fatalf("want removed, got removed=%v held=%+v", report.Removed, report.Held)
	}
}

func TestWorktreeOtherNestedRepositoriesStillHold(t *testing.T) {
	if testing.Short() {
		t.Skip("builds real git repositories and worktrees; runs in task test")
	}
	for name, tc := range map[string]struct{ exclude, rel, want string }{
		"too-deep":      {".tools/\n", ".tools/pre-commit/repo1/sub", HoldNested},
		"elsewhere":     {".tools/\n", ".tools/other/repo1", HoldNested},
		"in-tmp":        {"tmp/\n", "tmp/pre-commit/repo1", HoldNested},
		"pre-commit-at": {".tools/\n", ".tools/pre-commit", HoldNested},
		"lookalike":     {".tools/\n", ".tools/pre-commit-x/repo1", HoldNested},
		// /.tools is root-anchored, so sub/.tools is unrecognized data first.
		"not-anchored": {"sub/\n", "sub/.tools/pre-commit/repo1", HoldData},
	} {
		t.Run(name, func(t *testing.T) {
			repo, worktree := gitRepoWithWorktree(t)
			excludeInRepo(t, repo, tc.exclude)
			clone := preCommitClone(t, worktree, tc.rel)
			ageDir(t, worktree)
			fakeLsof(t, unrelatedOpenFile, "exit 0")
			report := reapOnce(t, Runner{}, repo, true)
			if got := heldReason(report, worktree); got != tc.want {
				t.Fatalf("held reason = %q, want %q", got, tc.want)
			}
			if _, err := os.Stat(clone); err != nil {
				t.Fatalf("clone deleted: %v", err)
			}
		})
	}
}

func TestToolCloneNeedsAnIgnoringWorktree(t *testing.T) {
	if testing.Short() {
		t.Skip("builds real git repositories and worktrees; runs in task test")
	}
	repo, worktree := gitRepoWithWorktree(t)
	clone := preCommitClone(t, worktree, ".tools/pre-commit/repoX")
	ctx := context.Background()
	gitPath := filepath.FromSlash(".tools/pre-commit/repoX/.git")
	if (Runner{}).toolClone(ctx, worktree, gitPath) {
		t.Fatal("a clone git does not ignore is the user's, not a tool clone")
	}
	excludeInRepo(t, repo, ".tools/\n")
	if !(Runner{}).toolClone(ctx, worktree, gitPath) {
		t.Fatal("an ignored pre-commit clone is a tool clone")
	}
	if (Runner{}).toolClone(ctx, worktree, ".git") {
		t.Fatal("the worktree's own .git is never a tool clone")
	}
	// A .git FILE (submodule/worktree pointer) is never a regenerable clone.
	if err := os.Remove(filepath.Join(clone, ".git")); err != nil && os.RemoveAll(filepath.Join(clone, ".git")) != nil {
		t.Fatal("cannot reset clone")
	}
	writeIn(t, clone, ".git")
	if (Runner{}).toolClone(ctx, worktree, gitPath) {
		t.Fatal("a .git file is not a tool clone")
	}
}
