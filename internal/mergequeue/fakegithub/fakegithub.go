// Copyright (c) 2025 Reliant Labs
package fakegithub

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/reliant-labs/reliant/internal/mergequeue/ghport"
)

// GitHub is the fake. It is safe for concurrent use.
type GitHub struct {
	t    testing.TB
	root string

	mu            sync.Mutex
	repos         map[string]*Repo
	faults        []*Fault
	calls         []Call
	hooks         []*hook
	nextCheckID   int64
	nextCommentID int64
}

var _ ghport.Client = (*GitHub)(nil)

// New returns an empty fake whose repositories live under t.TempDir().
func New(t testing.TB) *GitHub {
	t.Helper()
	return &GitHub{t: t, root: t.TempDir(), repos: map[string]*Repo{}}
}

// Repo is one fake repository and the PRs and check runs on it. Its methods
// are test helpers: they fail the test on error rather than return one.
type Repo struct {
	g   *GitHub
	id  ghport.Repo
	dir string

	// The fields below are guarded by g.mu.
	prs            map[int]*pullRequest
	nextPR         int
	checks         map[int64]*ghport.CheckRun
	squashOverride func(SquashCall) string
	diverge        []divergence
	squashCache    map[string]squashOutcome
}

type pullRequest struct {
	number    int
	title     string
	headRef   string
	baseRef   string
	head      string
	state     ghport.PRState
	merged    bool
	mergeSHA  string
	mergeHead string
	comments  []ghport.IssueComment
}

// NewRepo creates an empty bare repository named full ("owner/name").
func (g *GitHub) NewRepo(full string) *Repo {
	g.t.Helper()
	id, err := ghport.ParseRepo(full)
	if err != nil {
		g.t.Fatalf("fakegithub: %v", err)
	}
	dir := filepath.Join(g.root, id.Owner, id.Name+".git")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		g.t.Fatalf("fakegithub: %v", err)
	}
	if res := rawGit(dir, seedIdentity, "", nil, "init", "--bare", "--quiet", "--initial-branch=main"); res.err != nil {
		g.t.Fatalf("fakegithub: %v", res.wrap("init"))
	}
	r := &Repo{
		g: g, id: id, dir: dir,
		prs: map[int]*pullRequest{}, checks: map[int64]*ghport.CheckRun{},
		squashCache: map[string]squashOutcome{},
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if _, dup := g.repos[id.String()]; dup {
		g.t.Fatalf("fakegithub: repository %s already exists", id)
	}
	g.repos[id.String()] = r
	return r
}

// ID is the repository's name for [ghport.Client] calls.
func (r *Repo) ID() ghport.Repo { return r.id }

// Dir is the bare repository's git directory.
func (r *Repo) Dir() string { return r.dir }

// URL is what git fetches from and pushes to (a local path).
func (r *Repo) URL() string { return r.dir }

// Commit writes a commit onto branch with files changed (content [Deleted]
// removes a path) and advances the branch: a push. A missing branch is
// created with a root commit. It returns the new commit's sha.
func (r *Repo) Commit(branch, msg string, files map[string]string) string {
	r.g.t.Helper()
	r.g.mu.Lock()
	defer r.g.mu.Unlock()
	sha, err := r.commitLocked(branch, msg, files)
	if err != nil {
		r.g.t.Fatalf("fakegithub: commit on %s: %v", branch, err)
	}
	return sha
}

func (r *Repo) commitLocked(branch, msg string, files map[string]string) (string, error) {
	ref := ghport.BranchRef(branch)
	parent := r.refSHA(ref)
	tree, err := r.editTree(parent, files)
	if err != nil {
		return "", err
	}
	var parents []string
	if parent != "" {
		parents = []string{parent}
	}
	sha, err := r.writeCommit(seedIdentity, tree, parents, msg+"\n")
	if err != nil {
		return "", err
	}
	return sha, r.updateRef(ref, sha, parent)
}

