package cli

// `forge project checkouts` against a real repository with two worktrees,
// one dirty.
//
// Driven against real git rather than a parser fixture, because the
// interesting failures are in git's own output: a detached HEAD has no
// `branch` line, a worktree's path is the RESOLVED one (macOS's /var →
// /private/var) while the project dir may be the symlinked one, and
// `rev-list --left-right` reports behind before ahead. A hand-written
// porcelain fixture would encode whatever the author believed about those
// rather than what git does.

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/cli/cmdutil"
)

// checkoutsFixture is a repository with a remote-tracking main and two
// worktrees.
type checkoutsFixture struct {
	primary string
	clean   string
	dirty   string
}

// newCheckoutsFixture builds: an origin repo, a clone (the primary
// checkout), and two worktrees off the clone — one clean on a branch, one
// dirty on another.
//
// A real `origin` is created rather than stubbed because "main" means
// origin/<main>, and a fixture with no remote would exercise only the
// fallback path — which is the one case where ahead/behind is absent.
func newCheckoutsFixture(t *testing.T) checkoutsFixture {
	t.Helper()
	root := t.TempDir()
	origin := filepath.Join(root, "origin.git")
	seed := filepath.Join(root, "seed")

	// A seed repo with one commit on main, pushed to a bare origin.
	mustGit(t, root, "init", "--bare", "-b", "main", origin)
	mustGit(t, root, "init", "-b", "main", seed)
	writeFixtureFile(t, seed, "README.md", "one\n")
	mustGit(t, seed, "config", "user.email", "test@example.com")
	mustGit(t, seed, "config", "user.name", "test")
	mustGit(t, seed, "add", "-A")
	mustGit(t, seed, "commit", "-q", "-m", "one")
	mustGit(t, seed, "remote", "add", "origin", origin)
	mustGit(t, seed, "push", "-q", "origin", "main")

	primary := filepath.Join(root, "primary")
	mustGit(t, root, "clone", "--quiet", origin, primary)
	mustGit(t, primary, "config", "user.email", "test@example.com")
	mustGit(t, primary, "config", "user.name", "test")
	// origin/HEAD, so remoteMainRef resolves rather than falling back.
	mustGit(t, primary, "remote", "set-head", "origin", "main")

	// A clean worktree on its own branch, one commit AHEAD of main.
	clean := filepath.Join(root, "wt-clean")
	mustGit(t, primary, "worktree", "add", "-q", "-b", "feat-clean", clean)
	writeFixtureFile(t, clean, "clean.txt", "x\n")
	mustGit(t, clean, "add", "-A")
	mustGit(t, clean, "commit", "-q", "-m", "ahead by one")

	// A DIRTY worktree: committed, then an uncommitted edit plus an
	// untracked file. Both must read as dirty, because both change the
	// tree hash a Preview render would record.
	dirty := filepath.Join(root, "wt-dirty")
	mustGit(t, primary, "worktree", "add", "-q", "-b", "feat-dirty", dirty)
	writeFixtureFile(t, dirty, "README.md", "edited, not committed\n")
	writeFixtureFile(t, dirty, "untracked.txt", "also uncommitted\n")

	return checkoutsFixture{primary: primary, clean: clean, dirty: dirty}
}

func mustGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v in %s: %v: %s", args, dir, err, out)
	}
}

func writeFixtureFile(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
}

// byLabel indexes the WORKTREE entries for assertions.
//
// Worktrees only, because a label is not unique across kinds: the doc's §8.1
// contract gives the remote entry the label "main", and the primary checkout
// is usually on a branch called main too. `kind` is the discriminator — which
// is why it is in the contract — so the tests index the two separately rather
// than pretending the label is a key.
func byLabel(doc checkoutsDoc) map[string]checkoutDoc {
	out := map[string]checkoutDoc{}
	for _, c := range doc.Checkouts {
		if c.Kind == checkoutWorktree {
			out[c.Label] = c
		}
	}
	return out
}

// remoteEntry is the document's remote row, if it has one.
func remoteEntry(doc checkoutsDoc) (checkoutDoc, bool) {
	for _, c := range doc.Checkouts {
		if c.Kind == checkoutRemote {
			return c, true
		}
	}
	return checkoutDoc{}, false
}

