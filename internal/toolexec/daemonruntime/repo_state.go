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
// process, so it is computed only when the version moved; a dirty state can
// only change with the files or HEAD, both of which move the version.

// repoHead is a repository's HEAD as read from its git directory.
type repoHead struct {
	repo          string
	dir           string
	branch        string
	commit        string
	defaultBranch string
	err           string
}

// repoDirtyCache remembers each project's last computed dirty flags by the
// snapshot version they were computed for.
var repoDirtyCache = struct {
	sync.Mutex
	byProject map[string]repoDirtyEntry
}{byProject: map[string]repoDirtyEntry{}}

type repoDirtyEntry struct {
	version string
	dirty   map[string]repoDirty
}

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

// headsVersion is the part of a snapshot's version the repo heads contribute.
func headsVersion(heads []repoHead) []byte {
	var b strings.Builder
	for _, h := range heads {
		fmt.Fprintf(&b, "%s@%s:%s:%s;", h.repo, h.branch, h.commit, h.defaultBranch)
	}
	return []byte(b.String())
}

// repoStates completes heads with each repo's dirty flag, computing it only
// when version differs from the one last computed for projectPath.
func repoStates(ctx context.Context, projectPath, version string, heads []repoHead) []*reliantv1.SyncedRepoState {
	if len(heads) == 0 {
		return nil
	}
	repoDirtyCache.Lock()
	cached, ok := repoDirtyCache.byProject[projectPath]
	repoDirtyCache.Unlock()
	if !ok || cached.version != version {
		cached = repoDirtyEntry{version: version, dirty: map[string]repoDirty{}}
		for _, h := range heads {
			dirty, err := reliantDirDirty(ctx, h.dir)
			d := repoDirty{dirty: dirty}
			if err != nil {
				d.err = err.Error()
			}
			cached.dirty[h.repo] = d
		}
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
	h := repoHead{repo: repo, dir: dir}
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
