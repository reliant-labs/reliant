// Copyright (c) 2025 Reliant Labs
package daemonruntime

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/reliant/internal/daemon"
	"github.com/reliant-labs/reliant/internal/toolexec"
)

// ensureFixture is a project with nested repos and one reliant worktree
// workspace over them, created exactly as the server creates one: a
// worktree.create per repo into <HOME>/.reliant/worktrees/<workspace>/<rel>.
type ensureFixture struct {
	home    string
	project string
	root    string // the workspace root, the chat's working directory
	branch  string
	rels    []string
}

func newEnsureFixture(t *testing.T, rels ...string) *ensureFixture {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	project := filepath.Join(t.TempDir(), "proj")
	f := &ensureFixture{home: home, project: project, branch: "feat/recover", rels: rels}
	for _, rel := range rels {
		newRepo(t, filepath.Join(project, rel))
	}
	const workspaceID = "proj/recover-1a2b3c4d"
	for _, rel := range rels {
		payload, _ := json.Marshal(worktreeCreateRequest{
			ProjectPath: filepath.Join(project, rel),
			WorkspaceID: workspaceID,
			SubPath:     rel,
			Name:        "recover",
			Branch:      f.branch,
			BaseBranch:  "main",
			WorktreeID:  "wt-1",
		})
		raw, err := handleWorktreeCreate(context.Background(), payload)
		if err != nil {
			t.Fatal(err)
		}
		var resp worktreeCreateResponse
		if err := json.Unmarshal(raw, &resp); err != nil || !resp.Success {
			t.Fatalf("worktree.create %q: %s (%v)", rel, raw, err)
		}
	}
	f.root = filepath.Join(home, ".reliant", "worktrees", filepath.FromSlash(workspaceID))
	return f
}

func (f *ensureFixture) request(audience string) toolexec.WorkspaceEnsureRequest {
	req := toolexec.WorkspaceEnsureRequest{
		Path:         f.root,
		Repair:       true,
		WorktreeID:   "wt-1",
		Branch:       f.branch,
		FallbackPath: f.project,
		Audience:     audience,
	}
	for _, rel := range f.rels {
		req.Repos = append(req.Repos, toolexec.WorkspaceEnsureRepo{RepoPath: filepath.Join(f.project, rel), Rel: rel})
	}
	return req
}

func ensureVia(t *testing.T, e *workspaceEnsurer, req toolexec.WorkspaceEnsureRequest) toolexec.WorkspaceEnsureResponse {
	t.Helper()
	return e.ensure(context.Background(), req)
}

// commit makes a commit on the workspace's branch in one checkout, so a
// rebuilt checkout can be told apart from a fresh branch off main.
func commitIn(t *testing.T, dir, file string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, file), []byte("work\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	rcGit(t, dir, "add", file)
	rcGit(t, dir, "commit", "-q", "-m", "work on the branch")
}

