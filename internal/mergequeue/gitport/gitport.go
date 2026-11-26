// Copyright (c) 2025 Reliant Labs
package gitport

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
)

// ErrGitTooOld means the git binary lacks `merge-tree --write-tree
// --merge-base` (git 2.40).
var ErrGitTooOld = errors.New("gitport: git 2.40 or newer is required (merge-tree --write-tree --merge-base)")

// ErrConflict is what a [*ConflictError] unwraps to.
var ErrConflict = errors.New("gitport: merge conflict")

// ConflictError is a squash or revert that does not apply cleanly.
type ConflictError struct {
	// Op is "squash" or "revert".
	Op string
	// Paths are the conflicting paths, sorted and unique.
	Paths []string
	// Tree is the tree git wrote with conflict markers. It is never a
	// candidate; it is there for diagnostics.
	Tree string
}

func (e *ConflictError) Error() string {
	return fmt.Sprintf("gitport: %s conflicts in %d path(s): %s", e.Op, len(e.Paths), strings.Join(e.Paths, ", "))
}
func (e *ConflictError) Unwrap() error { return ErrConflict }

// Identity is the author and committer of commits the port writes.
type Identity struct {
	Name  string
	Email string
}

// DefaultIdentity is the identity used when [Options.Identity] is empty.
func DefaultIdentity() Identity {
	return Identity{Name: "Reliant Queue", Email: "queue@reliantlabs.io"}
}

// Options configure a [Repo].
type Options struct {
	// GitBin is the git binary; default "git" from PATH.
	GitBin string
	// Identity authors and commits every commit written.
	Identity Identity
	// Env is appended to git's hermetic environment, for credentials and
	// for test pinning (GIT_AUTHOR_DATE, GIT_COMMITTER_DATE).
	Env []string
}

var _ Port = (*Repo)(nil)

// Repo is a bare git repository used as the queue's workdir.
type Repo struct {
	dir  string
	opts Options
}

// Dir is the repository's git directory.
func (r *Repo) Dir() string { return r.dir }

// Init creates (or reuses) a bare repository at dir.
func Init(ctx context.Context, dir string, opts Options) (*Repo, error) {
	r := newRepo(dir, opts)
	if err := r.checkVersion(ctx); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("gitport: create %s: %w", dir, err)
	}
	if _, err := r.run(ctx, nil, "init", "--bare", "--quiet", dir); err != nil {
		return nil, err
	}
	return r, nil
}

// Clone creates a bare repository at dir and fetches every branch of url into
// refs/remotes/origin/*. It is Init plus Fetch, so a retry after a partial
// clone resumes instead of failing on a non-empty directory.
func Clone(ctx context.Context, url, dir string, opts Options) (*Repo, error) {
	r, err := Init(ctx, dir, opts)
	if err != nil {
		return nil, err
	}
	if err := r.Fetch(ctx, url, "+refs/heads/*:refs/remotes/origin/*"); err != nil {
		return nil, err
	}
	return r, nil
}

// Open opens an existing repository (bare, or a checkout's .git directory).
func Open(ctx context.Context, dir string, opts Options) (*Repo, error) {
	r := newRepo(dir, opts)
	if err := r.checkVersion(ctx); err != nil {
		return nil, err
	}
	if _, err := r.git(ctx, nil, "rev-parse", "--git-dir"); err != nil {
		return nil, fmt.Errorf("gitport: %s is not a git repository: %w", dir, err)
	}
	return r, nil
}

func newRepo(dir string, opts Options) *Repo {
	if opts.GitBin == "" {
		opts.GitBin = "git"
	}
	if opts.Identity == (Identity{}) {
		opts.Identity = DefaultIdentity()
	}
	return &Repo{dir: dir, opts: opts}
}

// Fetch fetches refspecs from url. With no refspecs it fetches url's default
// (which, in a bare repository with no remote configured, is HEAD only).
func (r *Repo) Fetch(ctx context.Context, url string, refspecs ...string) error {
	args := append([]string{"fetch", "--quiet", "--no-tags", "--no-write-fetch-head", url}, refspecs...)
	_, err := r.git(ctx, nil, args...)
	return err
}