// The §8.1 shape: the remote main plus every worktree, each with the fields
// the picker needs.
func TestProjectCheckouts_ListsMainAndEveryWorktree(t *testing.T) {
	if testing.Short() {
		t.Skip("builds real git repositories and worktrees; runs in task test")
	}
	fx := newCheckoutsFixture(t)

	doc, err := listCheckouts(context.Background(), fx.primary, false)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if doc.MainRef != "origin/main" {
		t.Errorf("main_ref = %q, want origin/main", doc.MainRef)
	}
	got := byLabel(doc)

	// "main" is the REMOTE ref, and it carries no path — which is the
	// field a consumer branches on, because there is nothing on disk to
	// render until someone checks it out.
	main, ok := remoteEntry(doc)
	if !ok {
		t.Fatalf("no remote entry in %+v", doc.Checkouts)
	}
	if main.Label != "main" {
		t.Errorf("remote label = %q, want main (the §8.1 contract)", main.Label)
	}
	if main.Ref != "origin/main" {
		t.Errorf("main ref = %q, want origin/main", main.Ref)
	}
	if main.Path != "" {
		t.Errorf("the remote entry must carry no path, got %q", main.Path)
	}
	if main.Dirty {
		t.Error("a remote ref is never dirty")
	}

	// Three worktrees: the primary (on branch main) and the two added
	// ones. The primary shares the remote's LABEL, which is why these are
	// looked up among worktrees only.
	for _, label := range []string{"main", "feat-clean", "feat-dirty"} {
		if _, ok := got[label]; !ok {
			t.Errorf("no %q worktree in %+v", label, doc.Checkouts)
		}
	}
	for _, label := range []string{"feat-clean", "feat-dirty"} {
		entry := got[label]
		if entry.Kind != checkoutWorktree {
			t.Errorf("%s kind = %q, want %q", label, entry.Kind, checkoutWorktree)
		}
		if entry.Path == "" {
			t.Errorf("%s must carry a path: the daemon only accepts paths this command returned", label)
		}
		if entry.Branch != label {
			t.Errorf("%s branch = %q, want %q", label, entry.Branch, label)
		}
		if entry.Head == "" {
			t.Errorf("%s must carry a head", label)
		}
	}
}

// The dirty flag covers BOTH an uncommitted edit and an untracked file,
// because both change the tree hash a Preview render would record — a
// checkout reported clean whose render is not reproducible is the mismatch
// the flag exists to prevent.
func TestProjectCheckouts_DirtyCoversEditsAndUntrackedFiles(t *testing.T) {
	if testing.Short() {
		t.Skip("builds real git repositories and worktrees; runs in task test")
	}
	fx := newCheckoutsFixture(t)

	doc, err := listCheckouts(context.Background(), fx.primary, false)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	got := byLabel(doc)
	if !got["feat-dirty"].Dirty {
		t.Error("feat-dirty has an uncommitted edit and an untracked file; it must read dirty")
	}
	if got["feat-clean"].Dirty {
		t.Error("feat-clean has nothing uncommitted; it must read clean")
	}

	// An untracked file ALONE is enough.
	writeFixtureFile(t, fx.clean, "new-untracked.txt", "x\n")
	doc, err = listCheckouts(context.Background(), fx.primary, false)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if !byLabel(doc)["feat-clean"].Dirty {
		t.Error("an untracked file alone makes a checkout dirty: it changes the tree hash")
	}
}

// ahead/behind is counted against the REMOTE main, with behind and ahead the
// right way round — `rev-list --left-right --count` reports main's side
// first, and transposing them would tell a user their branch is behind when
// it is ahead.
func TestProjectCheckouts_CountsAheadAndBehindAgainstRemoteMain(t *testing.T) {
	if testing.Short() {
		t.Skip("builds real git repositories and worktrees; runs in task test")
	}
	fx := newCheckoutsFixture(t)

	doc, err := listCheckouts(context.Background(), fx.primary, false)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	clean := byLabel(doc)["feat-clean"]
	if clean.AheadOfMain == nil || clean.BehindMain == nil {
		t.Fatalf("feat-clean must report a position relative to main, got %+v", clean)
	}
	if *clean.AheadOfMain != 1 {
		t.Errorf("feat-clean is ahead by %d, want 1", *clean.AheadOfMain)
	}
	if *clean.BehindMain != 0 {
		t.Errorf("feat-clean is behind by %d, want 0", *clean.BehindMain)
	}

	// Move origin/main forward, then re-read: the same worktree is now
	// behind as well as ahead, which is the normal state of a branch.
	mustGit(t, fx.primary, "commit", "-q", "--allow-empty", "-m", "main moves")
	mustGit(t, fx.primary, "push", "-q", "origin", "main")
	doc, err = listCheckouts(context.Background(), fx.primary, false)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	clean = byLabel(doc)["feat-clean"]
	if clean.BehindMain == nil || *clean.BehindMain != 1 {
		t.Errorf("feat-clean is behind by %v, want 1 after main moved", clean.BehindMain)
	}
	if clean.AheadOfMain == nil || *clean.AheadOfMain != 1 {
		t.Errorf("feat-clean is ahead by %v, want 1", clean.AheadOfMain)
	}
}