// The reported failure, end to end on the daemon: a chat's worktree directory
// disappears, and every command bound to it fails before running — forever,
// nothing ever repairs it. After an ensure the workspace is back on its branch
// with its commits, and the same command runs.
func TestEnsure_DeletedWorktreeIsRecreatedAndCommandsRunAgain(t *testing.T) {
	f := newEnsureFixture(t, "")
	commitIn(t, f.root, "committed.txt")
	if err := os.WriteFile(filepath.Join(f.root, "uncommitted.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(f.root); err != nil {
		t.Fatal(err)
	}

	for i := 0; i < 2; i++ {
		res := runExec(t, daemon.RunCommandRequest{Command: "git rev-parse --abbrev-ref HEAD", WorkingDir: f.root})
		if res.ExitCode == 0 || !strings.Contains(res.Stderr, "working directory does not exist") {
			t.Fatalf("before ensure, attempt %d: want the missing-directory failure, got %+v", i+1, res)
		}
	}

	e := newWorkspaceEnsurer()
	resp := ensureVia(t, e, f.request("thread-a"))
	if resp.Status != toolexec.WorkspaceRepaired || resp.Path != f.root {
		t.Fatalf("ensure = %+v, want repaired at %s", resp, f.root)
	}
	if resp.Notice == nil || resp.Notice.Kind != toolexec.NoticeRepaired || resp.Notice.Branch != f.branch {
		t.Fatalf("notice = %+v, want a repaired notice naming the branch", resp.Notice)
	}

	res := runExec(t, daemon.RunCommandRequest{Command: "git rev-parse --abbrev-ref HEAD && ls", WorkingDir: resp.Path})
	if res.ExitCode != 0 {
		t.Fatalf("after ensure the command still fails: %+v", res)
	}
	if !strings.Contains(res.Stdout, f.branch) || !strings.Contains(res.Stdout, "committed.txt") {
		t.Errorf("recreated workspace is not the branch with its commits:\n%s", res.Stdout)
	}
	if strings.Contains(res.Stdout, "uncommitted.txt") {
		t.Errorf("uncommitted work cannot survive a deleted directory; the workspace claims it did:\n%s", res.Stdout)
	}

	// Healthy now: the next ensure is a no-op and says nothing new.
	again := ensureVia(t, e, f.request("thread-a"))
	if again.Status != toolexec.WorkspacePresent || again.Notice != nil {
		t.Errorf("second ensure = %+v, want present with no notice", again)
	}
}

// A multi-repo workspace is one directory holding a checkout per nested repo;
// losing it means rebuilding every one, each from its own repository.
func TestEnsure_RecreatesEveryCheckoutOfAMultiRepoWorkspace(t *testing.T) {
	f := newEnsureFixture(t, "api", "web")
	commitIn(t, filepath.Join(f.root, "web"), "web-work.txt")
	if err := os.RemoveAll(f.root); err != nil {
		t.Fatal(err)
	}

	resp := ensureVia(t, newWorkspaceEnsurer(), f.request("thread-a"))
	if resp.Status != toolexec.WorkspaceRepaired {
		t.Fatalf("ensure = %+v, want repaired", resp)
	}
	for _, rel := range f.rels {
		if got := strings.TrimSpace(rcGit(t, filepath.Join(f.root, rel), "rev-parse", "--abbrev-ref", "HEAD")); got != f.branch {
			t.Errorf("%s is on %q, want %q", rel, got, f.branch)
		}
	}
	if _, err := os.Stat(filepath.Join(f.root, "web", "web-work.txt")); err != nil {
		t.Errorf("web's committed work is missing from the rebuilt checkout: %v", err)
	}
	if got := strings.Join(resp.Notice.Checkouts, ","); got != "api,web" {
		t.Errorf("notice names checkouts %q, want api,web", got)
	}
}

// One nested checkout deleted while its siblings survive: only that one is
// rebuilt, and nothing that was there is touched.
func TestEnsure_RebuildsOnlyTheMissingNestedCheckout(t *testing.T) {
	f := newEnsureFixture(t, "api", "web")
	dirty := filepath.Join(f.root, "api", "dirty.txt")
	if err := os.WriteFile(dirty, []byte("keep me"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(filepath.Join(f.root, "web")); err != nil {
		t.Fatal(err)
	}

	resp := ensureVia(t, newWorkspaceEnsurer(), f.request("thread-a"))
	if resp.Status != toolexec.WorkspaceRepaired || strings.Join(resp.Notice.Checkouts, ",") != "web" {
		t.Fatalf("ensure = %+v (notice %+v), want web alone repaired", resp, resp.Notice)
	}
	if b, err := os.ReadFile(dirty); err != nil || string(b) != "keep me" {
		t.Errorf("the surviving checkout's uncommitted work was disturbed: %q %v", b, err)
	}
}

// The branch is gone too, so there is nothing to recreate from. The call
// still runs — in the project's main checkout — and the notice says why.
func TestEnsure_FallsBackToTheMainCheckoutWhenTheBranchIsGone(t *testing.T) {
	f := newEnsureFixture(t, "")
	if err := os.RemoveAll(f.root); err != nil {
		t.Fatal(err)
	}
	rcGit(t, f.project, "worktree", "remove", "--force", "--force", f.root)
	rcGit(t, f.project, "branch", "-D", f.branch)

	resp := ensureVia(t, newWorkspaceEnsurer(), f.request("thread-a"))
	if resp.Status != toolexec.WorkspaceFallback || resp.Path != f.project {
		t.Fatalf("ensure = %+v, want fallback to %s", resp, f.project)
	}
	n := resp.Notice
	if n == nil || n.Kind != toolexec.NoticeFallback || n.RunningIn != f.project || n.WorkspacePath != f.root {
		t.Fatalf("notice = %+v, want a fallback notice from %s to %s", n, f.root, f.project)
	}
	if !strings.Contains(n.Reason, "no longer exists") {
		t.Errorf("reason %q does not say the branch is gone", n.Reason)
	}
	if res := runExec(t, daemon.RunCommandRequest{Command: "true", WorkingDir: resp.Path}); res.ExitCode != 0 {
		t.Errorf("command in the fallback directory failed: %+v", res)
	}
}

// What the prod incident looked like: the volume holding the project AND its
// worktrees is gone. There is nothing to recreate from and no main checkout;
// calls run in $HOME rather than not at all.
func TestEnsure_FallsBackToHomeWhenTheProjectIsGoneToo(t *testing.T) {
	f := newEnsureFixture(t, "api", "web")
	if err := os.RemoveAll(f.root); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(f.project); err != nil {
		t.Fatal(err)
	}

	resp := ensureVia(t, newWorkspaceEnsurer(), f.request("thread-a"))
	if resp.Status != toolexec.WorkspaceFallback || resp.Path != f.home {
		t.Fatalf("ensure = %+v, want fallback to $HOME %s", resp, f.home)
	}
	if !strings.Contains(resp.Notice.Reason, "missing on this machine") {
		t.Errorf("reason %q does not say the repository is missing", resp.Notice.Reason)
	}
}

// A directory where the checkout belongs, holding files that are not a
// checkout (agents in prod created one to "recover"): never overwritten.
func TestEnsure_NeverOverwritesADirectoryThatIsNotACheckout(t *testing.T) {
	f := newEnsureFixture(t, "")
	if err := os.RemoveAll(f.root); err != nil {
		t.Fatal(err)
	}
	keep := filepath.Join(f.root, ".recovery", "notes.md")
	if err := os.MkdirAll(filepath.Dir(keep), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keep, []byte("mine"), 0o644); err != nil {
		t.Fatal(err)
	}

	resp := ensureVia(t, newWorkspaceEnsurer(), f.request("thread-a"))
	if resp.Status != toolexec.WorkspaceFallback || resp.Path != f.project {
		t.Fatalf("ensure = %+v, want fallback to the main checkout", resp)
	}
	if b, err := os.ReadFile(keep); err != nil || string(b) != "mine" {
		t.Errorf("a file that was not reliant's was touched: %q %v", b, err)
	}
}

// Each thread hears about a change once. A branch of the chat is a new thread
// on the same worktree, so it is told about the repair it did not trigger; a
// caller with no audience (the user's repair button) consumes nothing.
func TestEnsure_NoticeReachesEachThreadExactlyOnce(t *testing.T) {
	f := newEnsureFixture(t, "")
	if err := os.RemoveAll(f.root); err != nil {
		t.Fatal(err)
	}
	e := newWorkspaceEnsurer()

	if resp := ensureVia(t, e, f.request("")); resp.Status != toolexec.WorkspaceRepaired || resp.Notice != nil {
		t.Fatalf("audience-less ensure = %+v, want repaired with no notice consumed", resp)
	}
	if resp := ensureVia(t, e, f.request("thread-original")); resp.Notice == nil || resp.Notice.Kind != toolexec.NoticeRepaired {
		t.Fatalf("original thread was not told about the repair: %+v", resp)
	}
	if resp := ensureVia(t, e, f.request("thread-original")); resp.Notice != nil {
		t.Errorf("original thread told twice: %+v", resp.Notice)
	}
	if resp := ensureVia(t, e, f.request("thread-branch")); resp.Notice == nil || resp.Notice.Kind != toolexec.NoticeRepaired {
		t.Errorf("branch thread was not told about the repair: %+v", resp)
	}
}

// A workspace that is not recreatable (here: not a reliant worktree) falls
// back while it is gone, and when it comes back — a volume re-attached —
// calls run there again and every thread that was moved is told.
func TestEnsure_ReportsAWorkspaceThatCameBack(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	project := t.TempDir()
	gone := filepath.Join(t.TempDir(), "elsewhere")
	e := newWorkspaceEnsurer()
	req := toolexec.WorkspaceEnsureRequest{Path: gone, FallbackPath: project, Audience: "thread-a"}

	first := ensureVia(t, e, req)
	if first.Status != toolexec.WorkspaceFallback || first.Path != project || first.Notice == nil {
		t.Fatalf("ensure = %+v, want fallback to %s with a notice", first, project)
	}
	if again := ensureVia(t, e, req); again.Status != toolexec.WorkspaceFallback || again.Notice != nil {
		t.Errorf("steady fallback = %+v, want no repeated notice", again)
	}

	if err := os.MkdirAll(gone, 0o755); err != nil {
		t.Fatal(err)
	}
	back := ensureVia(t, e, req)
	if back.Status != toolexec.WorkspacePresent || back.Path != gone {
		t.Fatalf("ensure = %+v, want present at %s", back, gone)
	}
	if back.Notice == nil || back.Notice.Kind != toolexec.NoticeRestored || back.Notice.PreviouslyRunningIn != project {
		t.Errorf("notice = %+v, want restored, previously running in %s", back.Notice, project)
	}
}

// A healthy workspace costs a stat and changes nothing.
func TestEnsure_PresentWorkspaceIsANoOp(t *testing.T) {
	f := newEnsureFixture(t, "api", "web")
	resp := ensureVia(t, newWorkspaceEnsurer(), f.request("thread-a"))
	if resp.Status != toolexec.WorkspacePresent || resp.Path != f.root || resp.Notice != nil {
		t.Errorf("ensure = %+v, want present at %s with no notice", resp, f.root)
	}
}

// One checkout cannot come back, the other is fine: keep running in the
// workspace and say which one is missing, rather than moving the chat away
// from the checkout that survived.
func TestEnsure_ReportsAnIncompleteWorkspaceInPlace(t *testing.T) {
	f := newEnsureFixture(t, "api", "web")
	web := filepath.Join(f.root, "web")
	if err := os.RemoveAll(web); err != nil {
		t.Fatal(err)
	}
	webRepo := filepath.Join(f.project, "web")
	rcGit(t, webRepo, "worktree", "remove", "--force", "--force", web)
	rcGit(t, webRepo, "branch", "-D", f.branch)

	resp := ensureVia(t, newWorkspaceEnsurer(), f.request("thread-a"))
	if resp.Status != toolexec.WorkspaceIncomplete || resp.Path != f.root {
		t.Fatalf("ensure = %+v, want incomplete at %s", resp, f.root)
	}
	if resp.Notice == nil || strings.Join(resp.Notice.Checkouts, ",") != "web" {
		t.Errorf("notice = %+v, want it to name web", resp.Notice)
	}
}

// The command is reachable through the registry the gateway dispatches on.
func TestEnsure_IsRegistered(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	payload, _ := json.Marshal(toolexec.WorkspaceEnsureRequest{Path: t.TempDir()})
	raw, err := defaultRegistry.Handle(context.Background(), toolexec.WorkspaceEnsureCommand, payload)
	if err != nil {
		t.Fatal(err)
	}
	var resp toolexec.WorkspaceEnsureResponse
	if err := json.Unmarshal(raw, &resp); err != nil || resp.Status != toolexec.WorkspacePresent {
		t.Fatalf("worktree.ensure via the registry = %s (%v)", raw, err)
	}
}