// ResolveCommit resolves rev (a sha, branch or ref) to a commit sha.
func (r *Repo) ResolveCommit(ctx context.Context, rev string) (string, error) {
	out, err := r.git(ctx, nil, "rev-parse", "--verify", "--quiet", "--end-of-options", rev+"^{commit}")
	if err != nil {
		return "", fmt.Errorf("gitport: %q is not a commit: %w", rev, err)
	}
	return strings.TrimSpace(out), nil
}

// TreeOf returns the tree sha of rev.
func (r *Repo) TreeOf(ctx context.Context, rev string) (string, error) {
	out, err := r.git(ctx, nil, "rev-parse", "--verify", "--quiet", "--end-of-options", rev+"^{tree}")
	if err != nil {
		return "", fmt.Errorf("gitport: %q has no tree: %w", rev, err)
	}
	return strings.TrimSpace(out), nil
}

// Result is a commit the port wrote.
type Result struct {
	Commit string
	Tree   string
}

// SquashInput describes one squash: Head's changes, merged into Base, as one
// commit whose only parent is Base. It is the commit GitHub's squash merge
// writes for the same PR (fakegithub's equivalence test holds the two equal).
type SquashInput struct {
	Base    string
	Head    string
	Title   string
	Body    string
	Trailer []Trailer
}

// Squash merges Head into Base in memory (merge-ort, merge base computed as
// git merge-base) and commits the result onto Base.
func (r *Repo) Squash(ctx context.Context, in SquashInput) (Result, error) {
	if in.Title == "" {
		return Result{}, errors.New("gitport: squash needs a title")
	}
	base, err := r.ResolveCommit(ctx, in.Base)
	if err != nil {
		return Result{}, err
	}
	head, err := r.ResolveCommit(ctx, in.Head)
	if err != nil {
		return Result{}, err
	}
	tree, err := r.mergeTree(ctx, "squash", "", base, head)
	if err != nil {
		return Result{}, err
	}
	msg, err := FormatMessage(in.Title, in.Body, in.Trailer)
	if err != nil {
		return Result{}, err
	}
	commit, err := r.commitTree(ctx, tree, base, msg)
	if err != nil {
		return Result{}, err
	}
	return Result{Commit: commit, Tree: tree}, nil
}

// RevertInput describes an exact revert of Commit, applied onto Onto.
type RevertInput struct {
	// Commit is the commit to revert. It must have exactly one parent: the
	// queue only reverts its own squash commits.
	Commit string
	// Onto is where the revert commit goes, usually main's tip. Empty means
	// Commit itself (the revert of the tip, whose tree is Commit's parent's).
	Onto string
	// Title defaults to git's `Revert "<subject>"`. The body always ends
	// with git's "This reverts commit <sha>." line, then Trailer.
	Title   string
	Trailer []Trailer
}

// Revert writes `git revert`'s result without a checkout: a 3-way merge of
// Onto and Commit's parent with Commit as the base, committed onto Onto.
func (r *Repo) Revert(ctx context.Context, in RevertInput) (Result, error) {
	commit, err := r.ResolveCommit(ctx, in.Commit)
	if err != nil {
		return Result{}, err
	}
	onto := commit
	if in.Onto != "" {
		if onto, err = r.ResolveCommit(ctx, in.Onto); err != nil {
			return Result{}, err
		}
	}
	info, err := r.readCommit(ctx, commit)
	if err != nil {
		return Result{}, err
	}
	if len(info.parents) != 1 {
		return Result{}, fmt.Errorf("gitport: revert of %s refused: it has %d parents, and only single-parent (squash) commits are reverted", commit, len(info.parents))
	}
	tree, err := r.mergeTree(ctx, "revert", commit, onto, info.parents[0])
	if err != nil {
		return Result{}, err
	}
	title := in.Title
	if title == "" {
		title = fmt.Sprintf("Revert %q", info.subject)
	}
	msg, err := FormatMessage(title, "This reverts commit "+commit+".", in.Trailer)
	if err != nil {
		return Result{}, err
	}
	out, err := r.commitTree(ctx, tree, onto, msg)
	if err != nil {
		return Result{}, err
	}
	return Result{Commit: out, Tree: tree}, nil
}