// "In step with main" and "I could not tell" are different answers, so a
// repository with no origin OMITS the counts rather than reporting zero.
func TestProjectCheckouts_OmitsAheadBehindWhenMainIsUnknown(t *testing.T) {
	dir := t.TempDir()
	mustGit(t, dir, "init", "-b", "main", dir)
	mustGit(t, dir, "config", "user.email", "test@example.com")
	mustGit(t, dir, "config", "user.name", "test")
	writeFixtureFile(t, dir, "README.md", "x\n")
	mustGit(t, dir, "add", "-A")
	mustGit(t, dir, "commit", "-q", "-m", "one")

	doc, err := listCheckouts(context.Background(), dir, false)
	if err != nil {
		t.Fatalf("a repository with no origin must still list its worktrees: %v", err)
	}
	if len(doc.Checkouts) == 0 {
		t.Fatal("want at least the primary checkout")
	}
	for _, c := range doc.Checkouts {
		if c.AheadOfMain != nil || c.BehindMain != nil {
			t.Errorf("%s reported a position against a main that does not exist: +%v/-%v",
				c.Label, c.AheadOfMain, c.BehindMain)
		}
	}
	// And the remote entry is simply absent, rather than a row with an
	// empty head.
	if entry, ok := remoteEntry(doc); ok {
		t.Errorf("there is no origin/main, so there must be no remote entry, got %+v", entry)
	}

	// --json must still carry the counts as ABSENT keys, not nulls or
	// zeros, so a consumer can tell the difference.
	encoded, err := json.Marshal(doc)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	if strings.Contains(string(encoded), "ahead_of_main") {
		t.Errorf("an unknowable count must be omitted from JSON, got: %s", encoded)
	}
}

// A detached HEAD has no branch, and the label falls back to the directory
// basename — which a user recognises, where a sha does not. The sha is in
// Head for whoever needs it.
func TestProjectCheckouts_DetachedHeadIsLabelledByDirectory(t *testing.T) {
	if testing.Short() {
		t.Skip("builds real git repositories and worktrees; runs in task test")
	}
	fx := newCheckoutsFixture(t)
	head := strings.TrimSpace(gitOutput(t, fx.primary, "rev-parse", "HEAD"))
	detached := filepath.Join(filepath.Dir(fx.primary), "wt-detached")
	mustGit(t, fx.primary, "worktree", "add", "-q", "--detach", detached, head)

	doc, err := listCheckouts(context.Background(), fx.primary, false)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	entry, ok := byLabel(doc)["wt-detached"]
	if !ok {
		t.Fatalf("a detached worktree must be listed by its directory, got %+v", doc.Checkouts)
	}
	if entry.Branch != "" {
		t.Errorf("a detached checkout has no branch, got %q", entry.Branch)
	}
	if entry.Head == "" {
		t.Error("a detached checkout still has a head, which is how it is identified")
	}
}

// No tree hash by default: hashing one checkout costs about half a second,
// so hashing every worktree would make the picker unusable to answer a
// question nobody has asked yet.
func TestProjectCheckouts_NoTreeHashByDefault(t *testing.T) {
	if testing.Short() {
		t.Skip("builds real git repositories and worktrees; runs in task test")
	}
	fx := newCheckoutsFixture(t)

	doc, err := listCheckouts(context.Background(), fx.primary, false)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	for _, c := range doc.Checkouts {
		if c.Tree != "" {
			t.Errorf("%s carries a tree hash without --tree: %q", c.Label, c.Tree)
		}
	}
}

// --tree hashes exactly ONE checkout — the selected one — because that is
// the only one whose cache key is about to be needed.
func TestProjectCheckouts_TreeHashesOnlyTheSelectedCheckout(t *testing.T) {
	if testing.Short() {
		t.Skip("builds real git repositories and worktrees; runs in task test")
	}
	fx := newCheckoutsFixture(t)

	doc, err := listCheckouts(context.Background(), fx.clean, true)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	var hashed []string
	for _, c := range doc.Checkouts {
		if c.Tree != "" {
			hashed = append(hashed, c.Label)
		}
		if c.Tree != "" && !c.Selected {
			t.Errorf("%s was hashed but is not the selected checkout", c.Label)
		}
	}
	if len(hashed) != 1 {
		t.Fatalf("hashed %v, want exactly the selected checkout", hashed)
	}
	if hashed[0] != "feat-clean" {
		t.Errorf("hashed %q, want feat-clean (the checkout the command ran in)", hashed[0])
	}
}

