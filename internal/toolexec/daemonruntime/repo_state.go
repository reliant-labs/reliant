// Copyright (c) 2025 Reliant Labs
package daemonruntime

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
)

// A snapshot says which commit its .reliant files come from, for the project
// root and each nested repo that has a .reliant directory: the branch, HEAD,
// whether .reliant has uncommitted changes, and origin's default branch. The
// server shows it beside an automation's definition and warns when that is a
// feature branch or an uncommitted edit.
//
// The project watcher rebuilds the snapshot every poll, so reading the state
// must be cheap: HEAD and its ref are read from the git directory as files
// (no process), and are part of the snapshot's version, so a commit or a
// checkout sends a new snapshot. Whether .reliant is dirty takes a git
// process, so it is computed only when what it depends on moved (dirtyKey):
// HEAD, and, as stats, the git index (staging or unstaging moves it), every
// file under .reliant, synced or not (an untracked README there is still an
// uncommitted change), and the ignore rules that decide whether an untracked
// file counts (.gitignore, info/exclude, the git config naming an excludes
// file). Rules outside those (an excludes file at a configured path) are
// caught by revalidating every repoDirtyRevalidate. A read that failed (a
// timeout, a transient git error) is retried with backoff rather than kept.
//
// Those stats key only the local cache. The snapshot's version takes the
// heads and the computed states (statesVersion), so an index refresh or a
// log written under .reliant that changes no dirty flag sends nothing, and a
// retry that recovers sends the corrected state.

// repoHead is a repository's HEAD as read from its git directory.
type repoHead struct {
	repo          string
	dir           string
	branch        string
	commit        string
	defaultBranch string
	err           string
	// dirtyInputs fingerprints what `git status -- .reliant` reads in this
	// repo: the index's, its ignore rules' and each .reliant file's size and
	// mtime.
	dirtyInputs string
}

// repoDirtyCache remembers each project's last computed dirty flags by the
// inputs they were computed from (dirtyKey).
var repoDirtyCache = struct {
	sync.Mutex
	byProject map[string]repoDirtyEntry
}{byProject: map[string]repoDirtyEntry{}}

type repoDirtyEntry struct {
	inputs string
	dirty  map[string]repoDirty
	// computedAt is when the flags were read; they are read again
	// repoDirtyRevalidate later even when no input moved.
	computedAt time.Time
	// retryAt is when a failed read is tried again; zero when none failed.
	retryAt time.Time
	backoff time.Duration
}

// The retry backoff after a failed dirty read: first after
// repoDirtyRetryMin, doubling to repoDirtyRetryMax while it keeps failing.
// A good read is revalidated after repoDirtyRevalidate, for an input the
// fingerprint cannot see.
var (
	repoDirtyRetryMin   = 10 * time.Second
	repoDirtyRetryMax   = 5 * time.Minute
	repoDirtyRevalidate = 2 * time.Minute
)

// reliantDirDirtyFn is reliantDirDirty, replaceable by a test.
var reliantDirDirtyFn = reliantDirDirty

type repoDirty struct {
	dirty bool
	err   string
}

// repoStateDirtyTimeout bounds one `git status` over a .reliant directory.
const repoStateDirtyTimeout = 5 * time.Second

// readRepoHeads reads the git HEAD of the project root and of each nested
// repo (repos, project-relative) that has a .reliant directory, root first.
// A directory that is not a git checkout is left out.
func readRepoHeads(projectPath string, repos []string) []repoHead {
	var heads []repoHead
	if h, ok := readRepoHead(projectPath, ""); ok {
		heads = append(heads, h)
	}
	sorted := append([]string(nil), repos...)
	sort.Strings(sorted)
	for _, rel := range sorted {
		rel = filepath.ToSlash(filepath.Clean(rel))
		if rel == "" || rel == "." {
			continue
		}
		dir := filepath.Join(projectPath, filepath.FromSlash(rel))
		if fi, err := os.Stat(filepath.Join(dir, ".reliant")); err != nil || !fi.IsDir() {
			continue
		}
		if h, ok := readRepoHead(dir, rel); ok {
			heads = append(heads, h)
		}
	}
	return heads
}

// headsVersion is the part of a snapshot's version the repo heads
// contribute: what they report, not the stats the dirty cache is keyed by.
func headsVersion(heads []repoHead) []byte {
	var b strings.Builder
	for _, h := range heads {
		fmt.Fprintf(&b, "%s@%s:%s:%s:%s;", h.repo, h.branch, h.commit, h.defaultBranch, h.err)
	}
	return []byte(b.String())
}