// PushInput is one ref update on the remote.
type PushInput struct {
	// SHA is the local commit to push.
	SHA string
	// Ref is the remote's fully qualified ref: "refs/heads/reliant-queue/42".
	Ref string
	// Force allows a non-fast-forward update.
	Force bool
	// ExpectedOld, when set, makes the push a compare-and-swap
	// (--force-with-lease): it fails unless the remote ref is at this sha.
	// It implies Force. Use the zero sha (40 zeros) for "must not exist".
	// A push of the sha the remote ref already holds is an up-to-date no-op
	// whatever the lease, so a retried push is idempotent.
	ExpectedOld string
}

// Push pushes in.SHA to in.Ref on url, without touching any local ref.
func (r *Repo) Push(ctx context.Context, url string, in PushInput) error {
	if !strings.HasPrefix(in.Ref, "refs/") {
		return fmt.Errorf("gitport: push ref %q is not fully qualified", in.Ref)
	}
	sha, err := r.ResolveCommit(ctx, in.SHA)
	if err != nil {
		return err
	}
	args := []string{"push", "--quiet", "--no-verify", "--porcelain"}
	switch {
	case in.ExpectedOld != "":
		old := in.ExpectedOld
		if strings.Trim(old, "0") == "" {
			old = "" // --force-with-lease=<ref>: with an empty value means "must not exist"
		}
		args = append(args, "--force-with-lease="+in.Ref+":"+old)
	case in.Force:
		args = append(args, "--force")
	}
	args = append(args, url, sha+":"+in.Ref)
	_, err = r.git(ctx, nil, args...)
	return err
}

// mergeTree runs merge-tree and returns the clean tree, or a *ConflictError.
// mergeBase empty lets git compute it.
func (r *Repo) mergeTree(ctx context.Context, op, mergeBase, ours, theirs string) (string, error) {
	args := []string{"merge-tree", "--write-tree", "-z", "--name-only", "--no-messages"}
	if mergeBase != "" {
		args = append(args, "--merge-base="+mergeBase)
	}
	args = append(args, ours, theirs)
	stdout, stderr, code, err := r.exec(ctx, nil, args...)
	if err != nil && code != 1 {
		return "", gitError(args, stderr, err)
	}
	fields := strings.Split(strings.TrimRight(stdout, "\x00"), "\x00")
	tree := strings.TrimSpace(fields[0])
	if !isSHA(tree) {
		return "", fmt.Errorf("gitport: merge-tree printed no tree: %q (stderr %q)", stdout, stderr)
	}
	if code == 0 {
		return tree, nil
	}
	paths := uniqueSorted(fields[1:])
	return "", &ConflictError{Op: op, Paths: paths, Tree: tree}
}

func (r *Repo) commitTree(ctx context.Context, tree, parent, msg string) (string, error) {
	out, err := r.git(ctx, strings.NewReader(msg), "commit-tree", tree, "-p", parent, "-F", "-")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

type commitInfo struct {
	tree    string
	parents []string
	subject string
}

func (r *Repo) readCommit(ctx context.Context, sha string) (commitInfo, error) {
	out, err := r.git(ctx, nil, "cat-file", "commit", sha)
	if err != nil {
		return commitInfo{}, err
	}
	header, msg, _ := strings.Cut(out, "\n\n")
	var info commitInfo
	for _, line := range strings.Split(header, "\n") {
		k, v, _ := strings.Cut(line, " ")
		switch k {
		case "tree":
			info.tree = v
		case "parent":
			info.parents = append(info.parents, v)
		}
	}
	info.subject, _, _ = strings.Cut(msg, "\n")
	return info, nil
}

func (r *Repo) git(ctx context.Context, stdin *strings.Reader, args ...string) (string, error) {
	stdout, stderr, _, err := r.exec(ctx, stdin, args...)
	if err != nil {
		return "", gitError(args, stderr, err)
	}
	return stdout, nil
}

// run is git without -C, for init (whose target may not exist yet).
func (r *Repo) run(ctx context.Context, stdin *strings.Reader, args ...string) (string, error) {
	return r.gitIn(ctx, "", stdin, args...)
}

func (r *Repo) gitIn(ctx context.Context, dir string, stdin *strings.Reader, args ...string) (string, error) {
	stdout, stderr, _, err := r.execIn(ctx, dir, stdin, args...)
	if err != nil {
		return "", gitError(args, stderr, err)
	}
	return stdout, nil
}

func (r *Repo) exec(ctx context.Context, stdin *strings.Reader, args ...string) (stdout, stderr string, code int, err error) {
	return r.execIn(ctx, r.dir, stdin, args...)
}

func (r *Repo) execIn(ctx context.Context, gitDir string, stdin *strings.Reader, args ...string) (stdout, stderr string, code int, err error) {
	full := args
	if gitDir != "" {
		full = append([]string{"--git-dir=" + gitDir}, args...)
	}
	cmd := exec.CommandContext(ctx, r.opts.GitBin, full...)
	cmd.Env = HermeticEnv(r.opts.Identity, r.opts.Env...)
	if stdin != nil {
		cmd.Stdin = stdin
	}
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	err = cmd.Run()
	code = 0
	if err != nil {
		code = -1
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			code = ee.ExitCode()
		}
	}
	return out.String(), errb.String(), code, err
}