// The selected checkout is matched through git's resolved path, not a string
// comparison — macOS's /var → /private/var symlink makes the naive
// comparison fail on exactly the machines this runs on.
func TestProjectCheckouts_MarksTheSelectedCheckoutAcrossSymlinks(t *testing.T) {
	if testing.Short() {
		t.Skip("builds real git repositories and worktrees; runs in task test")
	}
	fx := newCheckoutsFixture(t)

	for _, where := range []struct {
		dir, label string
	}{
		// The primary checkout is on branch main, so its label is
		// "main" — the same label the remote row carries. Only one of
		// them can be SELECTED, and it is never the remote one, which
		// has no path.
		{fx.primary, "main"},
		{fx.clean, "feat-clean"},
		{fx.dirty, "feat-dirty"},
	} {
		doc, err := listCheckouts(context.Background(), where.dir, false)
		if err != nil {
			t.Fatalf("list from %s: %v", where.dir, err)
		}
		var selected []string
		for _, c := range doc.Checkouts {
			if c.Selected {
				selected = append(selected, c.Label)
			}
		}
		// Exactly one, and it is the WORKTREE — never the remote entry,
		// which has no path and so cannot be "where you are".
		if len(selected) != 1 {
			t.Errorf("from %s: selected %v, want exactly one", where.dir, selected)
			continue
		}
		if selected[0] != where.label {
			t.Errorf("from %s: selected %q, want %q", where.dir, selected[0], where.label)
		}
	}
}

// A directory outside git refuses, naming why: Preview renders a checkout,
// and there is no checkout to render.
func TestProjectCheckouts_RefusesANonGitDirectory(t *testing.T) {
	_, err := listCheckouts(context.Background(), t.TempDir(), false)
	if err == nil {
		t.Fatal("expected a refusal outside a git checkout")
	}
	if !strings.Contains(err.Error(), "not a git checkout") {
		t.Errorf("error = %v, want it to say there is no repository", err)
	}
}

// The devstack key rides along so a local session row can be matched to the
// port block its stack actually holds (§7.4). It is the same key `forge env
// devstack key` prints, derived from the path with no git call.
func TestProjectCheckouts_CarriesTheDevstackKey(t *testing.T) {
	if testing.Short() {
		t.Skip("builds real git repositories and worktrees; runs in task test")
	}
	fx := newCheckoutsFixture(t)

	doc, err := listCheckouts(context.Background(), fx.primary, false)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	// At least one worktree must report a key. The primary checkout's key
	// is "" by design, so asserting on every entry would be asserting the
	// opposite of the contract.
	var keyed int
	for _, c := range doc.Checkouts {
		if c.Kind == checkoutWorktree && c.DevstackKey != "" {
			keyed++
		}
	}
	if keyed == 0 {
		t.Errorf("no worktree reported a devstack key, so a session could not be matched to its port block: %+v", doc.Checkouts)
	}
}

// The command's own --json output is the daemon's contract, so it is driven
// end to end rather than only through listCheckouts.
func TestProjectCheckouts_CommandJSONIsTheDocument(t *testing.T) {
	if testing.Short() {
		t.Skip("builds real git repositories and worktrees; runs in task test")
	}
	fx := newCheckoutsFixture(t)
	if err := cmdutil.SetProjectDir(fx.primary); err != nil {
		t.Fatalf("pin the project dir: %v", err)
	}
	t.Cleanup(func() { _ = cmdutil.SetProjectDir("") })

	var out bytes.Buffer
	cmd := newProjectCheckoutsCmd()
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetContext(context.Background())
	cmd.SetArgs([]string{"--json"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("checkouts --json: %v\n%s", err, out.String())
	}

	var doc checkoutsDoc
	if err := json.Unmarshal(out.Bytes(), &doc); err != nil {
		t.Fatalf("--json must emit ONLY the document: %v\n%s", err, out.String())
	}
	if doc.MainRef == "" || len(doc.Checkouts) == 0 {
		t.Errorf("document = %+v, want a main ref and some checkouts", doc)
	}
}

func gitOutput(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git %v: %v", args, err)
	}
	return string(out)
}