// dirtyKey is what the dirty flags of heads were computed from: the heads,
// each repo's dirtyInputs, and the user's global git ignore rules.
func dirtyKey(heads []repoHead) string {
	var b strings.Builder
	b.Write(headsVersion(heads))
	for _, h := range heads {
		fmt.Fprintf(&b, "%s{%s}", h.repo, h.dirtyInputs)
	}
	b.WriteString(globalIgnoreInputs())
	return b.String()
}

// globalIgnoreInputs fingerprints the user-level git files that can change
// which untracked files git status reports: the global config (core.excludesFile)
// and the default global excludes file.
func globalIgnoreInputs() string {
	var paths []string
	if home, err := os.UserHomeDir(); err == nil {
		paths = append(paths, filepath.Join(home, ".gitconfig"))
	}
	if xdg, err := os.UserConfigDir(); err == nil {
		paths = append(paths, filepath.Join(xdg, "git", "config"), filepath.Join(xdg, "git", "ignore"))
	}
	var b strings.Builder
	for _, p := range paths {
		statInput(&b, p, p)
	}
	return b.String()
}

// statInput writes name's size and mtime to b, or nothing when p is absent.
func statInput(b *strings.Builder, name, p string) {
	if fi, err := os.Stat(p); err == nil {
		fmt.Fprintf(b, "%s:%d:%d;", name, fi.Size(), fi.ModTime().UnixNano())
	}
}

// dirtyInputs fingerprints the git index, the repo's ignore rules and the
// files under dir/.reliant (its own .gitignore files among them) by size and
// mtime: stats only, no reads.
func dirtyInputs(dir, gitDir, commonDir string) string {
	var b strings.Builder
	statInput(&b, "index", filepath.Join(gitDir, "index"))
	statInput(&b, "gitignore", filepath.Join(dir, ".gitignore"))
	statInput(&b, "exclude", filepath.Join(commonDir, "info", "exclude"))
	statInput(&b, "config", filepath.Join(commonDir, "config"))
	root := filepath.Join(dir, ".reliant")
	_ = filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if fi, err := d.Info(); err == nil {
			rel, _ := filepath.Rel(root, p)
			fmt.Fprintf(&b, "%s:%d:%d;", rel, fi.Size(), fi.ModTime().UnixNano())
		}
		return nil
	})
	return b.String()
}

// repoStates completes heads with each repo's dirty flag, computing it only
// when its inputs (dirtyKey) moved since the last computation for
// projectPath, a failed read is due for a retry, or a good one for
// revalidation.
func repoStates(ctx context.Context, projectPath string, heads []repoHead) []*reliantv1.SyncedRepoState {
	if len(heads) == 0 {
		return nil
	}
	inputs := dirtyKey(heads)
	now := time.Now()
	repoDirtyCache.Lock()
	cached, ok := repoDirtyCache.byProject[projectPath]
	repoDirtyCache.Unlock()
	retry := ok && cached.inputs == inputs && !cached.retryAt.IsZero() && !now.Before(cached.retryAt)
	stale := ok && cached.retryAt.IsZero() && now.Sub(cached.computedAt) >= repoDirtyRevalidate
	if !ok || cached.inputs != inputs || retry || stale {
		next := repoDirtyEntry{inputs: inputs, dirty: map[string]repoDirty{}, computedAt: now}
		failed := false
		for _, h := range heads {
			dirty, err := reliantDirDirtyFn(ctx, h.dir)
			d := repoDirty{dirty: dirty}
			if err != nil {
				d.err = err.Error()
				failed = true
			}
			next.dirty[h.repo] = d
		}
		if failed {
			next.backoff = repoDirtyRetryMin
			if retry && cached.backoff > 0 {
				next.backoff = min(2*cached.backoff, repoDirtyRetryMax)
			}
			next.retryAt = now.Add(next.backoff)
		}
		cached = next
		repoDirtyCache.Lock()
		repoDirtyCache.byProject[projectPath] = cached
		repoDirtyCache.Unlock()
	}
	out := make([]*reliantv1.SyncedRepoState, 0, len(heads))
	for _, h := range heads {
		d := cached.dirty[h.repo]
		var errs []string
		for _, e := range []string{h.err, d.err} {
			if e != "" {
				errs = append(errs, e)
			}
		}
		out = append(out, &reliantv1.SyncedRepoState{
			Repo:          h.repo,
			Branch:        h.branch,
			CommitSha:     h.commit,
			Dirty:         d.dirty,
			DefaultBranch: h.defaultBranch,
			Error:         strings.Join(errs, "; "),
		})
	}
	return out
}