// HermeticEnv is the environment every git command of the port runs with:
// PATH and HOME from the process (git needs them to find helpers), no system
// or global config, no terminal prompts, the identity as author and committer,
// then extra.
func HermeticEnv(id Identity, extra ...string) []string {
	if id == (Identity{}) {
		id = DefaultIdentity()
	}
	env := []string{
		"PATH=" + os.Getenv("PATH"), //nolint:forbidigo // ambient OS variable: git finds its helpers on PATH
		"HOME=" + os.Getenv("HOME"), //nolint:forbidigo // ambient OS variable: git and ssh resolve ~ from HOME
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL=" + os.DevNull,
		"GIT_TERMINAL_PROMPT=0",
		"LC_ALL=C",
		"GIT_AUTHOR_NAME=" + id.Name,
		"GIT_AUTHOR_EMAIL=" + id.Email,
		"GIT_COMMITTER_NAME=" + id.Name,
		"GIT_COMMITTER_EMAIL=" + id.Email,
	}
	return append(env, extra...)
}

func gitError(args []string, stderr string, err error) error {
	verb := ""
	if len(args) > 0 {
		verb = args[0]
	}
	return fmt.Errorf("gitport: git %s: %w: %s", verb, err, strings.TrimSpace(stderr))
}

var (
	versionMu    sync.Mutex
	versionCheck = map[string]error{}
	versionRE    = regexp.MustCompile(`git version (\d+)\.(\d+)`)
)

// checkVersion verifies, once per binary, that git has merge-tree
// --write-tree --merge-base.
func (r *Repo) checkVersion(ctx context.Context) error {
	versionMu.Lock()
	defer versionMu.Unlock()
	if err, ok := versionCheck[r.opts.GitBin]; ok {
		return err
	}
	out, err := r.run(ctx, nil, "version")
	if err != nil {
		return fmt.Errorf("gitport: run %s: %w", r.opts.GitBin, err)
	}
	err = parseVersion(out)
	versionCheck[r.opts.GitBin] = err
	return err
}

func parseVersion(out string) error {
	m := versionRE.FindStringSubmatch(out)
	if m == nil {
		return fmt.Errorf("%w: cannot parse %q", ErrGitTooOld, strings.TrimSpace(out))
	}
	major, _ := strconv.Atoi(m[1])
	minor, _ := strconv.Atoi(m[2])
	if major < 2 || (major == 2 && minor < 40) {
		return fmt.Errorf("%w: have %s", ErrGitTooOld, strings.TrimSpace(out))
	}
	return nil
}

func isSHA(s string) bool {
	if len(s) != 40 && len(s) != 64 {
		return false
	}
	for _, c := range s {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

func uniqueSorted(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, p := range in {
		if p == "" || seen[p] {
			continue
		}
		seen[p] = true
		out = append(out, p)
	}
	slices.Sort(out)
	return out
}