// Branch creates branch at from (a branch name or sha).
func (r *Repo) Branch(branch, from string) string {
	r.g.t.Helper()
	sha := r.resolve(from)
	if sha == "" {
		r.g.t.Fatalf("fakegithub: %q is not a commit", from)
	}
	if err := r.updateRef(ghport.BranchRef(branch), sha, ""); err != nil {
		r.g.t.Fatalf("fakegithub: %v", err)
	}
	return sha
}

// SetBranch force-moves (or creates) branch to sha.
func (r *Repo) SetBranch(branch, sha string) {
	r.g.t.Helper()
	if _, err := r.git("update-ref", ghport.BranchRef(branch), sha); err != nil {
		r.g.t.Fatalf("fakegithub: %v", err)
	}
}

// Head returns the commit branch points at, or "" when it does not exist.
func (r *Repo) Head(branch string) string { return r.refSHA(ghport.BranchRef(branch)) }

// Tree returns the tree of a commit (or branch).
func (r *Repo) Tree(rev string) string {
	r.g.t.Helper()
	out, err := r.git("rev-parse", "--verify", rev+"^{tree}")
	if err != nil {
		r.g.t.Fatalf("fakegithub: %v", err)
	}
	return out
}

// PROptions open a PR.
type PROptions struct {
	Title string
	// Head is the head branch; it must exist.
	Head string
	// Base is the base branch; default "main".
	Base string
}

// OpenPR opens a PR and returns its number.
func (r *Repo) OpenPR(o PROptions) int {
	r.g.t.Helper()
	if o.Base == "" {
		o.Base = "main"
	}
	if o.Title == "" {
		o.Title = "Change " + o.Head
	}
	head := r.Head(o.Head)
	if head == "" {
		r.g.t.Fatalf("fakegithub: head branch %q does not exist", o.Head)
	}
	if r.Head(o.Base) == "" {
		r.g.t.Fatalf("fakegithub: base branch %q does not exist", o.Base)
	}
	r.g.mu.Lock()
	defer r.g.mu.Unlock()
	r.nextPR++
	r.prs[r.nextPR] = &pullRequest{
		number: r.nextPR, title: o.Title, headRef: o.Head, baseRef: o.Base,
		head: head, state: ghport.PRStateOpen,
	}
	return r.nextPR
}

// ClosePR closes a PR without merging it, as its author might mid-landing.
func (r *Repo) ClosePR(n int) {
	r.g.t.Helper()
	r.g.mu.Lock()
	defer r.g.mu.Unlock()
	p := r.mustPR(n)
	r.refreshHead(p)
	p.state = ghport.PRStateClosed
}

// ReopenPR reopens a closed, unmerged PR.
func (r *Repo) ReopenPR(n int) {
	r.g.t.Helper()
	r.g.mu.Lock()
	defer r.g.mu.Unlock()
	p := r.mustPR(n)
	if p.merged {
		r.g.t.Fatalf("fakegithub: PR #%d is merged", n)
	}
	if r.refSHA(ghport.BranchRef(p.headRef)) == "" {
		r.g.t.Fatalf("fakegithub: PR #%d's head branch %s is gone", n, p.headRef)
	}
	p.state = ghport.PRStateOpen
	r.refreshHead(p)
}

func (r *Repo) mustPR(n int) *pullRequest {
	p, ok := r.prs[n]
	if !ok {
		r.g.t.Fatalf("fakegithub: no PR #%d in %s", n, r.id)
	}
	return p
}

// refreshHead follows an open PR's head branch, so a push through git (or
// [Repo.Commit]) is a new head. Caller holds g.mu.
func (r *Repo) refreshHead(p *pullRequest) {
	if p.state != ghport.PRStateOpen {
		return
	}
	if sha := r.refSHA(ghport.BranchRef(p.headRef)); sha != "" {
		p.head = sha
	}
}

// repo looks up a repository for a client call.
func (g *GitHub) repo(id ghport.Repo) (*Repo, error) {
	r, ok := g.repos[id.String()]
	if !ok {
		return nil, &ghport.NotFoundError{What: "repository " + id.String()}
	}
	return r, nil
}

// ---- ghport.Client ----