// reliantDirDirty reports whether dir's .reliant directory has uncommitted
// changes. GIT_OPTIONAL_LOCKS=0 keeps it from taking index.lock, which an
// agent's git command in the same checkout would otherwise trip over.
func reliantDirDirty(ctx context.Context, dir string) (bool, error) {
	ctx, cancel := context.WithTimeout(ctx, repoStateDirtyTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", "-C", dir, "status", "--porcelain", "--untracked-files=all", "--", ".reliant")
	cmd.Env = append(os.Environ(), "GIT_OPTIONAL_LOCKS=0", "GIT_TERMINAL_PROMPT=0")
	out, err := cmd.Output()
	if err != nil {
		return false, fmt.Errorf("git status .reliant: %w", err)
	}
	return strings.TrimSpace(string(out)) != "", nil
}

// readRepoHead reads dir's HEAD from its git directory, without running git.
// ok is false when dir is not a git checkout.
func readRepoHead(dir, repo string) (repoHead, bool) {
	gitDir, commonDir, ok := gitDirs(dir)
	if !ok {
		return repoHead{}, false
	}
	h := repoHead{repo: repo, dir: dir, dirtyInputs: dirtyInputs(dir, gitDir, commonDir)}
	head, err := os.ReadFile(filepath.Join(gitDir, "HEAD"))
	if err != nil {
		h.err = fmt.Sprintf("read HEAD: %v", err)
		return h, true
	}
	ref, symbolic := strings.CutPrefix(strings.TrimSpace(string(head)), "ref: ")
	if symbolic {
		h.branch = strings.TrimPrefix(ref, "refs/heads/")
		h.commit = resolveRef(gitDir, commonDir, ref)
	} else {
		h.commit = ref
	}
	if origin, err := os.ReadFile(filepath.Join(commonDir, "refs", "remotes", "origin", "HEAD")); err == nil {
		if target, ok := strings.CutPrefix(strings.TrimSpace(string(origin)), "ref: refs/remotes/origin/"); ok {
			h.defaultBranch = target
		}
	}
	return h, true
}

// gitDirs is dir's git directory and the common directory its refs live in:
// .git itself for a clone, or the worktree's own directory and the main
// repository's for a linked worktree (a .git FILE naming its gitdir).
func gitDirs(dir string) (gitDir, commonDir string, ok bool) {
	dotGit := filepath.Join(dir, ".git")
	fi, err := os.Stat(dotGit)
	if err != nil {
		return "", "", false
	}
	gitDir = dotGit
	if !fi.IsDir() {
		data, err := os.ReadFile(dotGit)
		if err != nil {
			return "", "", false
		}
		target, found := strings.CutPrefix(strings.TrimSpace(string(data)), "gitdir: ")
		if !found {
			return "", "", false
		}
		if !filepath.IsAbs(target) {
			target = filepath.Join(dir, target)
		}
		gitDir = target
	}
	commonDir = gitDir
	if data, err := os.ReadFile(filepath.Join(gitDir, "commondir")); err == nil {
		c := strings.TrimSpace(string(data))
		if !filepath.IsAbs(c) {
			c = filepath.Join(gitDir, c)
		}
		commonDir = filepath.Clean(c)
	}
	return gitDir, commonDir, true
}

// resolveRef is ref's commit: a loose ref (the worktree's own first, for
// per-worktree refs, then the common directory's), else packed-refs. "" for a
// ref with no commit yet.
func resolveRef(gitDir, commonDir, ref string) string {
	for _, d := range []string{gitDir, commonDir} {
		if data, err := os.ReadFile(filepath.Join(d, filepath.FromSlash(ref))); err == nil {
			return strings.TrimSpace(string(data))
		}
	}
	f, err := os.Open(filepath.Join(commonDir, "packed-refs"))
	if err != nil {
		return ""
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "#") || strings.HasPrefix(line, "^") {
			continue
		}
		if sha, name, ok := strings.Cut(line, " "); ok && name == ref {
			return sha
		}
	}
	return ""
}

// statesVersion is the part of a snapshot's version the repo states
// contribute: a recovered (or newly failed) read sends a new snapshot.
func statesVersion(states []*reliantv1.SyncedRepoState) []byte {
	var b strings.Builder
	for _, s := range states {
		fmt.Fprintf(&b, "%s:%t:%s;", s.GetRepo(), s.GetDirty(), s.GetError())
	}
	return []byte(b.String())
}