// GetRef implements [ghport.Client].
func (g *GitHub) GetRef(ctx context.Context, id ghport.Repo, ref string) (ghport.Ref, error) {
	after, err := g.begin(ctx, Call{Method: MethodGetRef, Repo: id, Ref: ref})
	if err != nil {
		return ghport.Ref{}, err
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	out, err := g.getRef(id, ref)
	return finish(out, err, after)
}

func (g *GitHub) getRef(id ghport.Repo, ref string) (ghport.Ref, error) {
	r, err := g.repo(id)
	if err != nil {
		return ghport.Ref{}, err
	}
	if err := ghport.ValidateRef(ref); err != nil {
		return ghport.Ref{}, err
	}
	sha := r.refSHA(ref)
	if sha == "" {
		return ghport.Ref{}, &ghport.NotFoundError{What: "ref " + ref}
	}
	return ghport.Ref{Name: ref, SHA: sha}, nil
}

// CreateRef implements [ghport.Client].
func (g *GitHub) CreateRef(ctx context.Context, id ghport.Repo, ref, sha string) error {
	after, err := g.begin(ctx, Call{Method: MethodCreateRef, Repo: id, Ref: ref, SHA: sha})
	if err != nil {
		return err
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	return finishErr(g.createRef(id, ref, sha), after)
}

func (g *GitHub) createRef(id ghport.Repo, ref, sha string) error {
	r, err := g.repo(id)
	if err != nil {
		return err
	}
	if err := ghport.ValidateRef(ref); err != nil {
		return err
	}
	if r.refSHA(ref) != "" {
		return fmt.Errorf("%w: ref %s", ghport.ErrAlreadyExists, ref)
	}
	if !r.isCommit(sha) {
		return &ghport.ValidationError{Field: "sha", Reason: "object " + sha + " does not exist"}
	}
	return r.updateRef(ref, sha, "")
}

// UpdateRef implements [ghport.Client].
func (g *GitHub) UpdateRef(ctx context.Context, id ghport.Repo, ref, sha string, force bool) error {
	after, err := g.begin(ctx, Call{Method: MethodUpdateRef, Repo: id, Ref: ref, SHA: sha})
	if err != nil {
		return err
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	return finishErr(g.updateRef(id, ref, sha, force), after)
}

func (g *GitHub) updateRef(id ghport.Repo, ref, sha string, force bool) error {
	r, err := g.repo(id)
	if err != nil {
		return err
	}
	if err := ghport.ValidateRef(ref); err != nil {
		return err
	}
	old := r.refSHA(ref)
	if old == "" {
		return &ghport.NotFoundError{What: "ref " + ref}
	}
	if !r.isCommit(sha) {
		return &ghport.ValidationError{Field: "sha", Reason: "object " + sha + " does not exist"}
	}
	if !force && !r.isAncestor(old, sha) {
		return fmt.Errorf("%w: %s from %s to %s", ghport.ErrNotFastForward, ref, old, sha)
	}
	return r.updateRef(ref, sha, old)
}

// DeleteRef implements [ghport.Client]. Deleting an open PR's head branch
// closes the PR.
func (g *GitHub) DeleteRef(ctx context.Context, id ghport.Repo, ref string) error {
	after, err := g.begin(ctx, Call{Method: MethodDeleteRef, Repo: id, Ref: ref})
	if err != nil {
		return err
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	return finishErr(g.deleteRef(id, ref), after)
}

func (g *GitHub) deleteRef(id ghport.Repo, ref string) error {
	r, err := g.repo(id)
	if err != nil {
		return err
	}
	if err := ghport.ValidateRef(ref); err != nil {
		return err
	}
	old := r.refSHA(ref)
	if old == "" {
		return &ghport.NotFoundError{What: "ref " + ref}
	}
	for _, p := range r.prs {
		if p.state == ghport.PRStateOpen && ghport.BranchRef(p.headRef) == ref {
			r.refreshHead(p)
			p.state = ghport.PRStateClosed
		}
	}
	_, err = r.git("update-ref", "-d", ref, old)
	return err
}

// GetPullRequest implements [ghport.Client].
func (g *GitHub) GetPullRequest(ctx context.Context, id ghport.Repo, number int) (ghport.PullRequest, error) {
	after, err := g.begin(ctx, Call{Method: MethodGetPullRequest, Repo: id, Number: number})
	if err != nil {
		return ghport.PullRequest{}, err
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	out, err := g.getPR(id, number)
	return finish(out, err, after)
}

func (g *GitHub) getPR(id ghport.Repo, number int) (ghport.PullRequest, error) {
	r, err := g.repo(id)
	if err != nil {
		return ghport.PullRequest{}, err
	}
	p, ok := r.prs[number]
	if !ok {
		return ghport.PullRequest{}, &ghport.NotFoundError{What: fmt.Sprintf("pull request #%d", number)}
	}
	r.refreshHead(p)
	out := ghport.PullRequest{
		Number: p.number, Title: p.title, State: p.state,
		HeadSHA: p.head, HeadRef: p.headRef, BaseRef: p.baseRef,
		Merged: p.merged, MergeCommitSHA: p.mergeSHA, Mergeable: ghport.MergeableUnknown,
	}
	if p.state == ghport.PRStateOpen {
		if base := r.refSHA(ghport.BranchRef(p.baseRef)); base != "" {
			if _, conflicts, err := r.githubSquash(base, p.head); err == nil {
				out.Mergeable = ghport.MergeableClean
				if len(conflicts) > 0 {
					out.Mergeable = ghport.MergeableConflicting
				}
			}
		}
	}
	return out, nil
}

// MergePullRequest implements [ghport.Client] with GitHub's squash semantics;
// see the package doc.
func (g *GitHub) MergePullRequest(ctx context.Context, id ghport.Repo, number int, req ghport.MergeRequest) (ghport.MergeResult, error) {
	after, err := g.begin(ctx, Call{Method: MethodMergePullRequest, Repo: id, Number: number, SHA: req.ExpectedHeadSHA})
	if err != nil {
		return ghport.MergeResult{}, err
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	out, err := g.merge(id, number, req)
	return finish(out, err, after)
}

func (g *GitHub) merge(id ghport.Repo, number int, req ghport.MergeRequest) (ghport.MergeResult, error) {
	r, err := g.repo(id)
	if err != nil {
		return ghport.MergeResult{}, err
	}
	p, ok := r.prs[number]
	if !ok {
		return ghport.MergeResult{}, &ghport.NotFoundError{What: fmt.Sprintf("pull request #%d", number)}
	}
	if p.merged {
		return ghport.MergeResult{}, &ghport.AlreadyMergedError{MergeCommitSHA: p.mergeSHA, HeadSHA: p.mergeHead}
	}
	if p.state != ghport.PRStateOpen {
		return ghport.MergeResult{}, &ghport.NotMergeableError{Reason: ghport.NotMergeableClosed, Message: "pull request is closed"}
	}
	if req.Method != ghport.MergeSquash {
		return ghport.MergeResult{}, &ghport.ValidationError{Field: "merge_method", Reason: fmt.Sprintf("the fake implements squash only, not %q", req.Method)}
	}
	r.refreshHead(p)
	if req.ExpectedHeadSHA != "" && req.ExpectedHeadSHA != p.head {
		return ghport.MergeResult{}, &ghport.HeadMismatchError{Expected: req.ExpectedHeadSHA, Actual: p.head}
	}
	baseRef := ghport.BranchRef(p.baseRef)
	baseTip := r.refSHA(baseRef)
	if baseTip == "" {
		return ghport.MergeResult{}, &ghport.NotMergeableError{Reason: ghport.NotMergeableBlocked, Message: "base branch " + p.baseRef + " does not exist"}
	}
	tree, conflicts, err := r.githubSquash(baseTip, p.head)
	if err != nil {
		return ghport.MergeResult{}, &ghport.NotMergeableError{Reason: ghport.NotMergeableConflict, Message: err.Error()}
	}
	if len(conflicts) > 0 {
		return ghport.MergeResult{}, &ghport.NotMergeableError{Reason: ghport.NotMergeableConflict, Message: "conflicts in " + strings.Join(conflicts, ", ")}
	}
	if tree, err = r.applySquashFaults(SquashCall{PR: number, BaseTip: baseTip, Head: p.head, Tree: tree}); err != nil {
		return ghport.MergeResult{}, err
	}
	title := req.CommitTitle
	if title == "" {
		title = fmt.Sprintf("%s (#%d)", p.title, p.number)
	}
	msg := title + "\n"
	if body := strings.TrimSpace(req.CommitMessage); body != "" {
		msg += "\n" + body + "\n"
	}
	sha, err := r.writeCommit(githubIdentity, tree, []string{baseTip}, msg)
	if err != nil {
		return ghport.MergeResult{}, err
	}
	if err := r.updateRef(baseRef, sha, baseTip); err != nil {
		return ghport.MergeResult{}, &ghport.TransientError{Status: 500, Message: err.Error()}
	}
	p.state, p.merged, p.mergeSHA, p.mergeHead = ghport.PRStateClosed, true, sha, p.head
	return ghport.MergeResult{SHA: sha}, nil
}

// CreateCheckRun implements [ghport.Client].
func (g *GitHub) CreateCheckRun(ctx context.Context, id ghport.Repo, in ghport.CreateCheckRun) (ghport.CheckRun, error) {
	after, err := g.begin(ctx, Call{Method: MethodCreateCheckRun, Repo: id, SHA: in.HeadSHA})
	if err != nil {
		return ghport.CheckRun{}, err
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	out, err := g.createCheck(id, in)
	return finish(out, err, after)
}

func (g *GitHub) createCheck(id ghport.Repo, in ghport.CreateCheckRun) (ghport.CheckRun, error) {
	r, err := g.repo(id)
	if err != nil {
		return ghport.CheckRun{}, err
	}
	if in.Name == "" {
		return ghport.CheckRun{}, &ghport.ValidationError{Field: "name", Reason: "required"}
	}
	if !r.isCommit(in.HeadSHA) {
		return ghport.CheckRun{}, &ghport.ValidationError{Field: "head_sha", Reason: "no commit " + in.HeadSHA}
	}
	status := in.Status
	if status == "" {
		status = ghport.CheckQueued
		if in.Conclusion != "" {
			status = ghport.CheckCompleted
		}
	}
	if err := ghport.ValidateCheckRun(status, in.Conclusion); err != nil {
		return ghport.CheckRun{}, err
	}
	g.nextCheckID++
	run := &ghport.CheckRun{
		ID: g.nextCheckID, Name: in.Name, HeadSHA: in.HeadSHA, Status: status,
		Conclusion: in.Conclusion, Output: in.Output, ExternalID: in.ExternalID, DetailsURL: in.DetailsURL,
	}
	r.checks[run.ID] = run
	return *run, nil
}

// UpdateCheckRun implements [ghport.Client].
func (g *GitHub) UpdateCheckRun(ctx context.Context, id ghport.Repo, checkID int64, in ghport.UpdateCheckRun) (ghport.CheckRun, error) {
	after, err := g.begin(ctx, Call{Method: MethodUpdateCheckRun, Repo: id})
	if err != nil {
		return ghport.CheckRun{}, err
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	out, err := g.updateCheck(id, checkID, in)
	return finish(out, err, after)
}

func (g *GitHub) updateCheck(id ghport.Repo, checkID int64, in ghport.UpdateCheckRun) (ghport.CheckRun, error) {
	r, err := g.repo(id)
	if err != nil {
		return ghport.CheckRun{}, err
	}
	run, ok := r.checks[checkID]
	if !ok {
		return ghport.CheckRun{}, &ghport.NotFoundError{What: fmt.Sprintf("check run %d", checkID)}
	}
	next := *run
	if in.Status != "" {
		next.Status = in.Status
	}
	if in.Conclusion != "" {
		next.Conclusion = in.Conclusion
		if in.Status == "" {
			next.Status = ghport.CheckCompleted
		}
	}
	if next.Status != ghport.CheckCompleted {
		next.Conclusion = ""
	}
	if in.Output != nil {
		next.Output = *in.Output
	}
	if in.DetailsURL != "" {
		next.DetailsURL = in.DetailsURL
	}
	if err := ghport.ValidateCheckRun(next.Status, next.Conclusion); err != nil {
		return ghport.CheckRun{}, err
	}
	*run = next
	return next, nil
}

// ListCheckRuns implements [ghport.Client]: newest first.
func (g *GitHub) ListCheckRuns(ctx context.Context, id ghport.Repo, sha string, opts ghport.ListCheckRuns) ([]ghport.CheckRun, error) {
	after, err := g.begin(ctx, Call{Method: MethodListCheckRuns, Repo: id, SHA: sha})
	if err != nil {
		return nil, err
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	r, err := g.repo(id)
	if err != nil {
		return nil, err
	}
	var out []ghport.CheckRun
	for _, run := range r.checks {
		if run.HeadSHA == sha && (opts.Name == "" || run.Name == opts.Name) {
			out = append(out, *run)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID > out[j].ID })
	return finish(out, nil, after)
}

// GetContents implements [ghport.Client].
func (g *GitHub) GetContents(ctx context.Context, id ghport.Repo, path, ref string) ([]byte, error) {
	after, err := g.begin(ctx, Call{Method: MethodGetContents, Repo: id, Ref: ref})
	if err != nil {
		return nil, err
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	out, err := g.contents(id, path, ref)
	return finish(out, err, after)
}

func (g *GitHub) contents(id ghport.Repo, path, ref string) ([]byte, error) {
	r, err := g.repo(id)
	if err != nil {
		return nil, err
	}
	commit := r.resolve(ref)
	if commit == "" {
		return nil, &ghport.NotFoundError{What: "ref " + ref}
	}
	obj := commit + ":" + strings.TrimPrefix(path, "/")
	if res := rawGit(r.dir, seedIdentity, "", nil, "cat-file", "-t", obj); res.err != nil || strings.TrimSpace(res.stdout) != "blob" {
		return nil, &ghport.NotFoundError{What: fmt.Sprintf("file %s at %s", path, ref)}
	}
	res := rawGit(r.dir, seedIdentity, "", nil, "cat-file", "blob", obj)
	if err := res.wrap("cat-file", "blob", obj); err != nil {
		return nil, err
	}
	return []byte(res.stdout), nil
}

// GetCommit implements [ghport.Client].
func (g *GitHub) GetCommit(ctx context.Context, id ghport.Repo, sha string) (ghport.Commit, error) {
	after, err := g.begin(ctx, Call{Method: MethodGetCommit, Repo: id, SHA: sha})
	if err != nil {
		return ghport.Commit{}, err
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	out, err := g.commit(id, sha)
	return finish(out, err, after)
}

func (g *GitHub) commit(id ghport.Repo, sha string) (ghport.Commit, error) {
	r, err := g.repo(id)
	if err != nil {
		return ghport.Commit{}, err
	}
	if !r.isCommit(sha) {
		return ghport.Commit{}, &ghport.NotFoundError{What: "commit " + sha}
	}
	res := rawGit(r.dir, seedIdentity, "", nil, "cat-file", "commit", sha)
	if err := res.wrap("cat-file", "commit", sha); err != nil {
		return ghport.Commit{}, err
	}
	header, msg, _ := strings.Cut(res.stdout, "\n\n")
	out := ghport.Commit{SHA: r.resolve(sha), Message: msg}
	for _, line := range strings.Split(header, "\n") {
		k, v, _ := strings.Cut(line, " ")
		switch k {
		case "tree":
			out.TreeSHA = v
		case "parent":
			out.Parents = append(out.Parents, v)
		}
	}
	return out, nil
}

// CreateCommit implements [ghport.Client]: a commit object, authored as
// GitHub would for the token's App.
func (g *GitHub) CreateCommit(ctx context.Context, id ghport.Repo, in ghport.NewCommit) (ghport.Commit, error) {
	after, err := g.begin(ctx, Call{Method: MethodCreateCommit, Repo: id, SHA: in.TreeSHA})
	if err != nil {
		return ghport.Commit{}, err
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	r, err := g.repo(id)
	if err != nil {
		return ghport.Commit{}, err
	}
	if res := rawGit(r.dir, seedIdentity, "", nil, "cat-file", "-t", in.TreeSHA); res.err != nil || strings.TrimSpace(res.stdout) != "tree" {
		return ghport.Commit{}, &ghport.ValidationError{Field: "tree", Reason: "no tree " + in.TreeSHA}
	}
	for _, p := range in.Parents {
		if !r.isCommit(p) {
			return ghport.Commit{}, &ghport.ValidationError{Field: "parents", Reason: "no commit " + p}
		}
	}
	msg := strings.TrimRight(in.Message, "\n") + "\n"
	sha, err := r.writeCommit(githubIdentity, in.TreeSHA, in.Parents, msg)
	if err != nil {
		return ghport.Commit{}, err
	}
	out, err := g.commit(id, sha)
	return finish(out, err, after)
}

// CreateIssueComment implements [ghport.Client].
func (g *GitHub) CreateIssueComment(ctx context.Context, id ghport.Repo, number int, body string) (ghport.IssueComment, error) {
	after, err := g.begin(ctx, Call{Method: MethodCreateComment, Repo: id, Number: number})
	if err != nil {
		return ghport.IssueComment{}, err
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	p, err := g.pr(id, number)
	if err != nil {
		return ghport.IssueComment{}, err
	}
	if strings.TrimSpace(body) == "" {
		return ghport.IssueComment{}, &ghport.ValidationError{Field: "body", Reason: "required"}
	}
	g.nextCommentID++
	c := ghport.IssueComment{ID: g.nextCommentID, Body: body}
	p.comments = append(p.comments, c)
	return finish(c, nil, after)
}

// ListIssueComments implements [ghport.Client]: oldest first.
func (g *GitHub) ListIssueComments(ctx context.Context, id ghport.Repo, number int) ([]ghport.IssueComment, error) {
	after, err := g.begin(ctx, Call{Method: MethodListComments, Repo: id, Number: number})
	if err != nil {
		return nil, err
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	p, err := g.pr(id, number)
	if err != nil {
		return nil, err
	}
	return finish(append([]ghport.IssueComment(nil), p.comments...), nil, after)
}

func (g *GitHub) pr(id ghport.Repo, number int) (*pullRequest, error) {
	r, err := g.repo(id)
	if err != nil {
		return nil, err
	}
	p, ok := r.prs[number]
	if !ok {
		return nil, &ghport.NotFoundError{What: fmt.Sprintf("pull request #%d", number)}
	}
	return p, nil
}

// Comments returns a PR's conversation comments, oldest first.
func (r *Repo) Comments(n int) []ghport.IssueComment {
	r.g.mu.Lock()
	defer r.g.mu.Unlock()
	return append([]ghport.IssueComment(nil), r.mustPR(n).comments...)
}

// isCommit reports whether sha names a commit (a full or abbreviated sha;
// never a branch name, which GitHub's sha fields do not accept).
func (r *Repo) isCommit(sha string) bool {
	if sha == "" || strings.ContainsAny(sha, "/^~:") {
		return false
	}
	res := rawGit(r.dir, seedIdentity, "", nil, "cat-file", "-t", sha)
	return res.err == nil && strings.TrimSpace(res.stdout) == "commit"
}

func (r *Repo) isAncestor(ancestor, of string) bool {
	return rawGit(r.dir, seedIdentity, "", nil, "merge-base", "--is-ancestor", ancestor, of).err == nil
}

func finish[T any](v T, err, after error) (T, error) {
	if err != nil {
		return v, err
	}
	if after != nil {
		var zero T
		return zero, after
	}
	return v, nil
}

func finishErr(err, after error) error {
	if err != nil {
		return err
	}
	return after
}
